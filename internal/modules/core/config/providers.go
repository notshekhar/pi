package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/modules/core/catalog"
)

// User-defined providers: any OpenAI-compatible endpoint, named and used like
// a built-in.
//
// Most "new" providers are an OpenAI-compatible URL and a key, so shipping a
// code change for each is the wrong shape. A custom provider is three fields
// in settings and it appears in `/provider` beside the built-ins.

// CustomProvider is a user-defined endpoint.
type CustomProvider struct {
	// SDK is the API SHAPE the endpoint speaks: "openai" (the default),
	// "anthropic", or "google".
	//
	// Not cosmetic and not guessable from the URL. A gateway commonly exposes
	// several surfaces — bifrost serves /anthropic and /openai from one host —
	// and sending Claude's message shape to a chat-completions endpoint fails
	// in ways that read like a model problem rather than a routing one.
	SDK string `json:"sdk,omitempty"`
	// BaseURL is the API root, e.g. https://api.example.com/v1
	BaseURL string `json:"baseUrl"`
	// APIKey is the credential. Empty means read it from EnvVar or the
	// provider's own entry in the auth file.
	APIKey string `json:"apiKey,omitempty"`
	// EnvVar names an environment variable holding the key, which keeps the
	// secret out of the settings file.
	EnvVar string `json:"envVar,omitempty"`
	// KeyCommand is a shell command whose stdout IS the key. The path for a
	// vault or an SSO helper: nothing is stored, and a rotated credential is
	// picked up without editing anything.
	KeyCommand string `json:"keyCommand,omitempty"`
	// Headers are sent with every request. The way to reach an endpoint whose
	// credential is not a bearer token at all — a proxy wanting `x-api-key`,
	// a gateway wanting a tenant id.
	Headers map[string]string `json:"headers,omitempty"`
	// Models are the ids this endpoint serves, with whatever is known about
	// each. Without them the provider works but `/model` has nothing to
	// offer.
	Models []CustomModel `json:"models,omitempty"`
	// Context is the default window for models that do not state their own.
	Context int `json:"context,omitempty"`
}

// CustomModel is one model on a custom endpoint.
//
// The pricing fields are what make a custom provider appear in `/cost` at all.
// A gateway is not in the catalog, so nothing else knows what its tokens
// cost — and a model with no rate bills at zero, which is the one wrong answer
// a cost report can give: it looks like the work was free.
type CustomModel struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Context int    `json:"context,omitempty"`
	// MaxOutput is the output cap, 0 for the provider default.
	MaxOutput int `json:"maxOutput,omitempty"`
	// Reasoning marks a model that thinks, so the status line offers a level.
	Reasoning bool `json:"reasoning,omitempty"`
	// Cost is $/MTok, matching the catalog's units.
	Cost *CustomCost `json:"cost,omitempty"`
}

// CustomCost is $/MTok for a custom model.
type CustomCost struct {
	Input     float64 `json:"input,omitempty"`
	Output    float64 `json:"output,omitempty"`
	CacheRead float64 `json:"cacheRead,omitempty"`
}

// ModelIDs is just the ids, for the places that only need names.
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

// CustomProviders returns the configured endpoints, sorted by name.
func CustomProviders() map[string]CustomProvider {
	return LoadSettings().Providers
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

// LookupCustom finds a custom provider by name.
func LookupCustom(name string) (CustomProvider, bool) {
	p, ok := CustomProviders()[strings.ToLower(strings.TrimSpace(name))]
	return p, ok
}

// CustomKey resolves a custom provider's credential.
//
// Most specific first: a key typed in, then an environment variable, then a
// command. The command is last because it is the most expensive — it forks a
// process — and because someone who configured both a key and a helper meant
// the key.
func (p CustomProvider) CustomKey() string {
	if p.APIKey != "" {
		return p.APIKey
	}
	if p.EnvVar != "" {
		if v := envValue(p.EnvVar); v != "" {
			return v
		}
	}
	if p.KeyCommand != "" {
		return runKeyCommand(p.KeyCommand)
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

// AddCustomProvider stores an endpoint.
func AddCustomProvider(name string, p CustomProvider) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return fmt.Errorf("a provider needs a name")
	}
	if !strings.HasPrefix(p.BaseURL, "http://") && !strings.HasPrefix(p.BaseURL, "https://") {
		return fmt.Errorf("baseUrl must be an http(s) URL")
	}
	return Update(func(s *Settings) {
		if s.Providers == nil {
			s.Providers = map[string]CustomProvider{}
		}
		s.Providers[name] = p
	})
}

// RemoveCustomProvider forgets one.
func RemoveCustomProvider(name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if _, ok := LookupCustom(name); !ok {
		return fmt.Errorf("no custom provider named %q", name)
	}
	return Update(func(s *Settings) { delete(s.Providers, name) })
}

// envValue reads an environment variable.
func envValue(name string) string { return os.Getenv(name) }

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
	case "compact":
		return s.CompactModel
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

// SaveKey stores a provider credential in ~/.loop/auth.json.
//
// Writes the file loop already owns rather than starting a second one: the
// two programs share credentials, and a key entered in one should work in the
// other. Only the named provider is touched, so an unrelated entry is never
// clobbered.
func SaveKey(provider, key string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	key = strings.TrimSpace(key)
	if provider == "" || key == "" {
		return fmt.Errorf("a provider and a key are both required")
	}
	path := filepath.Join(home(), ".loop", "auth.json")

	doc := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("credentials file is not readable: %w", err)
		}
	}
	providers, _ := doc["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}
	// loop's entry SHAPE, not a bare string. This file is shared, and the
	// reader on both sides requires `{mode, provider, apiKey}` — a bare
	// string was written happily and then read back by nobody, so a key
	// entered here appeared to save and the provider stayed signed out.
	providers[provider] = map[string]any{
		"mode": "apikey", "provider": provider, "apiKey": key,
	}
	doc["providers"] = providers

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	// 0600: this file is credentials.
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// The shared credentials file, in one place.
//
// It belongs to loop as much as to pi-agent — the two read each other's
// logins on purpose — so every write goes through here: read the whole
// document, change one key, write it back atomically. Anything that edited it
// in place, or rewrote it from a struct, would drop the fields the other
// program stores and log the user out of it.

func authFilePath() string { return filepath.Join(home(), ".loop", "auth.json") }

func readAuthDoc() (map[string]any, error) {
	data, err := os.ReadFile(authFilePath())
	if err != nil {
		return nil, err
	}
	doc := map[string]any{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("credentials file is not readable: %w", err)
	}
	return doc, nil
}

func writeAuthDoc(doc map[string]any) error {
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	path := authFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	// 0600: this file is credentials.
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
		ID:        provider + "/" + custom.ID,
		Provider:  provider,
		ShortID:   custom.ID,
		Name:      firstNonEmpty(custom.Name, custom.ID),
		Context:   custom.Context,
		MaxOut:    custom.MaxOutput,
		Reasoning: custom.Reasoning,
	}
	if out.Context == 0 {
		out.Context = p.Context
	}
	if custom.Cost != nil {
		out.Cost = catalog.Cost{
			Input:     custom.Cost.Input,
			Output:    custom.Cost.Output,
			CacheRead: custom.Cost.CacheRead,
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
