package openaicompat

import (
	"regexp"

	"github.com/notshekhar/pi/internal/ai/provider"
)

// @ai-sdk/deepseek's request shape (DeepSeek, and Kimi through the same
// SDK in loop), layered over the chat completions body.

var deepseekLegacy = regexp.MustCompile(`(^|[/.])deepseek-(?:chat|reasoner|v[1-3](?:\.\d+)?)([-/:.]|$)`)

// deepseekV4 is the SDK's isDeepSeekV4Model.
func deepseekV4(model string) bool { return !deepseekLegacy.MatchString(model) }

var deepseekEffort = map[provider.ReasoningEffort]string{
	provider.ReasoningMinimal: "low", provider.ReasoningLow: "low", provider.ReasoningMedium: "high",
	provider.ReasoningHigh: "high", provider.ReasoningXHigh: "max",
}

func (m *languageModel) deepseekShape(req *chatRequest, opts provider.CallOptions) []provider.Warning {
	var warnings []provider.Warning
	// The thinking toggle: providerOptions win (loop sets it for Kimi), else
	// it follows the effort.
	var thinking map[string]any
	if t, ok := req.Extra["thinking"].(map[string]any); ok {
		thinking = t
		if thinking["type"] == "adaptive" {
			thinking = map[string]any{"type": "enabled"}
		}
	} else if e := opts.Reasoning; e != "" && e != provider.ReasoningDefault {
		kind := "enabled"
		if e == provider.ReasoningNone {
			kind = "disabled"
		}
		thinking = map[string]any{"type": kind}
	}
	if thinking != nil {
		extra := provider.JSONObject{}
		for k, v := range req.Extra {
			extra[k] = v
		}
		extra["thinking"] = thinking
		req.Extra = extra
	}
	disabled := thinking != nil && thinking["type"] == "disabled"
	thinkingOn := !disabled && (thinking != nil || m.modelID == "deepseek-reasoner" || deepseekV4(m.modelID))

	req.ReasoningEffort = ""
	if e := opts.Reasoning; !disabled && e != "" && e != provider.ReasoningNone && e != provider.ReasoningDefault {
		req.ReasoningEffort = deepseekEffort[e]
	}
	if thinkingOn && (req.Temperature != nil || req.TopP != nil) {
		warnings = append(warnings, provider.Unsupported("temperature/topP", "no effect while DeepSeek thinking is enabled"))
		req.Temperature, req.TopP = nil, nil
	}
	return warnings
}

// GroqEffort is @ai-sdk/groq's effort map; "none" only where a model takes it.
func GroqEffort(model string, e provider.ReasoningEffort) (string, bool) {
	switch e {
	case provider.ReasoningNone:
		return "none", model == "qwen/qwen3.6-27b"
	case provider.ReasoningMinimal, provider.ReasoningLow:
		return "low", true
	case provider.ReasoningMedium:
		return "medium", true
	case provider.ReasoningHigh, provider.ReasoningXHigh:
		return "high", true
	}
	return "", false
}
