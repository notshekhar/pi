package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/catalog"
	"github.com/notshekhar/pi/internal/core/jsonstore"
)

// User-defined providers, stored where loop stores them: auth.json's
// `customProviders`, keyed by name, in loop's CustomProviderConfig shape
// (types.ts). A custom provider's id is "custom:<name>", and its models are
// "custom:<name>/<model>" — the ids loop writes into sessions and settings.

// CustomPrefix marks a custom provider id.
const CustomPrefix = "custom:"

// CustomProvider is loop's CustomProviderConfig.
type CustomProvider struct {
	Name string `json:"name"`
	// SDK is the API shape: "openai" (default), "anthropic" or "google".
	SDK     string `json:"sdk"`
	BaseURL string `json:"baseURL"`
	// APIKey is loop's legacy flat key, mirrored from an apikey Auth.
	APIKey  string            `json:"apiKey"`
	Auth    *CustomAuth       `json:"auth,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Models  []CustomModel     `json:"models,omitempty"`
}

// CustomAuth is loop's CustomProviderAuth union, flattened.
type CustomAuth struct {
	// Kind is apikey | bearer | env | helper | oauth | none.
	Kind    string          `json:"kind"`
	APIKey  string          `json:"apiKey,omitempty"`
	Token   string          `json:"token,omitempty"`
	Var     string          `json:"var,omitempty"`
	Command string          `json:"command,omitempty"`
	TTLMs   int64           `json:"ttlMs,omitempty"`
	OAuth   json.RawMessage `json:"oauth,omitempty"`
}

// CustomModel is one model a custom provider exposes.
type CustomModel struct {
	ID            string      `json:"id"`
	Name          string      `json:"name,omitempty"`
	ContextWindow int         `json:"contextWindow,omitempty"`
	MaxOutput     int         `json:"maxOutput,omitempty"`
	Cost          *CustomCost `json:"cost,omitempty"`
}

// CustomCost is $/MTok.
type CustomCost struct {
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cacheRead,omitempty"`
	CacheWrite float64 `json:"cacheWrite,omitempty"`
}

// ID is the provider id: "custom:<name>".
func (p CustomProvider) ID() string { return CustomPrefix + p.Name }

// IsCustom reports a "custom:" provider id.
func IsCustom(id string) bool { return strings.HasPrefix(id, CustomPrefix) }

// ModelIDs is just the ids.
func (p CustomProvider) ModelIDs() []string {
	out := make([]string, 0, len(p.Models))
	for _, m := range p.Models {
		out = append(out, m.ID)
	}
	return out
}

// LookupModel finds one of this provider's models by id.
func (p CustomProvider) LookupModel(id string) (CustomModel, bool) {
	for _, m := range p.Models {
		if m.ID == id {
			return m, true
		}
	}
	return CustomModel{}, false
}

// NormalizedAuth is the provider's auth, derived from the legacy flat key
// when absent (loop's normalizeCustomAuth).
func (p CustomProvider) NormalizedAuth() CustomAuth {
	if p.Auth != nil {
		return *p.Auth
	}
	if p.APIKey != "" {
		return CustomAuth{Kind: "apikey", APIKey: p.APIKey}
	}
	return CustomAuth{Kind: "none"}
}

// CustomProviders returns the configured endpoints keyed by name.
func CustomProviders() map[string]CustomProvider {
	out := map[string]CustomProvider{}
	AuthStore().Get("customProviders", &out)
	for name, p := range out {
		if p.Name == "" {
			p.Name = name
			out[name] = p
		}
	}
	return out
}

// CustomProviderNames lists them in a stable order.
func CustomProviderNames() []string {
	custom := CustomProviders()
	names := make([]string, 0, len(custom))
	for name := range custom {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// LookupCustom finds a custom provider by "custom:<name>" or bare name.
func LookupCustom(id string) (CustomProvider, bool) {
	name := strings.TrimPrefix(strings.TrimSpace(id), CustomPrefix)
	p, ok := CustomProviders()[name]
	return p, ok
}

// CustomKey resolves a custom provider's credential (loop's
// resolveCustomCredential for the non-OAuth kinds).
func (p CustomProvider) CustomKey() string {
	a := p.NormalizedAuth()
	switch a.Kind {
	case "apikey":
		return a.APIKey
	case "bearer":
		return a.Token
	case "env":
		return os.Getenv(a.Var)
	case "helper":
		return runKeyCommand(a.Command)
	}
	return ""
}

// keyCommandTimeout bounds a helper. A vault client that hangs must not hang
// the agent's first request.
const keyCommandTimeout = 10 * time.Second

// keyCommandTTL is how long a helper's output is reused. loop's five minutes.
//
// Without it the helper runs on EVERY request — forking a vault client per
// turn, which is both slow and, for an SSO helper that rate-limits, a way to
// get locked out mid-conversation.
const keyCommandTTL = 5 * time.Minute

var (
	keyCacheMu sync.Mutex
	keyCache   = map[string]cachedKey{}
)

type cachedKey struct {
	value string
	at    time.Time
}

// InvalidateKeyCommands drops every cached helper result.
//
// The caller for this is a 401: a token that the endpoint has just rejected is
// worth re-fetching immediately rather than waiting out the TTL, which is the
// difference between one failed turn and five minutes of them.
func InvalidateKeyCommands() {
	keyCacheMu.Lock()
	keyCache = map[string]cachedKey{}
	keyCacheMu.Unlock()
}

// runKeyCommand executes a key helper and returns its trimmed stdout.
//
// Errors are swallowed into "" on purpose: the caller's next step is the same
// either way — no credential — and the request that follows reports the
// failure with the context the user actually needs.
func runKeyCommand(command string) string {
	keyCacheMu.Lock()
	if hit, ok := keyCache[command]; ok && time.Since(hit.at) < keyCommandTTL {
		keyCacheMu.Unlock()
		return hit.value
	}
	keyCacheMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), keyCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-c", command).Output()
	if err != nil {
		return ""
	}
	key := strings.TrimSpace(string(out))
	// A failure is NOT cached: the next request should try again rather than
	// wait out five minutes of a credential the helper could produce now.
	if key != "" {
		keyCacheMu.Lock()
		keyCache[command] = cachedKey{value: key, at: time.Now()}
		keyCacheMu.Unlock()
	}
	return key
}

// SaveCustomProvider stores an endpoint (loop's saveCustomProvider): the
// legacy apiKey is mirrored from an apikey auth so older readers still work.
func SaveCustomProvider(p CustomProvider) error {
	p.Name = strings.TrimSpace(strings.TrimPrefix(p.Name, CustomPrefix))
	if p.Name == "" {
		return fmt.Errorf("a provider needs a name")
	}
	if !strings.HasPrefix(p.BaseURL, "http://") && !strings.HasPrefix(p.BaseURL, "https://") {
		return fmt.Errorf("baseURL must be an http(s) URL")
	}
	if p.SDK == "" {
		p.SDK = "openai"
	}
	if p.Auth != nil {
		if p.Auth.Kind == "apikey" {
			p.APIKey = p.Auth.APIKey
		} else {
			p.APIKey = ""
		}
	}
	err := AuthStore().Mutate(func(o *jsonstore.Object) {
		all := jsonstore.NewObject()
		if raw := o.Raw("customProviders"); raw != nil {
			if parsed, err := jsonstore.ParseObject(raw); err == nil {
				all = parsed
			}
		}
		_ = all.Set(p.Name, p)
		raw, _ := all.MarshalJSON()
		o.SetRaw("customProviders", raw)
	})
	if err != nil {
		return err
	}
	ensureActive(p.ID())
	return nil
}

// AddCustomProvider stores an endpoint under name.
func AddCustomProvider(name string, p CustomProvider) error {
	p.Name = name
	return SaveCustomProvider(p)
}

// RemoveCustomProvider forgets one.
func RemoveCustomProvider(id string) error {
	p, ok := LookupCustom(id)
	if !ok {
		return fmt.Errorf("no custom provider named %q", id)
	}
	err := AuthStore().Mutate(func(o *jsonstore.Object) {
		if raw := o.Raw("customProviders"); raw != nil {
			if all, err := jsonstore.ParseObject(raw); err == nil {
				all.Delete(p.Name)
				out, _ := all.MarshalJSON()
				o.SetRaw("customProviders", out)
			}
		}
	})
	if err != nil {
		return err
	}
	if ActiveProvider() == p.ID() {
		return AuthStore().Set("active", nil)
	}
	return nil
}

// ScopedModel is the model to use for a named scope, or "" for the session's.
//
// Scopes exist because the right model differs by job: summarising a
// conversation and delegating a search do not need the model you chose for
// the conversation itself, and paying for it is a real cost on a long session.
func ScopedModel(scope string) string {
	s := LoadSettings()
	switch scope {
	case "subagent":
		return s.SubagentModel
	}
	return ""
}

// ForScope returns the config to use for a named scope, falling back to the
// session's own model when nothing is set.
func (c Config) ForScope(scope string) Config {
	id := ScopedModel(scope)
	if id == "" {
		return c
	}
	scoped := c
	provider, model := catalog.Parse(id, c.Provider)
	scoped.Provider, scoped.ModelID = provider, model
	return scoped
}

// ModelInfo is what is known about a model, from the catalog OR from a custom
// provider's own configuration.
//
// One resolver, because every caller wants the same three facts — the display
// name, the context window, and the price — and `catalog.Lookup` alone knows
// nothing about a user-defined endpoint. That gap was not cosmetic: a custom
// model resolved to the zero value, so its context window was 0 (auto-
// compaction could never trigger) and its rates were 0, which reports every
// turn as free. Costing work at zero is the one wrong answer a cost report
// must not give.
func ModelInfo(provider, modelID string) (catalog.Model, bool) {
	if m, ok := catalog.Lookup(provider, modelID, APIKey(provider)); ok {
		return m, true
	}
	p, ok := LookupCustom(provider)
	if !ok {
		return catalog.Model{}, false
	}
	custom, ok := p.LookupModel(modelID)
	if !ok {
		return catalog.Model{}, false
	}
	out := catalog.Model{
		ID:       provider + "/" + custom.ID,
		Provider: provider,
		ShortID:  custom.ID,
		Name:     firstNonEmpty(custom.Name, custom.ID),
		Context:  custom.ContextWindow,
		MaxOut:   custom.MaxOutput,
	}
	if out.Context == 0 {
		out.Context = 128_000
	}
	if custom.Cost != nil {
		out.Cost = catalog.Cost{
			Input:      custom.Cost.Input,
			Output:     custom.Cost.Output,
			CacheRead:  custom.Cost.CacheRead,
			CacheWrite: custom.Cost.CacheWrite,
		}
	}
	return out, true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
