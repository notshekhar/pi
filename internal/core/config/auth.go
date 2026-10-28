package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/home"
	"github.com/notshekhar/pi/internal/core/jsonstore"
)

// The shared credentials and config files — loop's auth/storage.ts.
//
// auth.json, settings.json and datasources.json are loop's files and pi
// writes them exactly as loop does. Several processes share them at once (the
// desktop app, `loop rpc`, terminals, pi), so every write to the provider map
// re-reads the file first: writing back a stale copy would restore a refresh
// token another process has already rotated, and providers that rotate treat
// a replayed token as theft and revoke the login.

var (
	storesMu sync.Mutex
	stores   = map[string]*jsonstore.Store{}
)

// store is the cached store for a file under ~/.loop, created on first use.
// Keyed by full path so a test (or a child) that points HOME elsewhere gets
// its own.
func store(name string, defaults func() *jsonstore.Object) *jsonstore.Store {
	path := home.Path(name)
	storesMu.Lock()
	defer storesMu.Unlock()
	s, ok := stores[path]
	if !ok {
		s = jsonstore.New(path, defaults())
		stores[path] = s
	}
	return s
}

// AuthStore is ~/.loop/auth.json.
func AuthStore() *jsonstore.Store {
	return store("auth.json", func() *jsonstore.Object {
		return jsonstore.Defaults("providers", map[string]any{}, "active", nil)
	})
}

// SettingsStore is ~/.loop/settings.json, with loop's defaults.
func SettingsStore() *jsonstore.Store {
	return store("settings.json", func() *jsonstore.Object {
		return jsonstore.Defaults(
			"defaultModel", nil,
			"theme", "dark",
			"maxSteps", 0,
			"autoCompactThreshold", 0.8,
			"workspaceContext", true,
		)
	})
}

// DatasourcesStore is ~/.loop/datasources.json.
func DatasourcesStore() *jsonstore.Store {
	return store("datasources.json", func() *jsonstore.Object {
		return jsonstore.Defaults("connections", map[string]any{})
	})
}

// RefreshConfigStores drops every cached file so the next read is from disk
// (loop's refreshConfigStores, what /reload means for the JSON layer).
func RefreshConfigStores() {
	AuthStore().Refresh()
	SettingsStore().Refresh()
	DatasourcesStore().Refresh()
}

// providerMap reads auth.json's providers, each entry raw.
func providerMap() map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	AuthStore().Get("providers", &out)
	return out
}

// updateProviders is a read-modify-write of the provider map against the
// file on disk, never the cache.
func updateProviders(mutate func(map[string]json.RawMessage)) error {
	s := AuthStore()
	return s.Mutate(func(o *jsonstore.Object) {
		providers := map[string]json.RawMessage{}
		o.Get("providers", &providers)
		// Keep the order loop wrote: rebuild through an ordered object.
		ordered := jsonstore.NewObject()
		if raw := o.Raw("providers"); raw != nil {
			if parsed, err := jsonstore.ParseObject(raw); err == nil {
				ordered = parsed
			}
		}
		mutate(providers)
		for _, k := range ordered.Keys() {
			if v, ok := providers[k]; ok {
				ordered.SetRaw(k, v)
			} else {
				ordered.Delete(k)
			}
		}
		for k, v := range providers {
			if !ordered.Has(k) {
				ordered.SetRaw(k, v)
			}
		}
		raw, _ := ordered.MarshalJSON()
		o.SetRaw("providers", raw)
	})
}

// authEntry is one provider's stored credential (loop's AuthEntry).
type authEntry struct {
	Mode     string          `json:"mode"`
	Provider string          `json:"provider"`
	APIKey   string          `json:"apiKey,omitempty"`
	Xai      json.RawMessage `json:"xai,omitempty"`
	Creds    json.RawMessage `json:"creds,omitempty"`
}

func readEntry(id string) (authEntry, bool) {
	raw, ok := providerMap()[id]
	if !ok {
		return authEntry{}, false
	}
	var e authEntry
	if json.Unmarshal(raw, &e) != nil {
		return authEntry{}, false
	}
	return e, true
}

// ListAuthorizedProviders is every provider with a stored credential.
func ListAuthorizedProviders() []string {
	out := []string{}
	if raw := AuthStore().Raw("providers"); raw != nil {
		if o, err := jsonstore.ParseObject(raw); err == nil {
			out = o.Keys()
		}
	}
	return out
}

// ActiveProvider is auth.json's `active`.
func ActiveProvider() string {
	var active string
	AuthStore().Get("active", &active)
	return active
}

// SetActiveProvider records the provider new sessions default to.
func SetActiveProvider(id string) error { return AuthStore().Set("active", id) }

func ensureActive(id string) {
	if ActiveProvider() == "" {
		_ = SetActiveProvider(id)
	}
}

// storedAPIKey is a provider's saved API key, "" when it has none.
func storedAPIKey(id string) string {
	e, ok := readEntry(id)
	if !ok || e.Mode != "apikey" {
		return ""
	}
	return e.APIKey
}

// AuthMode is "apikey", "oauth" or "missing" (loop's getAuthMode).
func AuthMode(id string) string {
	e, ok := readEntry(id)
	if !ok {
		return "missing"
	}
	return e.Mode
}

// SaveKey stores a provider API key in loop's entry shape.
func SaveKey(provider, key string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	key = strings.TrimSpace(key)
	if provider == "" || key == "" {
		return fmt.Errorf("a provider and a key are both required")
	}
	raw, _ := jsonstore.Marshal(map[string]any{"mode": "apikey", "provider": provider, "apiKey": key})
	if err := updateProviders(func(p map[string]json.RawMessage) { p[provider] = raw }); err != nil {
		return err
	}
	ensureActive(provider)
	return nil
}

// SaveOAuth stores a generic OAuth credential ({mode, provider, creds}).
func SaveOAuth(provider string, creds any) error {
	raw, err := jsonstore.Marshal(map[string]any{"mode": "oauth", "provider": provider, "creds": creds})
	if err != nil {
		return err
	}
	if err := updateProviders(func(p map[string]json.RawMessage) { p[provider] = raw }); err != nil {
		return err
	}
	ensureActive(provider)
	return nil
}

// Logout removes a provider's stored credential; "" removes them all.
func Logout(provider string) error {
	if provider == "" {
		if err := AuthStore().Set("providers", map[string]any{}); err != nil {
			return err
		}
		return AuthStore().Set("active", nil)
	}
	if _, ok := readEntry(provider); !ok {
		return fmt.Errorf("no credential stored for %q", provider)
	}
	var remaining []string
	err := updateProviders(func(p map[string]json.RawMessage) { delete(p, provider) })
	if err != nil {
		return err
	}
	if ActiveProvider() == provider {
		remaining = ListAuthorizedProviders()
		if len(remaining) > 0 {
			return AuthStore().Set("active", remaining[0])
		}
		return AuthStore().Set("active", nil)
	}
	return nil
}

// ─── refresh coordination ────────────────────────────────────────────────

const (
	authLockStale = 60 * time.Second
	authLockWait  = 30 * time.Second
)

// withAuthLock runs fn holding ~/.loop/auth.json.lock — the same directory
// lock loop takes (auth/refresh-lock.ts), so pi and loop never refresh the
// same credential at once. A lock that cannot be had in time is skipped: a
// stuck lock must not become an outage.
func withAuthLock(ctx context.Context, fn func() error) error {
	path := home.Path("auth.json.lock")
	_ = os.MkdirAll(home.Dir(), 0o700)
	acquire := func() bool {
		if os.Mkdir(path, 0o700) == nil {
			return true
		}
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) > authLockStale {
			_ = os.RemoveAll(path)
			return os.Mkdir(path, 0o700) == nil
		}
		return false
	}
	held := acquire()
	deadline := time.Now().Add(authLockWait)
	for !held && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		held = acquire()
	}
	if held {
		defer os.RemoveAll(path)
	}
	return fn()
}

// flight collapses concurrent refreshes of one credential in this process.
type flight struct {
	mu      sync.Mutex
	running map[string]*flightCall
}

type flightCall struct {
	done  chan struct{}
	value string
	err   error
}

var refreshes = &flight{running: map[string]*flightCall{}}

func (f *flight) do(key string, fn func() (string, error)) (string, error) {
	f.mu.Lock()
	if c, ok := f.running[key]; ok {
		f.mu.Unlock()
		<-c.done
		return c.value, c.err
	}
	c := &flightCall{done: make(chan struct{})}
	f.running[key] = c
	f.mu.Unlock()
	c.value, c.err = fn()
	close(c.done)
	f.mu.Lock()
	delete(f.running, key)
	f.mu.Unlock()
	return c.value, c.err
}

// ─── xAI subscription ────────────────────────────────────────────────────

// xaiStored reads the xAI OAuth record, raw (every field loop keeps —
// idToken, discovery, baseUrl — survives a refresh) and typed.
func xaiStored() (map[string]json.RawMessage, XaiCreds, bool) {
	e, ok := readEntry("xai")
	if !ok || e.Mode != "oauth" || len(e.Xai) == 0 {
		return nil, XaiCreds{}, false
	}
	raw := map[string]json.RawMessage{}
	var creds XaiCreds
	if json.Unmarshal(e.Xai, &raw) != nil || json.Unmarshal(e.Xai, &creds) != nil || creds.Access == "" {
		return nil, XaiCreds{}, false
	}
	return raw, creds, true
}

func saveXaiCreds(creds XaiCreds) error {
	merged := jsonstore.NewObject()
	if e, ok := readEntry("xai"); ok && len(e.Xai) > 0 {
		if o, err := jsonstore.ParseObject(e.Xai); err == nil {
			merged = o
		}
	}
	_ = merged.Set("access", creds.Access)
	_ = merged.Set("refresh", creds.Refresh)
	_ = merged.Set("expires", creds.Expires)
	_ = merged.Set("tokenEndpoint", creds.TokenEndpoint)
	if creds.IDToken != "" {
		_ = merged.Set("idToken", creds.IDToken)
	}
	if creds.TokenType != "" {
		_ = merged.Set("tokenType", creds.TokenType)
	}
	_ = merged.Set("baseUrl", xaiBaseURL)
	xai, _ := merged.MarshalJSON()
	entry := jsonstore.NewObject()
	_ = entry.Set("mode", "oauth")
	_ = entry.Set("provider", "xai")
	entry.SetRaw("xai", xai)
	raw, _ := entry.MarshalJSON()
	if err := updateProviders(func(p map[string]json.RawMessage) { p["xai"] = raw }); err != nil {
		return err
	}
	ensureActive("xai")
	return nil
}

// XaiAccessToken returns a usable bearer token, refreshing it when expired.
func XaiAccessToken(ctx context.Context) (string, error) {
	return xaiToken(ctx, "")
}

// XaiAccessTokenAfter401 is the retry after a request had `rejected`
// refused: it refreshes only if the stored token is still that one.
func XaiAccessTokenAfter401(ctx context.Context, rejected string) (string, error) {
	return xaiToken(ctx, rejected)
}

func xaiToken(ctx context.Context, rejected string) (string, error) {
	_, creds, ok := xaiStored()
	if !ok {
		return "", fmt.Errorf("not signed in to xAI")
	}
	if rejected == "" && time.Now().UnixMilli() < creds.Expires {
		return creds.Access, nil
	}
	return refreshes.do("xai:"+rejected, func() (string, error) {
		var token string
		err := withAuthLock(ctx, func() error {
			// Re-read under the lock: whoever held it may have renewed the
			// token, and refreshing from a stale copy spends a refresh token
			// that was already used.
			AuthStore().Refresh()
			_, current, ok := xaiStored()
			if !ok {
				return fmt.Errorf("not signed in to xAI")
			}
			if time.Now().UnixMilli() < current.Expires && current.Access != rejected {
				token = current.Access
				return nil
			}
			fresh, err := xaiRefresh(ctx, current)
			if err != nil {
				return err
			}
			if err := saveXaiCreds(fresh); err != nil {
				return err
			}
			token = fresh.Access
			return nil
		})
		return token, err
	})
}

// XaiSignedIn reports whether a subscription login is stored.
func XaiSignedIn() bool {
	_, _, ok := xaiStored()
	return ok
}

// LogoutXai removes the subscription credentials.
func LogoutXai() error {
	if !XaiSignedIn() {
		return errors.New("not signed in to xAI")
	}
	return Logout("xai")
}

// ─── Telegram gateway (auth.json, loop's telegram/store.ts) ──────────────

// Telegram is the stored gateway configuration.
type Telegram struct {
	Token       string
	ChatID      int64
	Enabled     bool
	BotUsername string
}

// LoadTelegram reads the gateway keys from auth.json.
func LoadTelegram() Telegram {
	s := AuthStore()
	var t Telegram
	s.Get("telegramToken", &t.Token)
	s.Get("telegramChatId", &t.ChatID)
	s.Get("telegramEnabled", &t.Enabled)
	s.Get("telegramBotUsername", &t.BotUsername)
	return t
}

// SetTelegramToken stores the bot token.
func SetTelegramToken(token string) error { return AuthStore().Set("telegramToken", token) }

// SetTelegramChatID stores the one chat allowed to drive the agent.
func SetTelegramChatID(id int64) error { return AuthStore().Set("telegramChatId", id) }
