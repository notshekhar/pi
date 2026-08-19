package google

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// convertedPrompt is a spec prompt rendered into Google's wire shape.
type convertedPrompt struct {
	Contents          []content
	SystemInstruction *content
	Warnings          []provider.Warning
}

// convertPrompt renders a spec prompt into Google contents.
//
// Three structural differences from the other providers drive this code:
//
//   - the assistant role is called "model";
//   - there is no tool role, so tool results go back as user turns holding
//     functionResponse parts;
//   - a functionResponse payload must be a JSON object, so scalar results are
//     wrapped rather than sent bare.
func convertPrompt(prompt provider.Prompt) (*convertedPrompt, error) {
	out := &convertedPrompt{}
	var systemParts []part

	for _, msg := range prompt {
		switch m := msg.(type) {
		case provider.SystemMessage:
			if len(out.Contents) > 0 {
				return nil, &provider.InvalidPromptError{
					Message: "system messages must come before any user or assistant message",
				}
			}
			systemParts = append(systemParts, part{Text: m.Content})

		case provider.UserMessage:
			parts, err := userParts(m)
			if err != nil {
				return nil, err
			}
			out.append("user", parts)

		case provider.AssistantMessage:
			parts, warnings, err := assistantParts(m)
			if err != nil {
				return nil, err
			}
			out.Warnings = append(out.Warnings, warnings...)
			out.append("model", parts)

		case provider.ToolMessage:
			parts, warnings, err := toolParts(m)
			if err != nil {
				return nil, err
			}
			out.Warnings = append(out.Warnings, warnings...)
			// Tool results are user turns here, and merge with an adjacent
			// user turn so the conversation gains no empty entries.
			out.append("user", parts)

		default:
			return nil, &provider.InvalidPromptError{
				Message: fmt.Sprintf("unsupported message type %T", msg),
			}
		}
	}

	if len(systemParts) > 0 {
		out.SystemInstruction = &content{Parts: systemParts}
	}
	return out, nil
}

// append adds parts to the contents, merging into the previous turn when the
// roles match.
func (c *convertedPrompt) append(role string, parts []part) {
	if len(parts) == 0 {
		return
	}
	if n := len(c.Contents); n > 0 && c.Contents[n-1].Role == role {
		c.Contents[n-1].Parts = append(c.Contents[n-1].Parts, parts...)
		return
	}
	c.Contents = append(c.Contents, content{Role: role, Parts: parts})
}

// userParts renders a user message.
func userParts(m provider.UserMessage) ([]part, error) {
	parts := make([]part, 0, len(m.Content))

	for _, p := range m.Content {
		switch v := p.(type) {
		case provider.TextPart:
			parts = append(parts, part{Text: v.Text})

		case provider.FilePart:
			converted, err := filePart(v)
			if err != nil {
				return nil, err
			}
			parts = append(parts, *converted)

		default:
			return nil, &provider.InvalidPromptError{
				Message: fmt.Sprintf("unsupported user content part %T", p),
			}
		}
	}
	return parts, nil
}

// filePart renders an attachment.
func filePart(p provider.FilePart) (*part, error) {
	switch d := p.Data.(type) {
	case provider.FileDataBytes:
		encoded := d.Base64
		if encoded == "" {
			encoded = base64.StdEncoding.EncodeToString(d.Data)
		}
		return &part{InlineData: &inlineData{
			MimeType: fullMediaType(p.MediaType),
			Data:     encoded,
		}}, nil

	case provider.FileDataURL:
		return &part{FileData: &fileData{
			MimeType: fullMediaType(p.MediaType),
			FileURI:  d.URL.String(),
		}}, nil

	case provider.FileDataText:
		return &part{Text: d.Text}, nil

	case provider.FileDataRef:
		uri, ok := d.Reference[providerID]
		if !ok {
			for _, v := range d.Reference {
				uri = v
				break
			}
		}
		return &part{FileData: &fileData{
			MimeType: fullMediaType(p.MediaType),
			FileURI:  uri,
		}}, nil

	default:
		return nil, &provider.InvalidPromptError{
			Message: fmt.Sprintf("unsupported file data %T", p.Data),
		}
	}
}

// assistantParts renders an assistant message.
func assistantParts(m provider.AssistantMessage) ([]part, []provider.Warning, error) {
	var (
		parts    []part
		warnings []provider.Warning
	)

	for _, p := range m.Content {
		switch v := p.(type) {
		case provider.TextPart:
			if v.Text == "" {
				continue
			}
			parts = append(parts, part{
				Text:             v.Text,
				ThoughtSignature: thoughtSignature(v.ProviderOptions),
			})

		case provider.ReasoningPart:
			// Reasoning is only replayable with its signature; without one
			// Google rejects the turn, so drop it rather than fail the call.
			sig := thoughtSignature(v.ProviderOptions)
			if sig == "" {
				warnings = append(warnings, provider.Unsupported(
					"reasoning without signature",
					"a reasoning part was dropped because it carries no google thoughtSignature",
				))
				continue
			}
			parts = append(parts, part{
				Text:             v.Text,
				Thought:          true,
				ThoughtSignature: sig,
			})

		case provider.ToolCallPart:
			args, err := argsObject(v.Input)
			if err != nil {
				return nil, nil, &provider.InvalidPromptError{
					Message: fmt.Sprintf("encoding input for tool call %s", v.ToolCallID),
					Cause:   err,
				}
			}
			parts = append(parts, part{
				FunctionCall: &functionCall{
					ID:   v.ToolCallID,
					Name: v.ToolName,
					Args: args,
				},
				ThoughtSignature: thoughtSignature(v.ProviderOptions),
			})

		case provider.FilePart:
			converted, err := filePart(v)
			if err != nil {
				return nil, nil, err
			}
			parts = append(parts, *converted)

		default:
			warnings = append(warnings, provider.Unsupported(
				fmt.Sprintf("assistant content part %T", p),
				"the part was dropped from the request",
			))
		}
	}
	return parts, warnings, nil
}

// thoughtSignature reads the signature that authenticates replayed reasoning.
func thoughtSignature(opts provider.ProviderOptions) string {
	block := opts.Get(providerID)
	if block == nil {
		return ""
	}
	sig, _ := block["thoughtSignature"].(string)
	return sig
}

// argsObject coerces tool call input into the object Google requires.
func argsObject(input any) (map[string]any, error) {
	if input == nil {
		return map[string]any{}, nil
	}

	// The input may already be decoded, or still be the model's JSON string.
	if s, ok := input.(string); ok {
		if strings.TrimSpace(s) == "" {
			return map[string]any{}, nil
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(s), &decoded); err != nil {
			return nil, err
		}
		return decoded, nil
	}

	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	if decoded == nil {
		decoded = map[string]any{}
	}
	return decoded, nil
}

// toolParts renders tool results as functionResponse parts.
func toolParts(m provider.ToolMessage) ([]part, []provider.Warning, error) {
	var (
		parts    []part
		warnings []provider.Warning
	)

	for _, p := range m.Content {
		switch v := p.(type) {
		case provider.ToolResultPart:
			response, w := toolResponse(v.Output)
			warnings = append(warnings, w...)
			parts = append(parts, part{FunctionResponse: &functionResponse{
				ID:       v.ToolCallID,
				Name:     v.ToolName,
				Response: response,
			}})

		case provider.ToolApprovalResponsePart:
			warnings = append(warnings, provider.Unsupported(
				"tool approval response",
				"the generateContent API has no approval flow, so the response was dropped",
			))

		default:
			return nil, nil, &provider.InvalidPromptError{
				Message: fmt.Sprintf("unsupported tool content part %T", p),
			}
		}
	}
	return parts, warnings, nil
}

// toolResponse renders a tool result as the object Google requires.
//
// A functionResponse payload must be an object, so text and other scalars are
// wrapped under a key rather than sent bare.
func toolResponse(output provider.ToolResultOutput) (map[string]any, []provider.Warning) {
	switch o := output.(type) {
	case provider.ToolOutputText:
		return map[string]any{"result": o.Value}, nil

	case provider.ToolOutputErrorText:
		return map[string]any{"error": o.Value}, nil

	case provider.ToolOutputJSON:
		if obj, ok := o.Value.(map[string]any); ok {
			return obj, nil
		}
		return map[string]any{"result": o.Value}, nil

	case provider.ToolOutputErrorJSON:
		return map[string]any{"error": o.Value}, nil

	case provider.ToolOutputExecutionDenied:
		reason := o.Reason
		if reason == "" {
			reason = "The user denied execution of this tool."
		}
		return map[string]any{"error": reason}, nil

	case provider.ToolOutputContent:
		var (
			text     strings.Builder
			warnings []provider.Warning
		)
		for _, p := range o.Value {
			switch v := p.(type) {
			case provider.ToolContentText:
				text.WriteString(v.Text)
			case provider.ToolContentFile:
				warnings = append(warnings, provider.Unsupported(
					"file in tool result",
					"a functionResponse payload carries only JSON, so the file was dropped",
				))
				fmt.Fprintf(&text, "\n[file omitted: %s]", v.MediaType)
			}
		}
		return map[string]any{"result": text.String()}, warnings

	default:
		return map[string]any{"result": ""}, []provider.Warning{
			provider.Unsupported(fmt.Sprintf("tool output %T", output), ""),
		}
	}
}

// mapFinishReason converts Google's finishReason to the unified reason.
//
// Google reports STOP even when the model asked for tools, so the presence of
// tool calls has to override it or the agent loop would stop early.
func mapFinishReason(raw string, hasToolCalls bool) provider.FinishReason {
	unified := provider.FinishOther

	switch raw {
	case "STOP":
		if hasToolCalls {
			unified = provider.FinishToolCalls
		} else {
			unified = provider.FinishStop
		}
	case "MAX_TOKENS":
		unified = provider.FinishLength
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY":
		unified = provider.FinishContentFilter
	case "MALFORMED_FUNCTION_CALL":
		unified = provider.FinishError
	}

	return provider.FinishReason{Unified: unified, Raw: raw}
}

// convertUsage maps Google usage onto the spec's breakdown.
//
// candidatesTokenCount excludes thinking tokens, so the output total is the
// sum of the two. promptTokenCount already includes cached tokens.
func convertUsage(u *usageMetadata) provider.Usage {
	if u == nil {
		return provider.Usage{}
	}

	promptTotal := u.PromptTokenCount
	cacheRead := u.CachedContentTokenCount
	noCache := promptTotal - cacheRead

	text := u.CandidatesTokenCount
	reasoning := u.ThoughtsTokenCount
	outputTotal := text + reasoning

	usage := provider.Usage{
		InputTokens: provider.InputTokens{
			Total:     &promptTotal,
			NoCache:   &noCache,
			CacheRead: &cacheRead,
		},
		OutputTokens: provider.OutputTokens{
			Total:     &outputTotal,
			Text:      &text,
			Reasoning: &reasoning,
		},
	}

	if raw, err := json.Marshal(u); err == nil {
		var obj provider.JSONObject
		if json.Unmarshal(raw, &obj) == nil {
			usage.Raw = obj
		}
	}

	return usage
}

// fullMediaType expands a bare top-level type, since Google requires a
// complete type/subtype pair.
func fullMediaType(mediaType string) string {
	if strings.Contains(mediaType, "/") && !strings.HasSuffix(mediaType, "/*") {
		return mediaType
	}
	top, _, _ := strings.Cut(strings.ToLower(mediaType), "/")
	switch top {
	case "image":
		return "image/png"
	case "text":
		return "text/plain"
	case "audio":
		return "audio/mpeg"
	case "video":
		return "video/mp4"
	default:
		return "application/pdf"
	}
}
