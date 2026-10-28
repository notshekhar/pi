package openaicompat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/ai/provider"
)

// convertPrompt renders a spec prompt into OpenAI chat messages.
//
// The shape differs from Anthropic in three ways that drive this code:
//
//   - the system prompt is an ordinary message, not a separate field;
//   - tool results are their own "tool" role, one message per result, rather
//     than blocks inside a user message;
//   - tool calls hang off the assistant message as a sibling field instead of
//     being content blocks.
//
// optionsKey names the provider's block in a message's provider options; a
// cacheControl there marks a prompt-cache breakpoint (see markCache).
//
// replay is how reasoning is sent back (see reasoningReplay).
func convertPrompt(prompt provider.Prompt, optionsKey string, replay reasoningReplay) ([]apiMessage, []provider.Warning, error) {
	var (
		messages []apiMessage
		warnings []provider.Warning
	)

	for _, msg := range prompt {
		before := len(messages)
		switch m := msg.(type) {
		case provider.SystemMessage:
			messages = append(messages, apiMessage{Role: "system", Content: m.Content})

		case provider.UserMessage:
			converted, w, err := userMessage(m)
			if err != nil {
				return nil, nil, err
			}
			warnings = append(warnings, w...)
			messages = append(messages, converted)

		case provider.AssistantMessage:
			converted, w, err := assistantMessage(m, replay)
			if err != nil {
				return nil, nil, err
			}
			warnings = append(warnings, w...)
			messages = append(messages, converted)

		case provider.ToolMessage:
			converted, w, err := toolMessages(m)
			if err != nil {
				return nil, nil, err
			}
			warnings = append(warnings, w...)
			messages = append(messages, converted...)

		default:
			return nil, nil, &provider.InvalidPromptError{
				Message: fmt.Sprintf("unsupported message type %T", msg),
			}
		}
		if cc := cacheMark(messageOptions(msg).Get(optionsKey)); cc != nil && len(messages) > before {
			markCache(&messages[len(messages)-1], cc)
		}
	}

	return messages, warnings, nil
}

// cacheMark reads a breakpoint from a provider-options block: cacheControl
// (or cache_control) as an object, or true for an ephemeral one.
func cacheMark(block provider.JSONObject) *cacheControl {
	raw, ok := block["cacheControl"]
	if !ok {
		raw, ok = block["cache_control"]
	}
	switch v := raw.(type) {
	case map[string]any:
		cc := &cacheControl{Type: "ephemeral"}
		if t, ok := v["type"].(string); ok && t != "" {
			cc.Type = t
		}
		if ttl, ok := v["ttl"].(string); ok {
			cc.TTL = ttl
		}
		return cc
	case bool:
		if v {
			return &cacheControl{Type: "ephemeral"}
		}
	}
	return nil
}

// markCache puts a cache_control on a message's last content part — the
// shape gateways relaying Anthropic's caching take (OpenRouter's Claude
// models cache nothing without one). String content becomes one text part.
func markCache(m *apiMessage, cc *cacheControl) {
	switch c := m.Content.(type) {
	case string:
		if c != "" {
			m.Content = []contentPart{{Type: "text", Text: c, CacheControl: cc}}
		}
	case []contentPart:
		if len(c) > 0 {
			c[len(c)-1].CacheControl = cc
		}
	}
}

func messageOptions(m provider.Message) provider.ProviderOptions {
	switch v := m.(type) {
	case provider.SystemMessage:
		return v.ProviderOptions
	case provider.UserMessage:
		return v.ProviderOptions
	case provider.AssistantMessage:
		return v.ProviderOptions
	case provider.ToolMessage:
		return v.ProviderOptions
	}
	return nil
}

// userMessage renders a user message, using the plain string form when it is
// text-only since some gateways reject the array form.
func userMessage(m provider.UserMessage) (apiMessage, []provider.Warning, error) {
	var warnings []provider.Warning

	textOnly := true
	for _, part := range m.Content {
		if _, ok := part.(provider.TextPart); !ok {
			textOnly = false
			break
		}
	}

	if textOnly {
		var text strings.Builder
		for _, part := range m.Content {
			text.WriteString(part.(provider.TextPart).Text)
		}
		return apiMessage{Role: "user", Content: text.String()}, nil, nil
	}

	parts := make([]contentPart, 0, len(m.Content))
	for _, part := range m.Content {
		switch p := part.(type) {
		case provider.TextPart:
			parts = append(parts, contentPart{Type: "text", Text: p.Text})

		case provider.FilePart:
			converted, w, err := filePart(p)
			if err != nil {
				return apiMessage{}, nil, err
			}
			warnings = append(warnings, w...)
			if converted != nil {
				parts = append(parts, *converted)
			}

		default:
			return apiMessage{}, nil, &provider.InvalidPromptError{
				Message: fmt.Sprintf("unsupported user content part %T", part),
			}
		}
	}

	return apiMessage{Role: "user", Content: parts}, warnings, nil
}

// filePart renders an attachment as an image or file part.
func filePart(p provider.FilePart) (*contentPart, []provider.Warning, error) {
	isImage := topLevelMediaType(p.MediaType) == "image"

	switch d := p.Data.(type) {
	case provider.FileDataURL:
		if !isImage {
			// The chat API has no URL form for non-image files.
			return nil, []provider.Warning{provider.Unsupported(
				"file URL",
				fmt.Sprintf("a %s file referenced by URL was dropped; inline the bytes instead", p.MediaType),
			)}, nil
		}
		return &contentPart{Type: "image_url", ImageURL: &imageURL{URL: d.URL.String()}}, nil, nil

	case provider.FileDataBytes:
		encoded := d.Base64
		if encoded == "" {
			encoded = base64.StdEncoding.EncodeToString(d.Data)
		}
		dataURI := "data:" + fullMediaType(p.MediaType) + ";base64," + encoded

		if isImage {
			return &contentPart{Type: "image_url", ImageURL: &imageURL{URL: dataURI}}, nil, nil
		}
		return &contentPart{
			Type: "file",
			File: &fileData{Filename: p.Filename, FileData: dataURI},
		}, nil, nil

	case provider.FileDataText:
		// Inline text has no dedicated part, so it becomes text content.
		return &contentPart{Type: "text", Text: d.Text}, nil, nil

	case provider.FileDataRef:
		id, ok := d.Reference[providerID]
		if !ok {
			for _, v := range d.Reference {
				id = v
				break
			}
		}
		return &contentPart{Type: "file", File: &fileData{FileID: id}}, nil, nil

	default:
		return nil, nil, &provider.InvalidPromptError{
			Message: fmt.Sprintf("unsupported file data %T", p.Data),
		}
	}
}

// assistantMessage renders an assistant message, hoisting tool calls out of
// the content list into the sibling tool_calls field.
// reasoningReplay is whether an assistant turn's reasoning goes back as
// reasoning_content: never, when the turn had some, or always (empty when
// it had none).
type reasoningReplay int

const (
	replayNever reasoningReplay = iota
	replayPresent
	replayAlways
)

func assistantMessage(m provider.AssistantMessage, replay reasoningReplay) (apiMessage, []provider.Warning, error) {
	var (
		text      strings.Builder
		reasoning strings.Builder
		toolCalls []apiToolCall
		warnings  []provider.Warning
	)

	for _, part := range m.Content {
		switch p := part.(type) {
		case provider.TextPart:
			text.WriteString(p.Text)

		case provider.ReasoningPart:
			// Replayed as reasoning_content, as the AI SDK does: DeepSeek and
			// Kimi need it on tool-calling turns, and a provider whose cache
			// holds the reasoning it produced reads the turn back as cached.
			reasoning.WriteString(p.Text)

		case provider.ToolCallPart:
			args, err := json.Marshal(p.Input)
			if err != nil {
				return apiMessage{}, nil, &provider.InvalidPromptError{
					Message: fmt.Sprintf("encoding input for tool call %s", p.ToolCallID),
					Cause:   err,
				}
			}
			toolCalls = append(toolCalls, apiToolCall{
				ID:       p.ToolCallID,
				Type:     "function",
				Function: apiFunctionCall{Name: p.ToolName, Arguments: string(args)},
			})

		case provider.FilePart, provider.CustomPart, provider.ReasoningFilePart:
			warnings = append(warnings, provider.Unsupported(
				fmt.Sprintf("assistant content part %T", part),
				"the part was dropped from the request",
			))
		}
	}

	out := apiMessage{Role: "assistant", ToolCalls: toolCalls}
	// Content is omitted rather than sent empty when the turn was only tool
	// calls, because several gateways reject an empty assistant content
	// alongside tool_calls.
	if text.Len() > 0 || len(toolCalls) == 0 {
		out.Content = text.String()
	}
	if r := reasoning.String(); replay == replayAlways || replay == replayPresent && r != "" {
		out.ReasoningContent = &r
	}
	return out, warnings, nil
}

// toolMessages renders tool results as one message per result, which is what
// the chat API expects.
func toolMessages(m provider.ToolMessage) ([]apiMessage, []provider.Warning, error) {
	var (
		out      []apiMessage
		warnings []provider.Warning
	)

	for _, part := range m.Content {
		switch p := part.(type) {
		case provider.ToolResultPart:
			content, w := toolResultContent(p.Output)
			warnings = append(warnings, w...)
			out = append(out, apiMessage{
				Role:       "tool",
				ToolCallID: p.ToolCallID,
				Content:    content,
			})

		case provider.ToolApprovalResponsePart:
			warnings = append(warnings, provider.Unsupported(
				"tool approval response",
				"the chat completions API has no approval flow, so the response was dropped",
			))

		default:
			return nil, nil, &provider.InvalidPromptError{
				Message: fmt.Sprintf("unsupported tool content part %T", part),
			}
		}
	}
	return out, warnings, nil
}

// toolResultContent flattens a tool result to the string the chat API takes.
//
// The API has no error flag on a tool message, so failures are described in
// the text; the model reads them the same way either way.
func toolResultContent(output provider.ToolResultOutput) (string, []provider.Warning) {
	switch o := output.(type) {
	case provider.ToolOutputText:
		return o.Value, nil

	case provider.ToolOutputErrorText:
		return "Error: " + o.Value, nil

	case provider.ToolOutputJSON:
		encoded, err := json.Marshal(o.Value)
		if err != nil {
			return fmt.Sprintf("Error: could not encode result: %v", err), nil
		}
		return string(encoded), nil

	case provider.ToolOutputErrorJSON:
		encoded, err := json.Marshal(o.Value)
		if err != nil {
			return fmt.Sprintf("Error: could not encode error: %v", err), nil
		}
		return "Error: " + string(encoded), nil

	case provider.ToolOutputExecutionDenied:
		if o.Reason != "" {
			return "The user denied execution of this tool: " + o.Reason, nil
		}
		return "The user denied execution of this tool.", nil

	case provider.ToolOutputContent:
		// A tool message takes a string, so multi-modal output collapses to
		// its text and a note about what was dropped.
		var (
			text     strings.Builder
			warnings []provider.Warning
		)
		for _, part := range o.Value {
			switch p := part.(type) {
			case provider.ToolContentText:
				text.WriteString(p.Text)
			case provider.ToolContentFile:
				warnings = append(warnings, provider.Unsupported(
					"file in tool result",
					"the chat completions API accepts only text in a tool message",
				))
				fmt.Fprintf(&text, "\n[file omitted: %s]", p.MediaType)
			}
		}
		return text.String(), warnings

	default:
		return "", []provider.Warning{provider.Unsupported(
			fmt.Sprintf("tool output %T", output), "",
		)}
	}
}

// topLevelMediaType returns the segment before the slash.
func topLevelMediaType(mediaType string) string {
	top, _, _ := strings.Cut(mediaType, "/")
	return strings.ToLower(top)
}

// fullMediaType expands a bare top-level type into a complete one, since a
// data: URI needs a full type.
func fullMediaType(mediaType string) string {
	if strings.Contains(mediaType, "/") && !strings.HasSuffix(mediaType, "/*") {
		return mediaType
	}
	switch topLevelMediaType(mediaType) {
	case "image":
		return "image/png"
	case "text":
		return "text/plain"
	case "audio":
		return "audio/mpeg"
	default:
		return "application/octet-stream"
	}
}

// mapFinishReason converts an OpenAI finish_reason to the unified reason.
func mapFinishReason(raw *string) provider.FinishReason {
	if raw == nil {
		return provider.FinishReason{Unified: provider.FinishOther}
	}

	unified := provider.FinishOther
	switch *raw {
	case "stop", "end_turn":
		unified = provider.FinishStop
	case "length", "max_tokens":
		unified = provider.FinishLength
	case "tool_calls", "function_call":
		unified = provider.FinishToolCalls
	case "content_filter":
		unified = provider.FinishContentFilter
	case "insufficient_system_resource":
		// DeepSeek reports server overload this way.
		unified = provider.FinishError
	}

	return provider.FinishReason{Unified: unified, Raw: *raw}
}

// convertUsage maps OpenAI usage onto the spec's breakdown.
//
// prompt_tokens already includes cached tokens here, unlike Anthropic, so the
// total is taken directly and the uncached figure is derived by subtraction.
func convertUsage(u *apiUsage) provider.Usage {
	if u == nil {
		return provider.Usage{}
	}

	total := u.PromptTokens
	cacheRead := u.cachedInputTokens()
	noCache := total - cacheRead
	// A gateway that counts the cache outside prompt_tokens (the AI SDK's
	// xAI guard): the prompt is the sum, and prompt_tokens the uncached part.
	if cacheRead > total {
		total, noCache = u.PromptTokens+cacheRead, u.PromptTokens
	}

	output := u.CompletionTokens

	usage := provider.Usage{
		InputTokens: provider.InputTokens{
			Total:     &total,
			NoCache:   &noCache,
			CacheRead: &cacheRead,
		},
		OutputTokens: provider.OutputTokens{Total: &output},
	}
	if d := u.PromptTokensDetails; d != nil && d.CacheWriteTokens > 0 {
		write := d.CacheWriteTokens
		fresh := max(0, noCache-write)
		usage.InputTokens.CacheWrite, usage.InputTokens.NoCache = &write, &fresh
	}

	if u.CompletionTokensDetails != nil {
		reasoning := u.CompletionTokensDetails.ReasoningTokens
		// OpenAI counts reasoning inside completion_tokens; xAI reports it
		// beside them (total_tokens = prompt + completion + reasoning), so
		// there the output is the sum and completion_tokens the text.
		if reasoning > 0 && u.TotalTokens == u.PromptTokens+u.CompletionTokens+reasoning {
			output = u.CompletionTokens + reasoning
		}
		text := max(0, output-reasoning)
		usage.OutputTokens.Reasoning = &reasoning
		usage.OutputTokens.Text = &text
	}

	if raw, err := json.Marshal(u); err == nil {
		var obj provider.JSONObject
		if json.Unmarshal(raw, &obj) == nil {
			usage.Raw = obj
		}
	}

	return usage
}
