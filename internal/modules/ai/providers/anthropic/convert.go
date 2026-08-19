package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// maxCacheBreakpoints is Anthropic's per-request limit on cache_control marks.
const maxCacheBreakpoints = 4

// convertedPrompt is a spec prompt rendered into Anthropic's wire shape.
type convertedPrompt struct {
	System   []systemBlock
	Messages []apiMessage
	Warnings []provider.Warning
}

// promptConverter walks a spec prompt and builds the Anthropic request body.
// It is stateful because cache breakpoints are budgeted across the whole
// request, not per message.
type promptConverter struct {
	breakpoints int
	warnings    []provider.Warning
}

// convertPrompt renders a spec prompt into Anthropic messages.
//
// Two structural rules drive the shape of this code:
//
//   - Anthropic takes the system prompt out of band, so system messages become
//     the System field. They are only legal before the first non-system
//     message.
//   - Anthropic has no "tool" role. Tool results are user messages, so
//     consecutive tool and user messages merge into one.
func convertPrompt(prompt provider.Prompt) (*convertedPrompt, error) {
	c := &promptConverter{}
	out := &convertedPrompt{}

	seenNonSystem := false

	for _, msg := range prompt {
		switch m := msg.(type) {
		case provider.SystemMessage:
			if seenNonSystem {
				return nil, &provider.InvalidPromptError{
					Message: "system messages must come before any user or assistant message",
				}
			}
			out.System = append(out.System, systemBlock{
				Type:         "text",
				Text:         m.Content,
				CacheControl: c.cacheControlFor(m.ProviderOptions, "system message"),
			})

		case provider.UserMessage:
			seenNonSystem = true
			blocks, err := c.userBlocks(m)
			if err != nil {
				return nil, err
			}
			c.appendBlocks(&out.Messages, "user", blocks)

		case provider.ToolMessage:
			seenNonSystem = true
			blocks, err := c.toolBlocks(m)
			if err != nil {
				return nil, err
			}
			// Tool results ride in a user message, and merge with an adjacent
			// one so the conversation does not gain empty turns.
			c.appendBlocks(&out.Messages, "user", blocks)

		case provider.AssistantMessage:
			seenNonSystem = true
			blocks, err := c.assistantBlocks(m)
			if err != nil {
				return nil, err
			}
			c.appendBlocks(&out.Messages, "assistant", blocks)

		default:
			return nil, &provider.InvalidPromptError{
				Message: fmt.Sprintf("unsupported message type %T", msg),
			}
		}
	}

	out.Warnings = c.warnings
	return out, nil
}

// appendBlocks adds blocks to the message list, merging into the previous
// message when the roles match.
func (c *promptConverter) appendBlocks(messages *[]apiMessage, role string, blocks []apiBlock) {
	if len(blocks) == 0 {
		return
	}
	if n := len(*messages); n > 0 && (*messages)[n-1].Role == role {
		(*messages)[n-1].Content = append((*messages)[n-1].Content, blocks...)
		return
	}
	*messages = append(*messages, apiMessage{Role: role, Content: blocks})
}

// userBlocks renders a user message.
func (c *promptConverter) userBlocks(m provider.UserMessage) ([]apiBlock, error) {
	blocks := make([]apiBlock, 0, len(m.Content))

	for i, part := range m.Content {
		// A cache breakpoint belongs on the final block of the message, since
		// it marks a prefix boundary.
		last := i == len(m.Content)-1

		switch p := part.(type) {
		case provider.TextPart:
			blocks = append(blocks, apiBlock{
				Type:         "text",
				Text:         p.Text,
				CacheControl: c.partCacheControl(p.ProviderOptions, m.ProviderOptions, last, "text part"),
			})

		case provider.FilePart:
			block, err := c.fileBlock(p)
			if err != nil {
				return nil, err
			}
			block.CacheControl = c.partCacheControl(p.ProviderOptions, m.ProviderOptions, last, "file part")
			blocks = append(blocks, *block)

		default:
			return nil, &provider.InvalidPromptError{
				Message: fmt.Sprintf("unsupported user content part %T", part),
			}
		}
	}
	return blocks, nil
}

// fileBlock renders a file as an image or document block.
func (c *promptConverter) fileBlock(p provider.FilePart) (*apiBlock, error) {
	source, err := fileSource(p.Data, p.MediaType)
	if err != nil {
		return nil, err
	}

	// Anthropic splits attachments by top level: images use an image block,
	// everything else a document block.
	if topLevelMediaType(p.MediaType) == "image" {
		return &apiBlock{Type: "image", Source: source}, nil
	}

	block := &apiBlock{Type: "document", Source: source, Title: p.Filename}

	if opts := p.ProviderOptions.Get(providerID); opts != nil {
		if title, ok := opts["title"].(string); ok && title != "" {
			block.Title = title
		}
		if context, ok := opts["context"].(string); ok {
			block.Context = context
		}
		if cite, ok := opts["citations"].(map[string]any); ok {
			if enabled, ok := cite["enabled"].(bool); ok {
				block.Citations = &citations{Enabled: enabled}
			}
		}
	}

	return block, nil
}

// fileSource renders file data into an Anthropic source object.
func fileSource(data provider.FileData, mediaType string) (*blockSource, error) {
	switch d := data.(type) {
	case provider.FileDataBytes:
		encoded := d.Base64
		if encoded == "" {
			encoded = base64.StdEncoding.EncodeToString(d.Data)
		}
		return &blockSource{
			Type:      "base64",
			MediaType: fullMediaType(mediaType),
			Data:      encoded,
		}, nil

	case provider.FileDataURL:
		return &blockSource{Type: "url", URL: d.URL.String()}, nil

	case provider.FileDataText:
		return &blockSource{
			Type:      "text",
			MediaType: "text/plain",
			Data:      d.Text,
		}, nil

	case provider.FileDataRef:
		id, ok := d.Reference[providerID]
		if !ok {
			return nil, &provider.InvalidPromptError{
				Message: "file reference does not carry an anthropic file id",
			}
		}
		return &blockSource{Type: "file", FileID: id}, nil

	default:
		return nil, &provider.InvalidPromptError{
			Message: fmt.Sprintf("unsupported file data %T", data),
		}
	}
}

// assistantBlocks renders an assistant message.
func (c *promptConverter) assistantBlocks(m provider.AssistantMessage) ([]apiBlock, error) {
	blocks := make([]apiBlock, 0, len(m.Content))

	for i, part := range m.Content {
		last := i == len(m.Content)-1

		switch p := part.(type) {
		case provider.TextPart:
			// Anthropic rejects trailing whitespace on the last assistant
			// block, which is exactly where a prefill ends up.
			text := p.Text
			if last {
				text = strings.TrimRight(text, " \t\n")
			}
			if text == "" {
				continue
			}
			blocks = append(blocks, apiBlock{
				Type:         "text",
				Text:         text,
				CacheControl: c.partCacheControl(p.ProviderOptions, m.ProviderOptions, last, "text part"),
			})

		case provider.ReasoningPart:
			block, ok := reasoningBlock(p)
			if !ok {
				// Reasoning replayed without its signature would be rejected,
				// so drop it. The text is still in the transcript for the
				// caller; only the model-facing copy is removed.
				c.warn(provider.Unsupported(
					"reasoning without signature",
					"a reasoning part was dropped because it carries no anthropic signature or redacted data",
				))
				continue
			}
			blocks = append(blocks, block)

		case provider.ToolCallPart:
			input, err := json.Marshal(p.Input)
			if err != nil {
				return nil, &provider.InvalidPromptError{
					Message: fmt.Sprintf("encoding input for tool call %s", p.ToolCallID),
					Cause:   err,
				}
			}

			// A hosted tool replays as server_tool_use under the provider's own
			// name for it, which is not always the name the caller used.
			blockType, name := "tool_use", p.ToolName
			if p.ProviderExecuted {
				blockType = "server_tool_use"
				if server, ok := p.ProviderOptions.Get(providerID)["serverToolName"].(string); ok && server != "" {
					name = server
				}
			}

			blocks = append(blocks, apiBlock{
				Type:         blockType,
				ID:           p.ToolCallID,
				Name:         name,
				Input:        input,
				CacheControl: c.partCacheControl(p.ProviderOptions, m.ProviderOptions, last, "tool call"),
			})

		case provider.ToolResultPart:
			// Provider-executed tools report their results inside the
			// assistant turn, in a block type named after the tool.
			if block, ok := hostedResultReplay(p); ok {
				blocks = append(blocks, block)
				continue
			}
			block, err := c.toolResultBlock(p)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, *block)

		case provider.FilePart:
			block, err := c.fileBlock(p)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, *block)

		case provider.CustomPart, provider.ReasoningFilePart:
			c.warn(provider.Unsupported(
				fmt.Sprintf("assistant content part %T", part),
				"the part was dropped from the request",
			))

		default:
			return nil, &provider.InvalidPromptError{
				Message: fmt.Sprintf("unsupported assistant content part %T", part),
			}
		}
	}
	return blocks, nil
}

// reasoningBlock renders a reasoning part, reporting false when it cannot be
// replayed. Anthropic requires either a signature or the redacted payload.
func reasoningBlock(p provider.ReasoningPart) (apiBlock, bool) {
	opts := p.ProviderOptions.Get(providerID)
	if opts == nil {
		return apiBlock{}, false
	}

	if sig, ok := opts["signature"].(string); ok && sig != "" {
		text := p.Text
		return apiBlock{Type: "thinking", Thinking: &text, Signature: sig}, true
	}
	if data, ok := opts["redactedData"].(string); ok && data != "" {
		return apiBlock{Type: "redacted_thinking", Data: data}, true
	}
	return apiBlock{}, false
}

// toolBlocks renders a tool-role message into user-role blocks.
func (c *promptConverter) toolBlocks(m provider.ToolMessage) ([]apiBlock, error) {
	blocks := make([]apiBlock, 0, len(m.Content))

	for i, part := range m.Content {
		last := i == len(m.Content)-1

		switch p := part.(type) {
		case provider.ToolResultPart:
			block, err := c.toolResultBlock(p)
			if err != nil {
				return nil, err
			}
			block.CacheControl = c.partCacheControl(p.ProviderOptions, m.ProviderOptions, last, "tool result")
			blocks = append(blocks, *block)

		case provider.ToolApprovalResponsePart:
			// Approvals only apply to provider-executed tools, which this
			// milestone does not send, so there is nothing to forward.
			c.warn(provider.Unsupported(
				"tool approval response",
				"provider-executed tools are not yet supported, so the approval was dropped",
			))

		default:
			return nil, &provider.InvalidPromptError{
				Message: fmt.Sprintf("unsupported tool content part %T", part),
			}
		}
	}
	return blocks, nil
}

// toolResultBlock renders a tool result.
func (c *promptConverter) toolResultBlock(p provider.ToolResultPart) (*apiBlock, error) {
	block := &apiBlock{Type: "tool_result", ToolUseID: p.ToolCallID}

	isError := func() *bool { t := true; return &t }

	switch out := p.Output.(type) {
	case provider.ToolOutputText:
		block.Content = out.Value

	case provider.ToolOutputErrorText:
		block.Content = out.Value
		block.IsError = isError()

	case provider.ToolOutputJSON:
		encoded, err := json.Marshal(out.Value)
		if err != nil {
			return nil, &provider.InvalidPromptError{
				Message: fmt.Sprintf("encoding result for tool call %s", p.ToolCallID),
				Cause:   err,
			}
		}
		block.Content = string(encoded)

	case provider.ToolOutputErrorJSON:
		encoded, err := json.Marshal(out.Value)
		if err != nil {
			return nil, &provider.InvalidPromptError{
				Message: fmt.Sprintf("encoding error for tool call %s", p.ToolCallID),
				Cause:   err,
			}
		}
		block.Content = string(encoded)
		block.IsError = isError()

	case provider.ToolOutputExecutionDenied:
		reason := out.Reason
		if reason == "" {
			reason = "The user denied execution of this tool."
		}
		block.Content = reason
		block.IsError = isError()

	case provider.ToolOutputContent:
		parts, err := c.toolContentBlocks(out.Value)
		if err != nil {
			return nil, err
		}
		block.Content = parts

	default:
		return nil, &provider.InvalidPromptError{
			Message: fmt.Sprintf("unsupported tool output %T", p.Output),
		}
	}

	return block, nil
}

// toolContentBlocks renders multi-modal tool output.
func (c *promptConverter) toolContentBlocks(parts []provider.ToolContentPart) ([]apiBlock, error) {
	out := make([]apiBlock, 0, len(parts))

	for _, part := range parts {
		switch p := part.(type) {
		case provider.ToolContentText:
			out = append(out, apiBlock{Type: "text", Text: p.Text})

		case provider.ToolContentFile:
			source, err := fileSource(p.Data, p.MediaType)
			if err != nil {
				return nil, err
			}
			if topLevelMediaType(p.MediaType) != "image" {
				// Anthropic only accepts text and image blocks inside a tool
				// result, so anything else has to be described rather than sent.
				c.warn(provider.Unsupported(
					"non-image file in tool result",
					fmt.Sprintf("a %s file was replaced with a placeholder", p.MediaType),
				))
				out = append(out, apiBlock{
					Type: "text",
					Text: fmt.Sprintf("[file omitted: %s]", p.MediaType),
				})
				continue
			}
			out = append(out, apiBlock{Type: "image", Source: source})

		case provider.ToolContentCustom:
			c.warn(provider.Unsupported(
				"custom tool result content",
				"the part was dropped from the request",
			))
		}
	}
	return out, nil
}

// partCacheControl resolves a cache breakpoint for a content part, falling
// back to the message-level setting on the message's last part.
func (c *promptConverter) partCacheControl(
	part, message provider.ProviderOptions,
	isLast bool,
	context string,
) *cacheControl {
	if cc := c.cacheControlFor(part, context); cc != nil {
		return cc
	}
	if isLast {
		return c.cacheControlFor(message, context)
	}
	return nil
}

// cacheControlFor reads a cache breakpoint out of provider options, budgeting
// it against Anthropic's four-breakpoint limit.
//
// Both cacheControl and cache_control are accepted, because callers port
// snippets from the TypeScript SDK and from raw API examples alike.
func (c *promptConverter) cacheControlFor(opts provider.ProviderOptions, context string) *cacheControl {
	block := opts.Get(providerID)
	if block == nil {
		return nil
	}

	raw, ok := block["cacheControl"]
	if !ok {
		raw, ok = block["cache_control"]
	}
	if !ok || raw == nil {
		return nil
	}

	c.breakpoints++
	if c.breakpoints > maxCacheBreakpoints {
		c.warn(provider.Unsupported(
			"cacheControl breakpoint limit",
			fmt.Sprintf(
				"anthropic allows %d cache breakpoints per request; the one on this %s was ignored",
				maxCacheBreakpoints, context,
			),
		))
		return nil
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
		if !v {
			c.breakpoints--
			return nil
		}
		return &cacheControl{Type: "ephemeral"}
	default:
		return &cacheControl{Type: "ephemeral"}
	}
}

// warn records a warning for the call.
func (c *promptConverter) warn(w provider.Warning) {
	c.warnings = append(c.warnings, w)
}

// topLevelMediaType returns the segment before the slash, so that "image",
// "image/*" and "image/png" all yield "image".
func topLevelMediaType(mediaType string) string {
	top, _, _ := strings.Cut(mediaType, "/")
	return strings.ToLower(top)
}

// fullMediaType expands a bare top-level type into something Anthropic
// accepts, since the API demands a complete type/subtype pair.
func fullMediaType(mediaType string) string {
	if strings.Contains(mediaType, "/") && !strings.HasSuffix(mediaType, "/*") {
		return mediaType
	}
	switch topLevelMediaType(mediaType) {
	case "image":
		return "image/png"
	case "text":
		return "text/plain"
	case "application", "":
		return "application/pdf"
	default:
		return mediaType
	}
}
