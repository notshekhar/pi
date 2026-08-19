package bedrock

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// convertedPrompt is a spec prompt rendered for Converse.
type convertedPrompt struct {
	System   []systemBlock
	Messages []apiMessage
	Warnings []provider.Warning
}

// convertPrompt renders a spec prompt into Converse's shape.
//
// Converse keeps system prompts in their own field, and requires strictly
// alternating user and assistant turns, so consecutive messages of the same
// role are merged rather than sent as-is.
//
// isMistral rewrites tool-call ids: Mistral on Bedrock rejects anything that
// is not exactly nine alphanumeric characters.
func convertPrompt(prompt provider.Prompt, isMistral bool) (*convertedPrompt, error) {
	out := &convertedPrompt{}

	for _, message := range prompt {
		switch m := message.(type) {
		case provider.SystemMessage:
			out.System = append(out.System, systemBlock{Text: m.Content})
			if cacheBreakpoint(m.ProviderOptions) {
				out.System = append(out.System, systemBlock{CachePoint: &cachePoint{Type: "default"}})
			}

		case provider.UserMessage:
			blocks, warnings, err := userBlocks(m)
			if err != nil {
				return nil, err
			}
			out.Warnings = append(out.Warnings, warnings...)
			out.append("user", blocks)

		case provider.AssistantMessage:
			blocks, warnings, err := assistantBlocks(m, isMistral)
			if err != nil {
				return nil, err
			}
			out.Warnings = append(out.Warnings, warnings...)
			out.append("assistant", blocks)

		case provider.ToolMessage:
			blocks, err := toolBlocks(m, isMistral)
			if err != nil {
				return nil, err
			}
			// Converse carries tool results in a user turn, as Anthropic's own
			// API does.
			out.append("user", blocks)

		default:
			return nil, &provider.InvalidPromptError{
				Message: fmt.Sprintf("unsupported message type %T", message),
			}
		}
	}

	return out, nil
}

// append adds blocks to the conversation, merging into the previous message
// when the role repeats. Converse rejects two consecutive turns of one role.
func (c *convertedPrompt) append(role string, blocks []apiBlock) {
	if len(blocks) == 0 {
		return
	}
	if n := len(c.Messages); n > 0 && c.Messages[n-1].Role == role {
		c.Messages[n-1].Content = append(c.Messages[n-1].Content, blocks...)
		return
	}
	c.Messages = append(c.Messages, apiMessage{Role: role, Content: blocks})
}

// userBlocks renders a user message.
func userBlocks(m provider.UserMessage) ([]apiBlock, []provider.Warning, error) {
	var (
		blocks   []apiBlock
		warnings []provider.Warning
	)

	for _, part := range m.Content {
		switch p := part.(type) {
		case provider.TextPart:
			blocks = append(blocks, apiBlock{Text: p.Text})

		case provider.FilePart:
			block, warning, err := fileBlock(p)
			if err != nil {
				return nil, nil, err
			}
			if warning != nil {
				warnings = append(warnings, *warning)
				continue
			}
			blocks = append(blocks, *block)

		default:
			return nil, nil, &provider.InvalidPromptError{
				Message: fmt.Sprintf("unsupported user content part %T", part),
			}
		}
	}

	if cacheBreakpoint(m.ProviderOptions) {
		blocks = append(blocks, apiBlock{CachePoint: &cachePoint{Type: "default"}})
	}
	return blocks, warnings, nil
}

// assistantBlocks renders an assistant message.
func assistantBlocks(m provider.AssistantMessage, isMistral bool) ([]apiBlock, []provider.Warning, error) {
	var (
		blocks   []apiBlock
		warnings []provider.Warning
	)

	for _, part := range m.Content {
		switch p := part.(type) {
		case provider.TextPart:
			// Converse rejects an empty text block, and a model that only
			// called a tool produces one.
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			blocks = append(blocks, apiBlock{Text: p.Text})

		case provider.ReasoningPart:
			block, ok := reasoningReplay(p)
			if !ok {
				// Replayed thinking without its signature is rejected, so it
				// is dropped rather than failing the whole turn.
				warnings = append(warnings, provider.Unsupported(
					"reasoning without signature",
					"a reasoning part was dropped because it carries no bedrock signature",
				))
				continue
			}
			blocks = append(blocks, block)

		case provider.ToolCallPart:
			blocks = append(blocks, apiBlock{ToolUse: &toolUseBlock{
				ToolUseID: normalizeToolCallID(p.ToolCallID, isMistral),
				Name:      sanitizeToolName(p.ToolName),
				Input:     p.Input,
			}})

		case provider.ToolResultPart:
			block, err := toolResult(p, isMistral)
			if err != nil {
				return nil, nil, err
			}
			blocks = append(blocks, *block)

		case provider.FilePart:
			block, warning, err := fileBlock(p)
			if err != nil {
				return nil, nil, err
			}
			if warning != nil {
				warnings = append(warnings, *warning)
				continue
			}
			blocks = append(blocks, *block)

		default:
			warnings = append(warnings, provider.Unsupported(
				fmt.Sprintf("assistant content part %T", part),
				"the part was dropped from the request",
			))
		}
	}

	if cacheBreakpoint(m.ProviderOptions) {
		blocks = append(blocks, apiBlock{CachePoint: &cachePoint{Type: "default"}})
	}
	return blocks, warnings, nil
}

// toolBlocks renders a tool message into user-role blocks.
func toolBlocks(m provider.ToolMessage, isMistral bool) ([]apiBlock, error) {
	blocks := make([]apiBlock, 0, len(m.Content))

	for _, part := range m.Content {
		p, ok := part.(provider.ToolResultPart)
		if !ok {
			// Approval responses have no Converse equivalent; the approval
			// itself happened before the call was ever made.
			continue
		}
		block, err := toolResult(p, isMistral)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, *block)
	}
	return blocks, nil
}

// toolResult renders one tool result.
func toolResult(p provider.ToolResultPart, isMistral bool) (*apiBlock, error) {
	block := &toolResultBlock{ToolUseID: normalizeToolCallID(p.ToolCallID, isMistral), Status: "success"}

	switch out := p.Output.(type) {
	case provider.ToolOutputText:
		block.Content = []toolResultPart{{Text: out.Value}}

	case provider.ToolOutputErrorText:
		block.Content = []toolResultPart{{Text: out.Value}}
		block.Status = "error"

	case provider.ToolOutputJSON:
		block.Content = []toolResultPart{{JSON: out.Value}}

	case provider.ToolOutputErrorJSON:
		block.Content = []toolResultPart{{JSON: out.Value}}
		block.Status = "error"

	case provider.ToolOutputExecutionDenied:
		reason := out.Reason
		if reason == "" {
			reason = "The user denied execution of this tool."
		}
		block.Content = []toolResultPart{{Text: reason}}
		block.Status = "error"

	case provider.ToolOutputContent:
		for _, item := range out.Value {
			switch v := item.(type) {
			case provider.ToolContentText:
				block.Content = append(block.Content, toolResultPart{Text: v.Text})
			case provider.ToolContentFile:
				format, ok := imageFormat(v.MediaType)
				if !ok {
					block.Content = append(block.Content, toolResultPart{
						Text: fmt.Sprintf("[%s content omitted: bedrock accepts only images in tool results]", v.MediaType),
					})
					continue
				}
				source, err := fileSource(v.Data)
				if err != nil {
					return nil, err
				}
				block.Content = append(block.Content, toolResultPart{
					Image: &imageBlock{Format: format, Source: source},
				})
			}
		}

	default:
		return nil, &provider.InvalidPromptError{
			Message: fmt.Sprintf("unsupported tool output %T", p.Output),
		}
	}

	if len(block.Content) == 0 {
		// Converse rejects an empty tool result.
		block.Content = []toolResultPart{{Text: ""}}
	}
	return &apiBlock{ToolResult: block}, nil
}

// reasoningReplay renders replayed thinking, reporting false when it cannot be
// replayed because the signature is missing.
func reasoningReplay(p provider.ReasoningPart) (apiBlock, bool) {
	opts := optionBlock(p.ProviderOptions)
	if opts == nil {
		return apiBlock{}, false
	}

	if redacted, ok := stringOpt(opts, "redactedData", "redactedContent"); ok && redacted != "" {
		return apiBlock{ReasoningContent: &reasoningBlock{
			RedactedContent: []byte(redacted),
		}}, true
	}

	signature, _ := stringOpt(opts, "signature")
	if signature == "" {
		return apiBlock{}, false
	}
	return apiBlock{ReasoningContent: &reasoningBlock{
		ReasoningText: &reasoningText{Text: p.Text, Signature: signature},
	}}, true
}

// fileBlock renders an image, video or document attachment. A warning rather
// than a block means the file was dropped.
func fileBlock(p provider.FilePart) (*apiBlock, *provider.Warning, error) {
	source, err := fileSource(p.Data)
	if err != nil {
		return nil, nil, err
	}

	if format, ok := imageFormat(p.MediaType); ok {
		return &apiBlock{Image: &imageBlock{Format: format, Source: source}}, nil, nil
	}

	if format, ok := videoFormat(p.MediaType); ok {
		return &apiBlock{Video: &videoBlock{Format: format, Source: source}}, nil, nil
	}

	if format, ok := documentFormat(p.MediaType, p.Filename); ok {
		name := p.Filename
		if name == "" {
			name = "document"
		}
		return &apiBlock{Document: &documentBlock{
			Format: format,
			// Converse rejects a name with characters outside a narrow set.
			Name:   sanitizeDocumentName(name),
			Source: source,
		}}, nil, nil
	}

	warning := provider.Unsupported(
		"file "+p.MediaType,
		"bedrock accepts only known image, video and document formats; the file was dropped",
	)
	return nil, &warning, nil
}

// fileSource renders file data as inline bytes or an S3 location.
//
// A non-S3 URL should never reach here: core downloads anything the model
// cannot fetch, and SupportedURLs only claims s3://.
func fileSource(data provider.FileData) (imageSource, error) {
	switch v := data.(type) {
	case provider.FileDataBytes:
		raw, err := fileBytes(v)
		if err != nil {
			return imageSource{}, err
		}
		return imageSource{Bytes: raw}, nil

	case provider.FileDataText:
		return imageSource{Bytes: []byte(v.Text)}, nil

	case provider.FileDataURL:
		if v.URL == nil || v.URL.Scheme != "s3" {
			return imageSource{}, &provider.InvalidPromptError{
				Message: fmt.Sprintf("bedrock only fetches s3:// URLs, got %v", v.URL),
			}
		}
		return imageSource{S3Location: &s3Location{URI: v.URL.String()}}, nil

	default:
		return imageSource{}, &provider.InvalidPromptError{
			Message: fmt.Sprintf("bedrock needs inline file content or an s3:// URL, got %T", data),
		}
	}
}

// fileBytes extracts the raw bytes of an inline file part.
func fileBytes(v provider.FileDataBytes) ([]byte, error) {
	if v.Data != nil {
		return v.Data, nil
	}
	decoded, err := decodeBase64(v.Base64)
	if err != nil {
		return nil, &provider.InvalidPromptError{
			Message: "file content is not valid base64",
			Cause:   err,
		}
	}
	return decoded, nil
}

// imageFormat maps a media type onto the bare subtype Converse expects,
// reporting false for anything it does not accept.
func imageFormat(mediaType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png":
		return "png", true
	case "image/jpeg", "image/jpg":
		return "jpeg", true
	case "image/gif":
		return "gif", true
	case "image/webp":
		return "webp", true
	}
	return "", false
}

// videoFormat maps a media type onto the bare subtype Converse expects.
func videoFormat(mediaType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "video/mp4":
		return "mp4", true
	case "video/quicktime":
		return "mov", true
	case "video/webm":
		return "webm", true
	case "video/x-matroska":
		return "mkv", true
	case "video/x-flv":
		return "flv", true
	case "video/mpeg":
		return "mpeg", true
	case "video/mpg":
		return "mpg", true
	case "video/wmv", "video/x-ms-wmv":
		return "wmv", true
	case "video/3gpp":
		return "three_gp", true
	}
	return "", false
}

// documentFormat maps a media type, or a filename extension when the media
// type is generic, onto a Converse document format.
func documentFormat(mediaType, filename string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "application/pdf":
		return "pdf", true
	case "text/csv":
		return "csv", true
	case "text/html":
		return "html", true
	case "text/plain":
		return "txt", true
	case "text/markdown":
		return "md", true
	case "application/msword":
		return "doc", true
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return "docx", true
	case "application/vnd.ms-excel":
		return "xls", true
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return "xlsx", true
	}

	// Fall back to the extension: callers routinely attach a PDF labelled
	// application/octet-stream.
	if i := strings.LastIndex(filename, "."); i >= 0 {
		switch ext := strings.ToLower(filename[i+1:]); ext {
		case "pdf", "csv", "doc", "docx", "xls", "xlsx", "html", "txt", "md":
			return ext, true
		}
	}
	return "", false
}

// sanitizeDocumentName strips characters Converse rejects in a document name.
// It allows letters, digits, spaces, hyphens and brackets, and collapses
// everything else to a hyphen.
func sanitizeDocumentName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '(', r == ')', r == '[', r == ']':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "document"
	}
	return b.String()
}

// cacheBreakpoint reports whether a message asked for a cache point.
func cacheBreakpoint(opts provider.ProviderOptions) bool {
	block := optionBlock(opts)
	if block == nil {
		return false
	}
	switch v := block["cachePoint"].(type) {
	case bool:
		return v
	case map[string]any:
		// The TypeScript SDK sends {type: "default"} rather than a bool.
		return true
	}
	// Accept the shared spelling too, so a prompt built for Anthropic works
	// unchanged against Bedrock.
	if v, ok := block["cacheControl"].(bool); ok {
		return v
	}
	return false
}

// decodeBase64 decodes standard or URL-safe base64, tolerating missing padding.
func decodeBase64(s string) ([]byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}
