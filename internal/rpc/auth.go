package rpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/catalog"
	"github.com/notshekhar/pi/internal/core/config"
)

// rpc/auth-flows.ts and rpc/custom-providers.ts: signing in from a client
// that cannot hold a terminal prompt open.

// builtinProviderIDs is loop's BUILTIN_PROVIDER_IDS.
var builtinProviderIDs = []string{"xai", "anthropic", "openai", "google", "openrouter", "github-copilot", "deepseek",
	"mistral", "glm", "zai", "kimi", "groq", "cerebras", "zenmux", "vercel", "bedrock", "ollama"}

// oauthProviders are the OAuth logins pi can actually run.
var oauthProviders = map[string]string{"xai": "xai"}

var detectProviders = map[string]bool{"bedrock": true, "ollama": true}

func customFor(id string) (config.CustomProvider, bool) {
	if !strings.HasPrefix(id, config.CustomPrefix) {
		return config.CustomProvider{}, false
	}
	return config.LookupCustom(id)
}

// authMethodsFor is which methods a provider offers, most preferred first.
func authMethodsFor(id string) []string {
	if strings.HasPrefix(id, config.CustomPrefix) {
		if c, ok := customFor(id); ok && c.NormalizedAuth().Kind == "oauth" {
			return []string{"oauth"}
		}
		return []string{}
	}
	if detectProviders[id] {
		return []string{"detect"}
	}
	var methods []string
	if _, ok := oauthProviders[id]; ok {
		methods = append(methods, "oauth")
	}
	if id != "github-copilot" {
		methods = append(methods, "apikey")
	}
	return methods
}

// apiKeyEnvVar is the env var a key falls back to.
func apiKeyEnvVar(id string) string {
	if strings.HasPrefix(id, config.CustomPrefix) {
		if c, ok := customFor(id); ok {
			if a := c.NormalizedAuth(); a.Kind == "env" {
				return a.Var
			}
		}
		return ""
	}
	switch {
	case detectProviders[id], id == "github-copilot":
		return ""
	case id == "vercel":
		return "AI_GATEWAY_API_KEY"
	}
	return strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_API_KEY"
}

func describeCustomAuth(a config.CustomAuth) string {
	switch a.Kind {
	case "apikey":
		return "api key"
	case "bearer":
		return "bearer token"
	case "env":
		return "env $" + a.Var
	case "helper":
		return "helper: " + a.Command
	case "oauth":
		var opts struct {
			Issuer string `json:"issuer"`
		}
		_ = json.Unmarshal(a.OAuth, &opts)
		if opts.Issuer != "" {
			return "oauth: " + opts.Issuer
		}
		return "oauth (browser sign-in)"
	}
	return "no credential"
}

// providerDescriptors is loop's listProviderDescriptors.
func providerDescriptors() []map[string]any {
	authorized := map[string]bool{}
	for _, p := range config.ListAuthorizedProviders() {
		authorized[p] = true
	}
	var out []map[string]any
	for _, id := range builtinProviderIDs {
		env := apiKeyEnvVar(id)
		d := map[string]any{"id": id, "kind": "builtin",
			"authorized": authorized[id] || (env != "" && os.Getenv(env) != ""),
			"mode":       config.AuthMode(id), "methods": authMethodsFor(id)}
		if env != "" {
			d["envVar"] = env
		}
		if id == "openai" && authorized["openai-chatgpt"] {
			d["authorized"], d["mode"] = true, config.AuthMode("openai-chatgpt")
		}
		out = append(out, d)
	}
	for _, name := range config.CustomProviderNames() {
		c, _ := config.LookupCustom(name)
		a := c.NormalizedAuth()
		mode := "apikey"
		switch a.Kind {
		case "oauth":
			mode = "oauth"
		case "none":
			mode = "missing"
		}
		d := map[string]any{"id": c.ID(), "kind": "custom", "authorized": a.Kind != "oauth",
			"mode": mode, "methods": authMethodsFor(c.ID()), "baseURL": c.BaseURL, "sdk": c.SDK}
		if a.Kind == "env" {
			d["envVar"] = a.Var
		}
		out = append(out, d)
	}
	return out
}

// usableProviders is loop's listUsableProviders: logged-in providers,
// zero-login ones the catalog found, and every custom gateway.
func usableProviders() []string {
	providers := config.ListAuthorizedProviders()
	seen := map[string]bool{}
	for _, p := range providers {
		seen[p] = true
	}
	for _, m := range catalog.All() {
		if m.Available && (m.Provider == "ollama" || m.Provider == "bedrock") && !seen[m.Provider] {
			providers = append(providers, m.Provider)
			seen[m.Provider] = true
		}
	}
	for _, name := range config.CustomProviderNames() {
		id := config.CustomPrefix + name
		if !seen[id] {
			providers = append(providers, id)
			seen[id] = true
		}
	}
	return providers
}

// ─── flows ───────────────────────────────────────────────────────────────

type flowEvent map[string]any

type authFlow struct {
	mu         sync.Mutex
	id         string
	provider   string
	events     []flowEvent
	status     string
	cancel     context.CancelFunc
	finishedAt time.Time
}

var (
	flowsMu sync.Mutex
	flows   = map[string]*authFlow{}
)

const flowRetention = time.Minute

func sweepFlows() {
	cutoff := time.Now().Add(-flowRetention)
	for id, f := range flows {
		if !f.finishedAt.IsZero() && f.finishedAt.Before(cutoff) {
			delete(flows, id)
		}
	}
}

func (f *authFlow) emit(e flowEvent) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
}

func (f *authFlow) finish(status string, e flowEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status != "running" {
		return
	}
	f.status, f.finishedAt = status, time.Now()
	f.events = append(f.events, e)
}

func beginFlow(provider string) (*authFlow, string) {
	flowsMu.Lock()
	defer flowsMu.Unlock()
	sweepFlows()
	for _, f := range flows {
		f.mu.Lock()
		running := f.provider == provider && f.status == "running"
		f.mu.Unlock()
		if running {
			return nil, f.id
		}
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	f := &authFlow{id: hex.EncodeToString(b[:]), provider: provider, status: "running"}
	flows[f.id] = f
	return f, ""
}

func ollamaBaseURL() string {
	for _, env := range []string{"LOOP_OLLAMA_BASE_URL", "OLLAMA_BASE_URL"} {
		if v := os.Getenv(env); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return "http://localhost:11434"
}

func runFlow(ctx context.Context, f *authFlow, method string) error {
	if method == "detect" {
		switch f.provider {
		case "ollama":
			f.emit(flowEvent{"type": "progress", "message": "Checking the local Ollama daemon…"})
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ollamaBaseURL()+"/api/tags", nil)
			resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
			if err != nil {
				return fmt.Errorf("Ollama is not reachable at %s. Start it (`ollama serve`) or set LOOP_OLLAMA_BASE_URL.", ollamaBaseURL())
			}
			defer resp.Body.Close()
			var body struct {
				Models []struct {
					Name string `json:"name"`
				} `json:"models"`
			}
			if json.NewDecoder(resp.Body).Decode(&body) != nil || len(body.Models) == 0 {
				return errors.New("Ollama is running but no models are installed. Pull one, e.g. `ollama pull llama3.2`.")
			}
			if err := config.SaveKey("ollama", "local"); err != nil {
				return err
			}
			_ = config.SetActiveProvider("ollama")
			n := len(body.Models)
			f.finish("done", flowEvent{"type": "done", "message": fmt.Sprintf("Ollama connected — %d model%s installed.", n, plural(n))})
			return nil
		case "bedrock":
			return errors.New("Bedrock sign-in is not available in pi yet — use loop for Bedrock")
		}
		return fmt.Errorf("%s has nothing to detect", f.provider)
	}
	target, ok := oauthProviders[f.provider]
	if !ok {
		return fmt.Errorf("%s does not support OAuth", f.provider)
	}
	if target == "xai" {
		if err := config.XaiLogin(ctx, func(url, instructions string) {
			e := flowEvent{"type": "auth", "url": url}
			if instructions != "" {
				e["instructions"] = instructions
			}
			f.emit(e)
		}); err != nil {
			return err
		}
		_ = config.SetActiveProvider("xai")
	}
	catalog.Bust()
	f.finish("done", flowEvent{"type": "done", "message": "Signed in to " + f.provider + "."})
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ─── custom provider drafts ──────────────────────────────────────────────

var customSDKs = []string{"anthropic", "openai", "google", "openai-compatible"}

var customName = regexp.MustCompile(`^[a-z0-9-]+$`)

func requireString(v any, field string) (string, error) {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("%s required", field)
	}
	return strings.TrimSpace(s), nil
}

func parseCustomAuth(v any, existing *config.CustomProvider) (config.CustomAuth, error) {
	m, ok := v.(map[string]any)
	if !ok {
		if existing != nil {
			return existing.NormalizedAuth(), nil
		}
		return config.CustomAuth{Kind: "none"}, nil
	}
	kind, _ := m["kind"].(string)
	switch kind {
	case "keep":
		if existing == nil {
			return config.CustomAuth{}, errors.New("no saved credential to keep")
		}
		return existing.NormalizedAuth(), nil
	case "apikey":
		k, err := requireString(m["apiKey"], "apiKey")
		return config.CustomAuth{Kind: "apikey", APIKey: k}, err
	case "bearer":
		k, err := requireString(m["token"], "token")
		return config.CustomAuth{Kind: "bearer", Token: k}, err
	case "env":
		k, err := requireString(m["var"], "var")
		return config.CustomAuth{Kind: "env", Var: k}, err
	case "helper":
		k, err := requireString(m["command"], "command")
		a := config.CustomAuth{Kind: "helper", Command: k}
		if ttl, ok := m["ttlMs"].(float64); ok {
			a.TTLMs = int64(ttl)
		}
		return a, err
	case "oauth":
		a := config.CustomAuth{Kind: "oauth"}
		if opts, ok := m["oauth"].(map[string]any); ok && len(opts) > 0 {
			a.OAuth, _ = json.Marshal(opts)
		}
		return a, nil
	case "none":
		return config.CustomAuth{Kind: "none"}, nil
	}
	if kind == "" {
		kind = "(missing)"
	}
	return config.CustomAuth{}, fmt.Errorf("unknown auth kind: %s", kind)
}

func parseCustomModels(v any) []config.CustomModel {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := []config.CustomModel{}
	for _, entry := range list {
		if s, ok := entry.(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, config.CustomModel{ID: s})
			}
			continue
		}
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(pstr(m, "id"))
		if id == "" {
			continue
		}
		cm := config.CustomModel{ID: id, Name: strings.TrimSpace(pstr(m, "name"))}
		if n, ok := m["contextWindow"].(float64); ok {
			cm.ContextWindow = int(n)
		}
		if n, ok := m["maxOutput"].(float64); ok {
			cm.MaxOutput = int(n)
		}
		if c, ok := m["cost"].(map[string]any); ok {
			cost := &config.CustomCost{}
			cost.Input, _ = c["input"].(float64)
			cost.Output, _ = c["output"].(float64)
			cost.CacheRead, _ = c["cacheRead"].(float64)
			cost.CacheWrite, _ = c["cacheWrite"].(float64)
			cm.Cost = cost
		}
		out = append(out, cm)
	}
	return out
}

// parseCustomDraft is loop's parseCustomProviderDraft → config.
func parseCustomDraft(v any) (config.CustomProvider, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return config.CustomProvider{}, errors.New("custom provider config required")
	}
	name, err := requireString(m["name"], "name")
	if err != nil {
		return config.CustomProvider{}, err
	}
	name = strings.ToLower(name)
	if !customName.MatchString(name) {
		return config.CustomProvider{}, errors.New("name must be lowercase letters, digits, hyphens")
	}
	sdk, err := requireString(m["sdk"], "sdk")
	if err != nil {
		return config.CustomProvider{}, err
	}
	valid := false
	for _, s := range customSDKs {
		if s == sdk {
			valid = true
		}
	}
	if !valid {
		return config.CustomProvider{}, fmt.Errorf("sdk must be one of: %s", strings.Join(customSDKs, ", "))
	}
	baseURL, err := requireString(m["baseURL"], "baseURL")
	if err != nil {
		return config.CustomProvider{}, err
	}
	var existing *config.CustomProvider
	if c, ok := config.LookupCustom(name); ok {
		existing = &c
	}
	auth, err := parseCustomAuth(m["auth"], existing)
	if err != nil {
		return config.CustomProvider{}, err
	}
	p := config.CustomProvider{Name: name, SDK: sdk, BaseURL: baseURL, Auth: &auth}
	if auth.Kind == "apikey" {
		p.APIKey = auth.APIKey
	}
	if h, ok := m["headers"].(map[string]any); ok {
		headers := map[string]string{}
		for k, v := range h {
			if s, ok := v.(string); ok && strings.TrimSpace(k) != "" {
				headers[strings.TrimSpace(k)] = s
			}
		}
		if len(headers) > 0 {
			p.Headers = headers
		}
	}
	p.Models = parseCustomModels(m["models"])
	return p, nil
}

func customSummary(c config.CustomProvider) map[string]any {
	a := c.NormalizedAuth()
	out := map[string]any{"id": c.ID(), "name": c.Name, "sdk": c.SDK, "baseURL": c.BaseURL,
		"authKind": a.Kind, "authDescription": describeCustomAuth(a),
		"hasStoredSecret": a.Kind == "apikey" || a.Kind == "bearer", "hasOAuthSession": false}
	if a.Kind == "env" {
		out["envVar"] = a.Var
	}
	if a.Kind == "helper" {
		out["helperCommand"] = a.Command
	}
	if c.Headers != nil {
		out["headers"] = c.Headers
	}
	models := []config.CustomModel{}
	models = append(models, c.Models...)
	out["models"] = models
	return out
}

func customNameParam(p map[string]any) (string, error) {
	raw, err := requireString(p["name"], "name")
	if err != nil {
		return "", err
	}
	name := strings.TrimPrefix(raw, config.CustomPrefix)
	if _, ok := config.LookupCustom(name); !ok {
		return "", fmt.Errorf("no custom provider named %s", name)
	}
	return name, nil
}

// ─── handlers ────────────────────────────────────────────────────────────

func (s *Server) authHandlers() map[string]handler {
	return map[string]handler{
		"auth.status": func(map[string]any, Transport) (any, error) {
			var active any
			if a := config.ActiveProvider(); a != "" {
				active = a
			}
			return map[string]any{"providers": usableProviders(), "authorized": config.ListAuthorizedProviders(), "active": active}, nil
		},
		"auth.providers": func(map[string]any, Transport) (any, error) {
			var active any
			if a := config.ActiveProvider(); a != "" {
				active = a
			}
			return map[string]any{"providers": providerDescriptors(), "active": active}, nil
		},
		"auth.login": func(p map[string]any, _ Transport) (any, error) {
			prov, key := pstr(p, "provider"), pstr(p, "apiKey")
			if prov == "" || key == "" {
				return nil, errors.New("provider and apiKey required")
			}
			if err := config.SaveKey(prov, key); err != nil {
				return nil, err
			}
			catalog.Bust()
			return map[string]any{"ok": true}, nil
		},
		"auth.logout": func(p map[string]any, _ Transport) (any, error) {
			prov := pstr(p, "provider")
			_ = config.Logout(prov)
			if config.IsCustom(prov) {
				_ = config.RemoveCustomProvider(prov)
			}
			catalog.Bust()
			return map[string]any{"ok": true}, nil
		},
		"auth.flow.start": func(p map[string]any, _ Transport) (any, error) {
			if p["custom"] != nil {
				return nil, errors.New("custom gateway browser sign-in is not available in pi yet")
			}
			prov := strings.TrimSpace(pstr(p, "provider"))
			if prov == "" {
				return nil, errors.New("provider required")
			}
			available := authMethodsFor(prov)
			method := pstr(p, "method")
			if method == "" {
				for _, m := range available {
					if m != "apikey" {
						method = m
						break
					}
				}
				if method == "" && len(available) > 0 {
					method = available[0]
				}
			}
			if method == "" {
				return nil, fmt.Errorf("%s has no interactive login", prov)
			}
			if method == "apikey" {
				return nil, errors.New("API-key logins go through auth.login, not auth.flow.start")
			}
			ok := false
			for _, m := range available {
				if m == method {
					ok = true
				}
			}
			if !ok {
				return nil, fmt.Errorf("%s does not support %s login", prov, method)
			}
			f, existing := beginFlow(prov)
			if existing != "" {
				return map[string]any{"flowId": existing, "provider": prov, "method": method}, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			f.cancel = cancel
			go func() {
				defer cancel()
				if err := runFlow(ctx, f, method); err != nil {
					f.finish("error", flowEvent{"type": "error", "message": err.Error()})
				}
			}()
			return map[string]any{"flowId": f.id, "provider": prov, "method": method}, nil
		},
		"auth.flow.poll": func(p map[string]any, _ Transport) (any, error) {
			flowsMu.Lock()
			f, ok := flows[pstr(p, "flowId")]
			flowsMu.Unlock()
			if !ok {
				return nil, fmt.Errorf("unknown login flow: %s", pstr(p, "flowId"))
			}
			cursor := 0
			if n, ok := pnum(p, "cursor"); ok {
				cursor = int(n)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			from := max(0, min(cursor, len(f.events)))
			return map[string]any{"status": f.status, "cursor": len(f.events), "events": append([]flowEvent{}, f.events[from:]...)}, nil
		},
		"auth.flow.answer": func(p map[string]any, _ Transport) (any, error) {
			flowsMu.Lock()
			_, ok := flows[pstr(p, "flowId")]
			flowsMu.Unlock()
			if !ok {
				return nil, fmt.Errorf("unknown login flow: %s", pstr(p, "flowId"))
			}
			return nil, errors.New("this login is not waiting for an answer")
		},
		"auth.flow.cancel": func(p map[string]any, _ Transport) (any, error) {
			flowsMu.Lock()
			f, ok := flows[pstr(p, "flowId")]
			flowsMu.Unlock()
			if ok {
				if f.cancel != nil {
					f.cancel()
				}
				f.finish("cancelled", flowEvent{"type": "error", "message": "Login cancelled."})
			}
			return map[string]any{"ok": true}, nil
		},
		"auth.custom.list": func(map[string]any, Transport) (any, error) {
			out := []map[string]any{}
			for _, name := range config.CustomProviderNames() {
				c, _ := config.LookupCustom(name)
				out = append(out, customSummary(c))
			}
			return map[string]any{"providers": out}, nil
		},
		"auth.custom.discover": func(p map[string]any, _ Transport) (any, error) {
			draft, err := parseCustomDraft(p)
			if err != nil {
				return nil, err
			}
			found := config.DiscoverModels(context.Background(), draft)
			if len(found) == 0 {
				return map[string]any{"models": nil}, nil
			}
			out := []map[string]any{}
			for _, m := range found {
				row := map[string]any{"id": m.ID}
				if m.Name != "" {
					row["name"] = m.Name
				}
				if m.ContextWindow != 0 {
					row["contextWindow"] = m.ContextWindow
				}
				if m.MaxOutput != 0 {
					row["maxOutput"] = m.MaxOutput
				}
				out = append(out, row)
			}
			return map[string]any{"models": out}, nil
		},
		"auth.custom.save": func(p map[string]any, _ Transport) (any, error) {
			draft, err := parseCustomDraft(p)
			if err != nil {
				return nil, err
			}
			if len(draft.Models) == 0 {
				return nil, errors.New("at least one model id is required")
			}
			if err := config.SaveCustomProvider(draft); err != nil {
				return nil, err
			}
			catalog.Bust()
			return map[string]any{"ok": true, "id": draft.ID(), "name": draft.Name, "models": len(draft.Models)}, nil
		},
		"auth.custom.remove": func(p map[string]any, _ Transport) (any, error) {
			name, err := customNameParam(p)
			if err != nil {
				return nil, err
			}
			if err := config.RemoveCustomProvider(name); err != nil {
				return nil, err
			}
			catalog.Bust()
			return map[string]any{"ok": true}, nil
		},
		"auth.custom.setActive": func(p map[string]any, _ Transport) (any, error) {
			name, err := customNameParam(p)
			if err != nil {
				return nil, err
			}
			if err := config.SetActiveProvider(config.CustomPrefix + name); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		},
	}
}
