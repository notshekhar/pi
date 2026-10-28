package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	_, got, ok := xaiStored()
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

// A custom endpoint's credential, by loop's auth kinds.
func TestCustomKeyByAuthKind(t *testing.T) {
	t.Setenv("PI_TEST_GW_KEY", "from-env")
	cases := []struct {
		p    CustomProvider
		want string
	}{
		{CustomProvider{APIKey: "legacy-flat"}, "legacy-flat"},
		{CustomProvider{Auth: &CustomAuth{Kind: "apikey", APIKey: "typed-in"}}, "typed-in"},
		{CustomProvider{Auth: &CustomAuth{Kind: "bearer", Token: "tok"}}, "tok"},
		{CustomProvider{Auth: &CustomAuth{Kind: "env", Var: "PI_TEST_GW_KEY"}}, "from-env"},
		{CustomProvider{Auth: &CustomAuth{Kind: "helper", Command: "printf from-cmd"}}, "from-cmd"},
		// A helper that fails yields nothing rather than its error text.
		{CustomProvider{Auth: &CustomAuth{Kind: "helper", Command: "exit 3"}}, ""},
		{CustomProvider{Auth: &CustomAuth{Kind: "none"}}, ""},
	}
	for _, c := range cases {
		if got := c.p.CustomKey(); got != c.want {
			t.Errorf("%+v → %q, want %q", c.p.Auth, got, c.want)
		}
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
		Models:  []CustomModel{{ID: "gw-model-1"}, {ID: "gw-model-2"}},
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(Config{Provider: "custom:mygw", CWD: t.TempDir()})
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
	if cfg.Provider != "custom:mygw" || cfg.ModelID != "gw-model-2" {
		t.Errorf("resolved to %s/%s", cfg.Provider, cfg.ModelID)
	}
}

// A token another process already rotated is used as-is: refreshing from a
// stale copy would replay a spent refresh token and revoke the login.
func TestXaiTokenReusesExternalRotation(t *testing.T) {
	withHome(t)
	if err := saveXaiCreds(XaiCreds{Access: "old", Refresh: "r0", Expires: time.Now().Add(time.Hour).UnixMilli(),
		TokenEndpoint: "https://invalid.example/token"}); err != nil {
		t.Fatal(err)
	}
	// Prime the cache, then rotate on disk behind its back.
	if tok, _ := XaiAccessToken(context.Background()); tok != "old" {
		t.Fatalf("token = %q", tok)
	}
	path := filepath.Join(os.Getenv("HOME"), ".loop", "auth.json")
	data, _ := os.ReadFile(path)
	data = []byte(strings.Replace(string(data), `"old"`, `"rotated"`, 1))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := XaiAccessTokenAfter401(context.Background(), "old")
	if err != nil || tok != "rotated" {
		t.Fatalf("after 401: %q, %v (should reuse the rotated token without refreshing)", tok, err)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("lock left behind")
	}
}

// Writes keep loop's format: tab indentation, no trailing newline, and every
// field loop stores on the login.
func TestXaiSaveKeepsLoopFields(t *testing.T) {
	withHome(t)
	path := filepath.Join(os.Getenv("HOME"), ".loop", "auth.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(`{"providers":{"xai":{"mode":"oauth","provider":"xai","xai":{"access":"a","refresh":"r","expires":1,"tokenEndpoint":"https://auth.x.ai/t","discovery":{"token_endpoint":"x"},"idToken":"id"}}},"active":"xai"}`), 0o600)
	if err := saveXaiCreds(XaiCreds{Access: "b", Refresh: "r2", Expires: 2, TokenEndpoint: "https://auth.x.ai/t"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	for _, want := range []string{`"discovery"`, `"idToken": "id"`, `"access": "b"`, "\n\t\"providers\""} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.HasSuffix(s, "\n") {
		t.Error("trailing newline: loop's configstore writes none")
	}
}
