package openairesponses

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/notshekhar/pi/internal/ai/provider"
)

// capabilities is the AI SDK's getOpenAILanguageModelCapabilities, read off
// the model id. A gateway's vendor prefix ("azure/gpt-5.6-sol") is looked
// past: the SDK cannot, and loop works around it with forceReasoning.
type capabilities struct {
	reasoning        bool
	developerRole    bool
	supportedEfforts []string // nil: any effort is passed through
}

var (
	oSeries = regexp.MustCompile(`^o(\d+)(?:-|$)`)
	gptID   = regexp.MustCompile(`^gpt-(\d+)(?:\.(\d+))?(?:-(.+))?$`)
)

func capabilitiesOf(modelID string) capabilities {
	id := modelID[strings.LastIndex(modelID, "/")+1:]
	var c capabilities
	if oSeries.MatchString(id) {
		c.reasoning = true
	}
	if m := gptID.FindStringSubmatch(id); m != nil {
		major, _ := strconv.Atoi(m[1])
		chat := m[2] == "" && strings.HasPrefix(m[3], "chat")
		if major >= 5 && !chat {
			c.reasoning = true
		}
		if major >= 6 {
			switch id {
			case "gpt-6-sol", "gpt-6-luna", "gpt-6.1-terra":
				c.supportedEfforts = []string{"none", "low", "medium", "high", "xhigh", "max"}
			default:
				c.supportedEfforts = []string{"low", "medium", "high", "xhigh", "max"}
			}
		}
	}
	c.developerRole = c.reasoning
	return c
}

// convertPrompt renders a spec prompt as Responses input items (the AI
// SDK's convertToOpenAIResponsesInput with store on, the API default).
func convertPrompt(prompt provider.Prompt, key string, reasoningModel bool) ([]any, []provider.Warning, error) {
	var (
		input    []any
		warnings []provider.Warning
	)
	for _, msg := range prompt {
		switch m := msg.(type) {
		case provider.SystemMessage:
			role := "system"
			if reasoningModel {
				role = "developer"
			}
			input = append(input, map[string]any{"role": role, "content": m.Content})

		case provider.UserMessage:
			var content []any
			for _, part := range m.Content {
				switch p := part.(type) {
				case provider.TextPart:
					content = append(content, map[string]any{"type": "input_text", "text": p.Text})
				case provider.FilePart:
					item, w := fileItem(p.Data, p.MediaType, p.Filename)
					warnings = append(warnings, w...)
					if item != nil {
						content = append(content, item)
					}
				default:
					return nil, nil, &provider.InvalidPromptError{Message: fmt.Sprintf("unsupported user content part %T", part)}
				}
			}
			input = append(input, map[string]any{"role": "user", "content": content})

		case provider.AssistantMessage:
			// A stored message of a reasoning model is bound to the reasoning
			// item that produced it, so it is referenced only after one.
			referenced := map[string]bool{}
			precedingReasoning := false
			for _, part := range m.Content {
				switch p := part.(type) {
				case provider.TextPart:
					id, _ := p.ProviderOptions.Get(key)["itemId"].(string)
					if id != "" && (!reasoningModel || precedingReasoning) {
						if !referenced[id] {
							referenced[id] = true
							input = append(input, map[string]any{"type": "item_reference", "id": id})
						}
						continue
					}
					input = append(input, map[string]any{"role": "assistant", "content": p.Text})

				case provider.ReasoningPart:
					precedingReasoning = true
					opts := p.ProviderOptions.Get(key)
					id, _ := opts["itemId"].(string)
					switch {
					case id != "":
						if !referenced[id] {
							referenced[id] = true
							input = append(input, map[string]any{"type": "item_reference", "id": id})
						}
					case opts["reasoningEncryptedContent"] != nil:
						item := map[string]any{"type": "reasoning", "encrypted_content": opts["reasoningEncryptedContent"], "summary": []any{}}
						if p.Text != "" {
							item["summary"] = []any{map[string]any{"type": "summary_text", "text": p.Text}}
						}
						input = append(input, item)
					default:
						// Another provider's reasoning: OpenAI cannot take it.
					}

				case provider.ToolCallPart:
					args, err := json.Marshal(p.Input)
					if err != nil {
						return nil, nil, &provider.InvalidPromptError{Message: "encoding input for tool call " + p.ToolCallID, Cause: err}
					}
					if p.Input == nil {
						args = []byte("{}")
					}
					// Client-run calls replay in full, never by reference: the
					// output pairs with the call by call_id alone.
					input = append(input, map[string]any{
						"type": "function_call", "call_id": p.ToolCallID, "name": p.ToolName, "arguments": string(args),
					})

				case provider.ToolResultPart:
					// Results of provider-run tools need no replay.
				default:
					warnings = append(warnings, provider.Unsupported(fmt.Sprintf("assistant content part %T", part), "the part was dropped from the request"))
				}
			}

		case provider.ToolMessage:
			for _, part := range m.Content {
				p, ok := part.(provider.ToolResultPart)
				if !ok {
					continue
				}
				input = append(input, map[string]any{"type": "function_call_output", "call_id": p.ToolCallID, "output": toolOutput(p.Output)})
			}

		default:
			return nil, nil, &provider.InvalidPromptError{Message: fmt.Sprintf("unsupported message type %T", msg)}
		}
	}
	return input, warnings, nil
}

// toolOutput is a function_call_output's output: a string, or content items
// for a result carrying images (the read tool's screenshots).
func toolOutput(output provider.ToolResultOutput) any {
	jsonText := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("could not encode the result: %v", err)
		}
		return string(raw)
	}
	switch o := output.(type) {
	case provider.ToolOutputText:
		return o.Value
	case provider.ToolOutputJSON:
		return jsonText(o.Value)
	case provider.ToolOutputErrorText:
		return jsonText(map[string]any{"error": o.Value})
	case provider.ToolOutputErrorJSON:
		return jsonText(map[string]any{"error": o.Value})
	case provider.ToolOutputExecutionDenied:
		if o.Reason != "" {
			return o.Reason
		}
		return "Tool call execution denied."
	case provider.ToolOutputContent:
		var items []any
		for _, part := range o.Value {
			switch p := part.(type) {
			case provider.ToolContentText:
				items = append(items, map[string]any{"type": "input_text", "text": p.Text})
			case provider.ToolContentFile:
				if item, _ := fileItem(p.Data, p.MediaType, p.Filename); item != nil {
					items = append(items, item)
				}
			}
		}
		return items
	}
	return ""
}

// fileItem is an attachment as an input_image, input_file or input_text.
func fileItem(data provider.FileData, mediaType, filename string) (map[string]any, []provider.Warning) {
	image := strings.HasPrefix(mediaType, "image/") || mediaType == "image"
	if mediaType == "image" || mediaType == "image/*" {
		mediaType = "image/jpeg"
	}
	switch d := data.(type) {
	case provider.FileDataText:
		return map[string]any{"type": "input_text", "text": d.Text}, nil
	case provider.FileDataURL:
		if image {
			return map[string]any{"type": "input_image", "image_url": d.URL.String()}, nil
		}
		return map[string]any{"type": "input_file", "file_url": d.URL.String()}, nil
	case provider.FileDataBytes:
		encoded := d.Base64
		if encoded == "" {
			encoded = base64.StdEncoding.EncodeToString(d.Data)
		}
		uri := "data:" + mediaType + ";base64," + encoded
		if image {
			return map[string]any{"type": "input_image", "image_url": uri}, nil
		}
		if filename == "" {
			filename = "file"
		}
		return map[string]any{"type": "input_file", "filename": filename, "file_data": uri}, nil
	case provider.FileDataRef:
		for _, id := range d.Reference {
			kind := "input_file"
			if image {
				kind = "input_image"
			}
			return map[string]any{"type": kind, "file_id": id}, nil
		}
	}
	return nil, []provider.Warning{provider.Unsupported("file part", "an attachment with no usable data was dropped")}
}
