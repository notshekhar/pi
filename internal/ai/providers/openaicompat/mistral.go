package openaicompat

import (
	"github.com/notshekhar/pi/internal/ai/provider"
)

// @ai-sdk/mistral's request shape, layered over the chat completions body.

// mistralNoEffort is its modelsWithoutReasoningEffort.
var mistralNoEffort = map[string]bool{
	"codestral-2508": true, "codestral-latest": true, "ministral-14b-2512": true, "ministral-14b-latest": true,
	"ministral-3b-2512": true, "ministral-3b-latest": true, "ministral-8b-2512": true, "ministral-8b-latest": true,
	"mistral-code-fim-latest": true, "mistral-code-latest": true, "mistral-large-latest": true, "mistral-large-2512": true,
	"voxtral-small-2507": true, "voxtral-small-latest": true,
}

// mistralLowHighMax take only low, high and max, and cannot turn reasoning off.
var mistralLowHighMax = map[string]bool{"zai-glm-5": true, "zai-glm-5-3": true, "zai-glm-5-3-flash": true, "zai-glm-latest": true}

func mistralEffort(model string, effort provider.ReasoningEffort) (string, []provider.Warning) {
	if effort == "" || effort == provider.ReasoningDefault {
		return "", nil
	}
	if mistralNoEffort[model] {
		return "", []provider.Warning{provider.Unsupported("reasoning", "this model does not support reasoning configuration")}
	}
	lhm := mistralLowHighMax[model]
	if effort == provider.ReasoningNone {
		if lhm {
			return "low", []provider.Warning{provider.Unsupported("reasoning", `"none" is not supported by this model; mapped to "low"`)}
		}
		return "none", nil
	}
	if lhm {
		return map[provider.ReasoningEffort]string{
			provider.ReasoningMinimal: "low", provider.ReasoningLow: "low", provider.ReasoningMedium: "high",
			provider.ReasoningHigh: "high", provider.ReasoningXHigh: "max",
		}[effort], nil
	}
	return "high", nil
}

// mistralShape rewrites a built request into Mistral's dialect.
func (m *languageModel) mistralShape(req *chatRequest, opts provider.CallOptions) []provider.Warning {
	effort, warnings := mistralEffort(m.modelID, opts.Reasoning)
	req.ReasoningEffort = effort

	if req.Seed != nil {
		extra := provider.JSONObject{}
		for k, v := range req.Extra {
			extra[k] = v
		}
		if _, set := extra["random_seed"]; !set {
			extra["random_seed"] = *req.Seed
		}
		req.Extra, req.Seed = extra, nil
	}

	// Mistral has no "required" and no named choice: "any", with the tools
	// narrowed to the one named.
	if tc := opts.ToolChoice; tc != nil {
		switch tc.Type {
		case provider.ToolChoiceRequired:
			req.ToolChoice = "any"
		case provider.ToolChoiceTool:
			var kept []apiTool
			for _, t := range req.Tools {
				if t.Function.Name == tc.ToolName {
					kept = append(kept, t)
				}
			}
			req.Tools, req.ToolChoice = kept, "any"
		}
	}

	// Assistant turns that reasoned go back as content parts with their
	// thinking; the last message, when it is the assistant's, is a prefix.
	for i, msg := range opts.Prompt {
		switch v := msg.(type) {
		case provider.AssistantMessage:
			idx := m.messageIndex(req, opts.Prompt, i)
			if idx < 0 {
				continue
			}
			var parts []any
			reasoned := false
			for _, part := range v.Content {
				switch p := part.(type) {
				case provider.TextPart:
					parts = append(parts, map[string]any{"type": "text", "text": p.Text})
				case provider.ReasoningPart:
					reasoned = true
					parts = append(parts, map[string]any{"type": "thinking", "closed": true,
						"thinking": []any{map[string]any{"type": "text", "text": p.Text}}})
				}
			}
			if reasoned {
				req.Messages[idx].Content = parts
			}
			if i == len(opts.Prompt)-1 {
				req.Messages[idx].Prefix = true
			}
		case provider.ToolMessage:
			// Tool results carry their tool's name.
			for _, part := range v.Content {
				if p, ok := part.(provider.ToolResultPart); ok {
					for j := range req.Messages {
						if req.Messages[j].Role == "tool" && req.Messages[j].ToolCallID == p.ToolCallID {
							req.Messages[j].Name = p.ToolName
						}
					}
				}
			}
		}
	}
	return warnings
}

// messageIndex finds the wire message a prompt message became: prompt and
// wire stay aligned except that a tool message fans out into one wire
// message per result.
func (m *languageModel) messageIndex(req *chatRequest, prompt provider.Prompt, at int) int {
	idx := 0
	for i, msg := range prompt {
		if i == at {
			if idx < len(req.Messages) {
				return idx
			}
			return -1
		}
		if t, ok := msg.(provider.ToolMessage); ok {
			for _, part := range t.Content {
				if _, ok := part.(provider.ToolResultPart); ok {
					idx++
				}
			}
			continue
		}
		idx++
	}
	return -1
}
