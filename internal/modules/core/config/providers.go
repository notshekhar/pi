package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/modules/core/catalog"
)

// User-defined providers: any OpenAI-compatible endpoint, named and used like
// a built-in.
//
// Most "new" providers are an OpenAI-compatible URL and a key, so shipping a
// code change for each is the wrong shape. A custom provider is three fields
// in settings and it appears in `/provider` beside the built-ins.

// CustomProvider is an OpenAI-compatible endpoint.
type CustomProvider struct {
	// BaseURL is the API root, e.g. https://api.example.com/v1
	BaseURL string `json:"baseUrl"`
	// APIKey is the credential. Empty means read it from EnvVar or the
	// provider's own entry in the auth file.
	APIKey string `json:"apiKey,omitempty"`
	// EnvVar names an environment variable holding the key, which keeps the
	// secret out of the settings file.
	EnvVar string `json:"envVar,omitempty"`
	// Models are the ids this endpoint serves. Without them the provider
	// works but `/model` has nothing to offer.
	Models []string `json:"models,omitempty"`
	// Context is the window for its models, when they share one.
	Context int `json:"context,omitempty"`
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
func (p CustomProvider) CustomKey() string {
	if p.APIKey != "" {
		return p.APIKey
	}
	if p.EnvVar != "" {
		return envValue(p.EnvVar)
	}
	return ""
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
