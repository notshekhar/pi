package session

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/notshekhar/pi/internal/ai/provider"
)

// Message content in loop's stored shape: AI SDK v7 ModelMessage content.
//
// A user prompt is stored as a plain STRING (loop never persists attachments
// or hook context — those ride only the copy sent to the model). Assistant
// and tool content are part arrays: text, reasoning (with providerOptions,
// which carries signed reasoning a resumed session must send back),
// tool-call, and tool-result with a typed output.
//
// Decoding is tolerant because it reads rows loop wrote: an unknown part
// type is skipped rather than failing the whole session. Encoding is exact,
// because loop reads what pi writes.

// wirePart is one AI SDK content part.
type wirePart struct {
	Type             string                   `json:"type"`
	Text             string                   `json:"text,omitempty"`
	ToolCallID       string                   `json:"toolCallId,omitempty"`
	ToolName         string                   `json:"toolName,omitempty"`
	Input            json.RawMessage          `json:"input,omitempty"`
	Output           *wireOutput              `json:"output,omitempty"`
	ProviderExecuted bool                     `json:"providerExecuted,omitempty"`
	Data             json.RawMessage          `json:"data,omitempty"`
	Image            json.RawMessage          `json:"image,omitempty"`
	URL              string                   `json:"url,omitempty"`
	MediaType        string                   `json:"mediaType,omitempty"`
	Filename         string                   `json:"filename,omitempty"`
	ProviderOptions  provider.ProviderOptions `json:"providerOptions,omitempty"`
}

// wireOutput is a tool result's typed output.
type wireOutput struct {
	Type            string                   `json:"type"`
	Value           json.RawMessage          `json:"value,omitempty"`
	Reason          string                   `json:"reason,omitempty"`
	ProviderOptions provider.ProviderOptions `json:"providerOptions,omitempty"`
}

// EncodeContent turns a provider message into its role and stored content.
func EncodeContent(msg provider.Message) (role string, content json.RawMessage, err error) {
	switch m := msg.(type) {
	case provider.UserMessage:
		// Text only → a plain string, which is how loop stores every prompt.
		allText := true
		var text strings.Builder
		for _, p := range m.Content {
			if t, ok := p.(provider.TextPart); ok {
				text.WriteString(t.Text)
			} else {
				allText = false
			}
		}
		if allText {
			raw, err := json.Marshal(text.String())
			return "user", raw, err
		}
		parts := make([]wirePart, 0, len(m.Content))
		for _, p := range m.Content {
			wp, err := encodePart(p)
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, wp)
		}
		raw, err := json.Marshal(parts)
		return "user", raw, err
	case provider.AssistantMessage:
		parts := make([]wirePart, 0, len(m.Content))
		for _, p := range m.Content {
			wp, err := encodePart(p)
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, wp)
		}
		raw, err := json.Marshal(parts)
		return "assistant", raw, err
	case provider.ToolMessage:
		parts := make([]wirePart, 0, len(m.Content))
		for _, p := range m.Content {
			wp, err := encodePart(p)
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, wp)
		}
		raw, err := json.Marshal(parts)
		return "tool", raw, err
	}
	return "", nil, fmt.Errorf("session: cannot store a %T", msg)
}

func encodePart(part any) (wirePart, error) {
	switch p := part.(type) {
	case provider.TextPart:
		return wirePart{Type: "text", Text: p.Text, ProviderOptions: p.ProviderOptions}, nil
	case provider.ReasoningPart:
		return wirePart{Type: "reasoning", Text: p.Text, ProviderOptions: p.ProviderOptions}, nil
	case provider.ToolCallPart:
		input, err := json.Marshal(p.Input)
		if err != nil {
			return wirePart{}, err
		}
		if p.Input == nil {
			input = json.RawMessage("{}")
		}
		return wirePart{Type: "tool-call", ToolCallID: p.ToolCallID, ToolName: p.ToolName, Input: input,
			ProviderExecuted: p.ProviderExecuted, ProviderOptions: p.ProviderOptions}, nil
	case provider.ToolResultPart:
		out, err := encodeOutput(p.Output)
		if err != nil {
			return wirePart{}, err
		}
		return wirePart{Type: "tool-result", ToolCallID: p.ToolCallID, ToolName: p.ToolName, Output: out,
			ProviderOptions: p.ProviderOptions}, nil
	case provider.FilePart:
		wp := wirePart{Type: "file", MediaType: p.MediaType, Filename: p.Filename, ProviderOptions: p.ProviderOptions}
		switch d := p.Data.(type) {
		case provider.FileDataBytes:
			b64 := d.Base64
			if b64 == "" {
				b64 = base64.StdEncoding.EncodeToString(d.Data)
			}
			wp.Data, _ = json.Marshal(b64)
		case provider.FileDataURL:
			if d.URL != nil {
				wp.Data, _ = json.Marshal(d.URL.String())
			}
		case provider.FileDataText:
			wp.Data, _ = json.Marshal(d.Text)
		}
		return wp, nil
	}
	return wirePart{}, fmt.Errorf("session: cannot store a %T part", part)
}

func encodeOutput(o provider.ToolResultOutput) (*wireOutput, error) {
	val := func(v any) json.RawMessage {
		raw, _ := json.Marshal(v)
		return raw
	}
	switch out := o.(type) {
	case nil:
		return &wireOutput{Type: "text", Value: val("")}, nil
	case provider.ToolOutputText:
		return &wireOutput{Type: "text", Value: val(out.Value), ProviderOptions: out.ProviderOptions}, nil
	case provider.ToolOutputErrorText:
		return &wireOutput{Type: "error-text", Value: val(out.Value), ProviderOptions: out.ProviderOptions}, nil
	case provider.ToolOutputJSON:
		return &wireOutput{Type: "json", Value: val(out.Value), ProviderOptions: out.ProviderOptions}, nil
	case provider.ToolOutputErrorJSON:
		return &wireOutput{Type: "error-json", Value: val(out.Value), ProviderOptions: out.ProviderOptions}, nil
	case provider.ToolOutputExecutionDenied:
		return &wireOutput{Type: "execution-denied", Reason: out.Reason, ProviderOptions: out.ProviderOptions}, nil
	case provider.ToolOutputContent:
		var parts []map[string]any
		for _, c := range out.Value {
			switch cp := c.(type) {
			case provider.ToolContentText:
				parts = append(parts, map[string]any{"type": "text", "text": cp.Text})
			case provider.ToolContentFile:
				if b, ok := cp.Data.(provider.FileDataBytes); ok {
					data := b.Base64
					if data == "" {
						data = base64.StdEncoding.EncodeToString(b.Data)
					}
					parts = append(parts, map[string]any{"type": "media", "data": data, "mediaType": cp.MediaType})
				}
			}
		}
		return &wireOutput{Type: "content", Value: val(parts)}, nil
	}
	return nil, fmt.Errorf("session: cannot store tool output %T", o)
}

// DecodeContent rebuilds a provider message from a stored role and content.
// ok is false for content that holds nothing a provider message can carry.
func DecodeContent(role string, raw json.RawMessage) (provider.Message, bool) {
	var str string
	isString := json.Unmarshal(raw, &str) == nil
	var parts []wirePart
	if !isString {
		_ = json.Unmarshal(raw, &parts)
	}
	switch role {
	case "user":
		if isString {
			return provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: str}}}, true
		}
		var m provider.UserMessage
		for _, wp := range parts {
			if p, ok := decodePart(wp).(provider.UserPart); ok {
				m.Content = append(m.Content, p)
			}
		}
		return m, len(m.Content) > 0
	case "assistant":
		if isString {
			if str == "" {
				return provider.AssistantMessage{}, false
			}
			return provider.AssistantMessage{Content: []provider.AssistantPart{provider.TextPart{Text: str}}}, true
		}
		var m provider.AssistantMessage
		for _, wp := range parts {
			if p, ok := decodePart(wp).(provider.AssistantPart); ok {
				m.Content = append(m.Content, p)
			}
		}
		return m, len(m.Content) > 0
	case "tool":
		var m provider.ToolMessage
		for _, wp := range parts {
			if p, ok := decodePart(wp).(provider.ToolPart); ok {
				m.Content = append(m.Content, p)
			}
		}
		return m, len(m.Content) > 0
	}
	return nil, false
}

func decodePart(wp wirePart) any {
	switch wp.Type {
	case "text":
		return provider.TextPart{Text: wp.Text, ProviderOptions: wp.ProviderOptions}
	case "reasoning":
		return provider.ReasoningPart{Text: wp.Text, ProviderOptions: wp.ProviderOptions}
	case "tool-call":
		var input provider.JSONValue
		if len(wp.Input) > 0 {
			_ = json.Unmarshal(wp.Input, &input)
		}
		return provider.ToolCallPart{ToolCallID: wp.ToolCallID, ToolName: wp.ToolName, Input: input,
			ProviderExecuted: wp.ProviderExecuted, ProviderOptions: wp.ProviderOptions}
	case "tool-result":
		return provider.ToolResultPart{ToolCallID: wp.ToolCallID, ToolName: wp.ToolName,
			Output: decodeOutput(wp.Output), ProviderOptions: wp.ProviderOptions}
	case "file", "image":
		data := wp.Data
		if len(data) == 0 {
			data = wp.Image
		}
		var s string
		_ = json.Unmarshal(data, &s)
		if s == "" {
			s = wp.URL
		}
		media := wp.MediaType
		if media == "" && wp.Type == "image" {
			media = "image/*"
		}
		var fd provider.FileData = provider.FileDataBytes{Base64: s}
		if u, err := url.Parse(s); err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "data") {
			fd = provider.FileDataURL{URL: u}
		}
		return provider.FilePart{Data: fd, MediaType: media, Filename: wp.Filename, ProviderOptions: wp.ProviderOptions}
	}
	return nil
}

func decodeOutput(w *wireOutput) provider.ToolResultOutput {
	if w == nil {
		return provider.ToolOutputText{}
	}
	str := func() string {
		var s string
		if json.Unmarshal(w.Value, &s) == nil {
			return s
		}
		return string(w.Value)
	}
	value := func() provider.JSONValue {
		var v provider.JSONValue
		_ = json.Unmarshal(w.Value, &v)
		return v
	}
	switch w.Type {
	case "text":
		return provider.ToolOutputText{Value: str(), ProviderOptions: w.ProviderOptions}
	case "error-text":
		return provider.ToolOutputErrorText{Value: str(), ProviderOptions: w.ProviderOptions}
	case "json":
		return provider.ToolOutputJSON{Value: value(), ProviderOptions: w.ProviderOptions}
	case "error-json":
		return provider.ToolOutputErrorJSON{Value: value(), ProviderOptions: w.ProviderOptions}
	case "execution-denied":
		return provider.ToolOutputExecutionDenied{Reason: w.Reason, ProviderOptions: w.ProviderOptions}
	case "content":
		var items []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(w.Value, &items)
		var out provider.ToolOutputContent
		for _, it := range items {
			if it.Type == "text" {
				out.Value = append(out.Value, provider.ToolContentText{Text: it.Text})
			}
		}
		return out
	}
	return provider.ToolOutputText{Value: str(), ProviderOptions: w.ProviderOptions}
}
