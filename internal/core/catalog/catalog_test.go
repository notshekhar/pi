package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points HOME at a scratch dir so the real ~/.loop is never read.
func isolate(t *testing.T, kimiKey string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	Configure(Hooks{APIKey: func(p string) string {
		if p == "kimi" {
			return kimiKey
		}
		return ""
	}})
	t.Cleanup(func() { Configure(Hooks{}) })
	return dir
}

func TestParseKeepsGatewaySlashes(t *testing.T) {
	prov, model := Parse("openrouter/anthropic/claude-sonnet-4-6", "google")
	if prov != "openrouter" || model != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("got %s / %s", prov, model)
	}
}

func TestParseColonOnlyWhenProviderKnown(t *testing.T) {
	prov, model := Parse("kimi:k3", "google")
	if prov != "kimi" || model != "k3" {
		t.Fatalf("got %s / %s", prov, model)
	}
	prov, model = Parse("us.anthropic.claude-sonnet-4-5-20250929-v1:0", "bedrock")
	if prov != "bedrock" || model != "us.anthropic.claude-sonnet-4-5-20250929-v1:0" {
		t.Fatalf("got %s / %s", prov, model)
	}
}

// A Kimi Code subscription key swaps the platform models for the plan's.
func TestKimiSubscriptionSwapsCatalog(t *testing.T) {
	isolate(t, "sk-platform")
	if platform := Models("kimi"); len(platform) == 0 || platform[0].ShortID != "kimi-k3" {
		t.Fatalf("platform = %+v", platform)
	}
	isolate(t, "sk-kimi-sub")
	code := Models("kimi")
	if len(code) != len(fallbackData.KimiCode) || code[0].ShortID != "k3" {
		t.Fatalf("code = %+v", code)
	}
}

// Fallbacks lead their provider, so Default is the curated first choice.
func TestFallbacksLeadAndWinCollisions(t *testing.T) {
	isolate(t, "")
	first := fallbackData.Fallback[0]
	if d, ok := Default(first.Provider); !ok || d.ID != first.ID {
		t.Fatalf("default = %+v, want %s", d, first.ID)
	}
	if m, ok := Lookup("anthropic", "claude-sonnet-4-5"); !ok || m.Name == "" || m.Context == 0 {
		t.Fatalf("lookup short: %+v ok=%v", m, ok)
	}
}

// Providers loop seeds statically all have models; bedrock and ollama are
// discovered live in loop, so they have none until then.
func TestEveryStaticProviderHasModels(t *testing.T) {
	isolate(t, "")
	for _, p := range Providers {
		if p.ID == "bedrock" || p.ID == "ollama" {
			continue
		}
		if ms := Models(p.ID); len(ms) == 0 {
			t.Errorf("%s has no models", p.ID)
		}
	}
}

// ~/.loop/models.json patches replace whole fields ({...model, ...patch})
// and can add models; availability from catalog.json applies to defs.
func TestOverridesAndAvailabilityFromLoopFiles(t *testing.T) {
	home := isolate(t, "")
	os.MkdirAll(filepath.Join(home, ".loop"), 0o700)
	os.WriteFile(filepath.Join(home, ".loop", "models.json"), []byte(`{
  "anthropic/claude-sonnet-4-5": {"name": "Patched"},
  "openrouter/some/new-model:free": {"id": "openrouter/some/new-model:free", "provider": "openrouter", "name": "New", "contextWindow": 1000, "maxOutput": 10, "cost": {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}, "reasoning": true, "modalities": ["text"], "available": true}
}`), 0o644)
	os.WriteFile(filepath.Join(home, ".loop", "catalog.json"), []byte(`{"availability":{"openai":["openai/gpt-5"]},"ts":1,"models":{},"modelsTs":0,"bedrockModels":[]}`), 0o600)
	Bust()
	if m, _ := Lookup("anthropic", "claude-sonnet-4-5"); m.Name != "Patched" || m.Context == 0 {
		t.Errorf("patched = %+v", m)
	}
	if m, ok := Lookup("openrouter", "some/new-model:free"); !ok || m.Name != "New" || !m.Reasoning {
		t.Errorf("added = %+v %v", m, ok)
	}
	var avail, unavail int
	for _, m := range Models("openai") {
		if strings.HasPrefix(m.ID, "openai/") && m.Available {
			avail++
		} else {
			unavail++
		}
	}
	if unavail == 0 {
		t.Error("availability from catalog.json was not applied to generated defs")
	}
	// Reading must not have dropped keys pi does not produce.
	data, _ := os.ReadFile(filepath.Join(home, ".loop", "catalog.json"))
	if !strings.Contains(string(data), "bedrockModels") {
		t.Errorf("catalog.json lost a key: %s", data)
	}
}

// A custom gateway model inherits limits and prices from the vendor catalog
// when the user declared none, through the gateway's routing prefix.
func TestCustomProviderModelsResolveThroughVendorCatalog(t *testing.T) {
	isolate(t, "")
	Configure(Hooks{CustomProviders: func() []CustomProvider {
		return []CustomProvider{{Name: "gw", SDK: "anthropic", Models: []CustomModelDecl{{ID: "anthropic/claude-sonnet-4-5-20250929"}, {ID: "mystery-model"}}}}
	}})
	m, ok := Lookup("custom:gw", "anthropic/claude-sonnet-4-5-20250929")
	if !ok || m.Cost.Input == 0 || m.Context == 0 {
		t.Fatalf("vendor-resolved = %+v %v", m, ok)
	}
	floor, ok := Lookup("custom:gw", "mystery-model")
	if !ok || floor.Context != gatewayFloor.ContextWindow || floor.Cost.Input != 0 {
		t.Fatalf("floor = %+v", floor)
	}
	// An id the gateway never listed still resolves (modelInfoIn).
	if _, ok := Lookup("custom:gw", "renamed-later"); !ok {
		t.Fatal("unlisted custom model did not fall back")
	}
}
