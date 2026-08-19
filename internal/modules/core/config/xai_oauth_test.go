package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func authHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	return dir
}

// Discovery is a document fetched over the network, and the token endpoint it
// names is where the refresh token gets sent. Following one anywhere is how a
// compromised resolver walks off with an account.
func TestDiscoveryEndpointsAreConfinedToXai(t *testing.T) {
	bad := []string{
		"http://auth.x.ai/token",      // not https
		"https://evil.example/token",  // not x.ai
		"https://notx.ai/token",       // not a subdomain
		"https://x.ai.evil.com/token", // suffix trickery
		"",                            // absent
		"not a url",
	}
	for _, raw := range bad {
		if err := validateXaiEndpoint(raw, "token_endpoint"); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"https://auth.x.ai/token", "https://accounts.x.ai/oauth2/token", "https://x.ai/t"} {
		if err := validateXaiEndpoint(raw, "token_endpoint"); err != nil {
			t.Errorf("rejected a legitimate endpoint %q: %v", raw, err)
		}
	}
}

// The credentials go into the file loop shares, in loop's own entry shape, so
// signing in here signs you in there.
func TestCredentialsRoundTripThroughTheSharedFile(t *testing.T) {
	authHome(t)
	creds := XaiCreds{
		Access: "at", Refresh: "rt",
		Expires:       time.Now().Add(time.Hour).UnixMilli(),
		TokenEndpoint: "https://auth.x.ai/token",
	}
	if err := saveXaiCreds(creds); err != nil {
		t.Fatal(err)
	}
	if !XaiSignedIn() {
		t.Fatal("a stored sign-in was not found")
	}
	got, ok := loadXaiCreds()
	if !ok || got.Access != "at" || got.Refresh != "rt" {
		t.Fatalf("credentials did not round-trip: %+v", got)
	}

	raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".loop", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Providers map[string]struct {
			Mode     string `json:"mode"`
			Provider string `json:"provider"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Providers["xai"].Mode != "oauth" || doc.Providers["xai"].Provider != "xai" {
		t.Errorf("not loop's entry shape: %+v", doc.Providers["xai"])
	}
}

// A key entered here has to be readable again. It was not: SaveKey wrote a
// bare string while every reader — on both sides of the shared file —
// requires `{mode, provider, apiKey}`, so the key saved and the provider
// stayed signed out.
func TestSavedKeyIsReadableAgain(t *testing.T) {
	authHome(t)
	if err := SaveKey("xai", "sk-test-123"); err != nil {
		t.Fatal(err)
	}
	if got := APIKey("xai"); got != "sk-test-123" {
		t.Fatalf("APIKey = %q, want the key that was just saved", got)
	}
	if !Authorized("xai") {
		t.Error("a provider with a stored key is not authorized")
	}
}

// Writing one provider must not disturb another — the file is shared with
// loop, and clobbering its entries would sign the user out of it.
func TestWritingOneProviderLeavesTheOthersAlone(t *testing.T) {
	authHome(t)
	if err := SaveKey("anthropic", "sk-ant"); err != nil {
		t.Fatal(err)
	}
	if err := saveXaiCreds(XaiCreds{Access: "at", Refresh: "rt", Expires: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := SaveKey("openai", "sk-oai"); err != nil {
		t.Fatal(err)
	}
	if APIKey("anthropic") != "sk-ant" {
		t.Error("an unrelated API key was lost")
	}
	if !XaiSignedIn() {
		t.Error("the OAuth entry was lost by a later key write")
	}
}

// Signing out has to actually sign out, whichever way the user signed in.
func TestLogoutRemovesASubscription(t *testing.T) {
	authHome(t)
	if err := saveXaiCreds(XaiCreds{Access: "at", Refresh: "rt", Expires: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := Logout("xai"); err != nil {
		t.Fatal(err)
	}
	if XaiSignedIn() {
		t.Error("a live subscription token was left on disk after logout")
	}
}

// A custom endpoint's credential, resolved most-specific first.
func TestCustomKeyResolutionOrder(t *testing.T) {
	t.Setenv("PI_TEST_GW_KEY", "from-env")

	both := CustomProvider{APIKey: "typed-in", EnvVar: "PI_TEST_GW_KEY", KeyCommand: "printf from-cmd"}
	if got := both.CustomKey(); got != "typed-in" {
		t.Errorf("a typed key lost to something else: %q", got)
	}
	if got := (CustomProvider{EnvVar: "PI_TEST_GW_KEY", KeyCommand: "printf from-cmd"}).CustomKey(); got != "from-env" {
		t.Errorf("env var = %q", got)
	}
	if got := (CustomProvider{KeyCommand: "printf from-cmd"}).CustomKey(); got != "from-cmd" {
		t.Errorf("key command = %q", got)
	}
	// An env var that is set but EMPTY falls through, or a stale export
	// silently disables a working helper.
	t.Setenv("PI_TEST_GW_KEY", "")
	if got := (CustomProvider{EnvVar: "PI_TEST_GW_KEY", KeyCommand: "printf from-cmd"}).CustomKey(); got != "from-cmd" {
		t.Errorf("an empty env var did not fall through: %q", got)
	}
	// A helper that fails yields nothing rather than its error text, which
	// would otherwise be sent to the endpoint as a credential.
	if got := (CustomProvider{KeyCommand: "exit 3"}).CustomKey(); got != "" {
		t.Errorf("a failed helper produced a credential: %q", got)
	}
}

// A custom provider has to actually build a model. It did not: the branch
// returned "unknown provider", so every custom endpoint was configurable,
// listed in the picker, and dead on the first turn.
func TestCustomProviderResolvesToAModel(t *testing.T) {
	authHome(t)
	if err := AddCustomProvider("mygw", CustomProvider{
		BaseURL: "http://127.0.0.1:9/v1",
		APIKey:  "k",
		Models:  []string{"gw-model-1", "gw-model-2"},
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(Config{Provider: "mygw", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("a custom provider did not resolve: %v", err)
	}
	// The first model listed is its default — which is why the wizard asks
	// for them in preference order.
	if cfg.ModelID != "gw-model-1" {
		t.Errorf("default model = %q, want the first listed", cfg.ModelID)
	}
	if _, err := LanguageModel(cfg); err != nil {
		t.Fatalf("a custom provider did not build a model: %v", err)
	}

	// And `provider/model` has to split on a custom name too, or
	// `/model mygw/gw-model-2` resolves against the built-in list and fails.
	cfg, err = Resolve(Config{ModelID: "mygw/gw-model-2", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("a qualified custom id did not resolve: %v", err)
	}
	if cfg.Provider != "mygw" || cfg.ModelID != "gw-model-2" {
		t.Errorf("resolved to %s/%s", cfg.Provider, cfg.ModelID)
	}
}
