package kimi

import (
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

func TestSubscriptionKey(t *testing.T) {
	if !IsSubscriptionKey("sk-kimi-abc") {
		t.Fatal("sk-kimi- should be a subscription key")
	}
	if IsSubscriptionKey("sk-0123456789abcdef") {
		t.Fatal("plain sk- should be a platform key")
	}
}

func TestBaseURLFollowsKeyKind(t *testing.T) {
	t.Setenv("KIMI_BASE_URL", "")
	t.Setenv("LOOP_KIMI_BASE_URL", "")

	if got := BaseURL("sk-platform"); got != platformBaseURL {
		t.Errorf("platform = %s", got)
	}
	if got := BaseURL("sk-kimi-sub"); got != codingBaseURL {
		t.Errorf("subscription = %s", got)
	}
}

func TestBaseURLEnvOverride(t *testing.T) {
	t.Setenv("KIMI_BASE_URL", "https://api.moonshot.cn/v1")
	if got := BaseURL("sk-kimi-sub"); got != "https://api.moonshot.cn/v1" {
		t.Errorf("KIMI_BASE_URL should win, got %s", got)
	}
}

func TestDefaultModelFollowsKeyKind(t *testing.T) {
	if got := DefaultModel("sk-platform"); got != K3 {
		t.Errorf("platform default = %s", got)
	}
	if got := DefaultModel("sk-kimi-sub"); got != CodeK3 {
		t.Errorf("subscription default = %s", got)
	}
}

func TestApplyThinkingMapsEffortToToggle(t *testing.T) {
	got := applyThinking(provider.CallOptions{Reasoning: provider.ReasoningHigh})
	if got.Reasoning != "" {
		t.Fatalf("reasoning_effort must not be forwarded, got %q", got.Reasoning)
	}
	thinking, _ := got.ProviderOptions.Get(providerID)["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking = %v", thinking)
	}

	off := applyThinking(provider.CallOptions{Reasoning: provider.ReasoningNone})
	thinking, _ = off.ProviderOptions.Get(providerID)["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("none → disabled, got %v", thinking)
	}
}

func TestApplyThinkingLeavesDefaultAlone(t *testing.T) {
	got := applyThinking(provider.CallOptions{Reasoning: provider.ReasoningDefault})
	if got.ProviderOptions.Get(providerID) != nil {
		t.Fatal("default effort should not inject a thinking toggle")
	}
}

func TestApplyThinkingDoesNotOverrideCaller(t *testing.T) {
	opts := provider.CallOptions{
		Reasoning: provider.ReasoningHigh,
		ProviderOptions: provider.ProviderOptions{
			providerID: {"thinking": map[string]any{"type": "disabled"}},
		},
	}
	got := applyThinking(opts)
	thinking, _ := got.ProviderOptions.Get(providerID)["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("caller toggle was overwritten: %v", thinking)
	}
}
