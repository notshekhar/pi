package catalog

import "testing"

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
	// Bedrock ids contain colons and must stay intact on the current provider.
	prov, model = Parse("us.anthropic.claude-sonnet-4-5-20250929-v1:0", "bedrock")
	if prov != "bedrock" || model != "us.anthropic.claude-sonnet-4-5-20250929-v1:0" {
		t.Fatalf("got %s / %s", prov, model)
	}
}

func TestKimiSubscriptionSwapsCatalog(t *testing.T) {
	platform := Models("kimi", "sk-platform")
	if len(platform) == 0 || platform[0].ShortID != "kimi-k3" {
		t.Fatalf("platform = %+v", platform)
	}
	code := Models("kimi", "sk-kimi-sub")
	if len(code) != 3 || code[0].ShortID != "k3" {
		t.Fatalf("code = %+v", code)
	}
}

func TestLookupShortAndFull(t *testing.T) {
	m, ok := Lookup("anthropic", "claude-sonnet-4-5")
	if !ok || m.Name != "Claude Sonnet 4.5" {
		t.Fatalf("lookup short: %+v ok=%v", m, ok)
	}
	m, ok = Lookup("kimi", "kimi-k2.7-code")
	if !ok || m.ID != "kimi/kimi-k2.7-code" {
		t.Fatalf("lookup kimi: %+v ok=%v", m, ok)
	}
}

func TestEveryProviderHasModels(t *testing.T) {
	for _, p := range Providers {
		if ms := Models(p.ID); len(ms) == 0 {
			t.Errorf("%s has no models", p.ID)
		}
	}
}
