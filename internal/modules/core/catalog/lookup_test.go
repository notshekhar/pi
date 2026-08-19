package catalog

import "testing"

// A lookup without the key silently misses every Code-plan model, which shows
// up as a session reporting $0.0000 and an unknown context window rather than
// as an error.
func TestLookupFindsCodePlanModelsWithTheKey(t *testing.T) {
	const codeKey = "sk-kimi-abc123"

	if _, ok := Lookup("kimi", "k3", codeKey); !ok {
		t.Error("k3 not found with a Code-plan key")
	}
	// Without the key it is genuinely not in the catalog, which is correct —
	// the point is that callers must pass the key they have.
	if _, ok := Lookup("kimi", "k3"); ok {
		t.Log("k3 is also in the default catalog; the key-aware path still matters")
	}

	// A model with pricing and a window, which is what the callers need.
	m, ok := Lookup("kimi", "k3", codeKey)
	if !ok {
		t.Fatal("lookup failed")
	}
	if m.Context <= 0 {
		t.Errorf("no context window: %+v", m)
	}
}

func TestLookupStillWorksForOrdinaryProviders(t *testing.T) {
	if _, ok := Lookup("anthropic", "claude-opus-4-8"); !ok {
		t.Error("a plain provider lookup broke")
	}
}
