package agent

import (
	"encoding/json"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
)

// loop's buildReasoningParams, case by case.
func TestReasoningParams(t *testing.T) {
	for _, c := range []struct {
		prov, model, level string
		known              bool
		effort             provider.ReasoningEffort
		opts               string
	}{
		{"anthropic", "claude-opus-5", "high", true, "", `{"anthropic":{"effort":"high"}}`},
		{"anthropic", "claude-opus-4-6", "xhigh", true, "", `{"anthropic":{"effort":"max","thinking":{"display":"summarized","type":"adaptive"}}}`},
		{"anthropic", "claude-opus-4-8", "off", true, "", `{"anthropic":{"thinking":{"type":"disabled"}}}`},
		{"anthropic", "claude-fable-5", "off", true, "", `null`},
		{"anthropic", "claude-sonnet-4-5", "medium", true, "medium", `null`},
		{"xai", "grok-4.7", "high", true, "", `null`},
		{"xai", "grok-3-mini", "high", true, "high", `null`},
		{"openai", "gpt-6.1-sol", "medium", true, "medium", `{"openai":{"reasoningSummary":"auto"}}`},
		{"openai", "azure/gpt-6.1-sol", "low", true, "low", `{"openai":{"forceReasoning":true,"reasoningSummary":"auto"}}`},
		{"openai", "gpt-6.1-sol", "off", true, "none", `null`},
		{"openrouter", "anthropic/claude-sonnet-4.5", "xhigh", true, "", `{"openrouter":{"reasoning":{"effort":"high"}}}`},
		{"kimi", "kimi-k3", "medium", true, "", `{"kimi":{"thinking":{"type":"enabled"}}}`},
		{"glm", "glm-5", "off", true, "", `{"glm":{"thinking":{"type":"disabled"}}}`},
		{"ollama", "qwen3", "low", true, "", `{"ollama":{"think":true}}`},
		{"deepseek", "deepseek-v4-flash", "high", true, "high", `null`},
	} {
		effort, opts := reasoningParams(c.prov, c.prov, c.model, c.level, true, c.known)
		raw, _ := json.Marshal(opts)
		if effort != c.effort || string(raw) != c.opts {
			t.Errorf("%s/%s %s: effort %q opts %s, want %q %s", c.prov, c.model, c.level, effort, raw, c.effort, c.opts)
		}
	}
	if e, o := reasoningParams("openai", "openai", "gpt-4.1", "high", false, false); e != "" || o != nil {
		t.Error("a model the catalog says cannot reason got reasoning")
	}
}
