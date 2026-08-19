package session

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// Wire format for conversation history.
//
// Provider messages are trees of interface-typed parts, so `encoding/json`
// cannot round-trip them on its own: it will happily marshal a ToolCallPart
// and then have no idea what to unmarshal it back into. Every interface gets
// an explicit type tag here, and decoding switches on it.
//
// `ProviderOptions` is carried on every part, and that is not optional
// polish: Anthropic and OpenAI round-trip SIGNED reasoning through it, and a
// resumed session that dropped the signature is rejected by the API.
//
// Unsupported parts are an ERROR, never a silent drop. A conversation missing
// one part of a tool-call/result pair is malformed, and the provider rejects
// the whole request — far better to refuse to save than to write a session
// that cannot be resumed.

type wireMessage struct {
	Role    string                   `json:"role"`
	Content []wirePart               `json:"content,omitempty"`
	Text    string                   `json:"text,omitempty"` // system messages
	Options provider.ProviderOptions `json:"options,omitempty"`
}

type wirePart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	MediaType string        `json:"mediaType,omitempty"`
	Filename  string        `json:"filename,omitempty"`
	Data      *wireFileData `json:"data,omitempty"`

	ToolCallID       string          `json:"toolCallId,omitempty"`
	ToolName         string          `json:"toolName,omitempty"`
	Input            json.RawMessage `json:"input,omitempty"`
	ProviderExecuted bool            `json:"providerExecuted,omitempty"`

	Output *wireOutput `json:"output,omitempty"`

	Options provider.ProviderOptions `json:"options,omitempty"`
}

type wireFileData struct {
	Type   string `json:"type"`
	Bytes  []byte `json:"bytes,omitempty"`
	Base64 string `json:"base64,omitempty"`
	URL    string `json:"url,omitempty"`
	Text   string `json:"text,omitempty"`
}

type wireOutput struct {
	Type   string                   `json:"type"`
	Value  json.RawMessage          `json:"value,omitempty"`
	Reason string                   `json:"reason,omitempty"`
	Option provider.ProviderOptions `json:"options,omitempty"`
}

// encodeMessage converts a provider message to its wire form.
func encodeMessage(msg provider.Message) (wireMessage, error) {
	switch m := msg.(type) {
	case provider.SystemMessage:
		return wireMessage{Role: "system", Text: m.Content, Options: m.ProviderOptions}, nil

	case provider.UserMessage:
		w := wireMessage{Role: "user", Options: m.ProviderOptions}
		for _, part := range m.Content {
			p, err := encodePart(part)
			if err != nil {
				return w, err
			}
			w.Content = append(w.Content, p)
		}
		return w, nil

	case provider.AssistantMessage:
		w := wireMessage{Role: "assistant", Options: m.ProviderOptions}
		for _, part := range m.Content {
			p, err := encodePart(part)
			if err != nil {
				return w, err
			}
			w.Content = append(w.Content, p)
		}
		return w, nil

	case provider.ToolMessage:
		w := wireMessage{Role: "tool", Options: m.ProviderOptions}
		for _, part := range m.Content {
			p, err := encodePart(part)
			if err != nil {
				return w, err
			}
			w.Content = append(w.Content, p)
		}
		return w, nil
	}
	return wireMessage{}, fmt.Errorf("session: cannot encode message of type %T", msg)
}

func encodePart(part any) (wirePart, error) {
	switch p := part.(type) {
	case provider.TextPart:
		return wirePart{Type: "text", Text: p.Text, Options: p.ProviderOptions}, nil

	case provider.ReasoningPart:
		return wirePart{Type: "reasoning", Text: p.Text, Options: p.ProviderOptions}, nil

	case provider.FilePart:
		data, err := encodeFileData(p.Data)
		if err != nil {
			return wirePart{}, err
		}
		return wirePart{
			Type: "file", MediaType: p.MediaType, Filename: p.Filename,
			Data: data, Options: p.ProviderOptions,
		}, nil

	case provider.ToolCallPart:
		input, err := json.Marshal(p.Input)
		if err != nil {
			return wirePart{}, fmt.Errorf("session: tool call input: %w", err)
		}
		return wirePart{
			Type: "tool-call", ToolCallID: p.ToolCallID, ToolName: p.ToolName,
			Input: input, ProviderExecuted: p.ProviderExecuted, Options: p.ProviderOptions,
		}, nil

	case provider.ToolResultPart:
		out, err := encodeOutput(p.Output)
		if err != nil {
			return wirePart{}, err
		}
		return wirePart{
			Type: "tool-result", ToolCallID: p.ToolCallID, ToolName: p.ToolName,
			Output: out, Options: p.ProviderOptions,
		}, nil
	}
	return wirePart{}, fmt.Errorf("session: cannot encode part of type %T", part)
}

func encodeFileData(d provider.FileData) (*wireFileData, error) {
	switch f := d.(type) {
	case nil:
		return nil, nil
	case provider.FileDataBytes:
		return &wireFileData{Type: "bytes", Bytes: f.Data, Base64: f.Base64}, nil
	case provider.FileDataURL:
		u := ""
		if f.URL != nil {
			u = f.URL.String()
		}
		return &wireFileData{Type: "url", URL: u}, nil
	case provider.FileDataText:
		return &wireFileData{Type: "text", Text: f.Text}, nil
	}
	return nil, fmt.Errorf("session: cannot encode file data of type %T", d)
}

func encodeOutput(o provider.ToolResultOutput) (*wireOutput, error) {
	marshal := func(kind string, v any, opts provider.ProviderOptions) (*wireOutput, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("session: tool output: %w", err)
		}
		return &wireOutput{Type: kind, Value: raw, Option: opts}, nil
	}
	switch out := o.(type) {
	case nil:
		return nil, nil
	case provider.ToolOutputText:
		return marshal("text", out.Value, out.ProviderOptions)
	case provider.ToolOutputErrorText:
		return marshal("error-text", out.Value, out.ProviderOptions)
	case provider.ToolOutputJSON:
		return marshal("json", out.Value, out.ProviderOptions)
	case provider.ToolOutputErrorJSON:
		return marshal("error-json", out.Value, out.ProviderOptions)
	case provider.ToolOutputExecutionDenied:
		return &wireOutput{Type: "denied", Reason: out.Reason, Option: out.ProviderOptions}, nil
	}
	return nil, fmt.Errorf("session: cannot encode tool output of type %T", o)
}

// decodeMessage rebuilds a provider message from its wire form.
func decodeMessage(w wireMessage) (provider.Message, error) {
	switch w.Role {
	case "system":
		return provider.SystemMessage{Content: w.Text, ProviderOptions: w.Options}, nil

	case "user":
		m := provider.UserMessage{ProviderOptions: w.Options}
		for _, wp := range w.Content {
			p, err := decodePart(wp)
			if err != nil {
				return nil, err
			}
			up, ok := p.(provider.UserPart)
			if !ok {
				return nil, fmt.Errorf("session: %q is not valid in a user message", wp.Type)
			}
			m.Content = append(m.Content, up)
		}
		return m, nil

	case "assistant":
		m := provider.AssistantMessage{ProviderOptions: w.Options}
		for _, wp := range w.Content {
			p, err := decodePart(wp)
			if err != nil {
				return nil, err
			}
			ap, ok := p.(provider.AssistantPart)
			if !ok {
				return nil, fmt.Errorf("session: %q is not valid in an assistant message", wp.Type)
			}
			m.Content = append(m.Content, ap)
		}
		return m, nil

	case "tool":
		m := provider.ToolMessage{ProviderOptions: w.Options}
		for _, wp := range w.Content {
			p, err := decodePart(wp)
			if err != nil {
				return nil, err
			}
			tp, ok := p.(provider.ToolPart)
			if !ok {
				return nil, fmt.Errorf("session: %q is not valid in a tool message", wp.Type)
			}
			m.Content = append(m.Content, tp)
		}
		return m, nil
	}
	return nil, fmt.Errorf("session: unknown message role %q", w.Role)
}

func decodePart(w wirePart) (any, error) {
	switch w.Type {
	case "text":
		return provider.TextPart{Text: w.Text, ProviderOptions: w.Options}, nil

	case "reasoning":
		return provider.ReasoningPart{Text: w.Text, ProviderOptions: w.Options}, nil

	case "file":
		data, err := decodeFileData(w.Data)
		if err != nil {
			return nil, err
		}
		return provider.FilePart{
			Data: data, MediaType: w.MediaType, Filename: w.Filename,
			ProviderOptions: w.Options,
		}, nil

	case "tool-call":
		var input provider.JSONValue
		if len(w.Input) > 0 {
			if err := json.Unmarshal(w.Input, &input); err != nil {
				return nil, fmt.Errorf("session: tool call input: %w", err)
			}
		}
		return provider.ToolCallPart{
			ToolCallID: w.ToolCallID, ToolName: w.ToolName, Input: input,
			ProviderExecuted: w.ProviderExecuted, ProviderOptions: w.Options,
		}, nil

	case "tool-result":
		out, err := decodeOutput(w.Output)
		if err != nil {
			return nil, err
		}
		return provider.ToolResultPart{
			ToolCallID: w.ToolCallID, ToolName: w.ToolName, Output: out,
			ProviderOptions: w.Options,
		}, nil
	}
	return nil, fmt.Errorf("session: unknown part type %q", w.Type)
}

func decodeFileData(w *wireFileData) (provider.FileData, error) {
	if w == nil {
		return nil, nil
	}
	switch w.Type {
	case "bytes":
		return provider.FileDataBytes{Data: w.Bytes, Base64: w.Base64}, nil
	case "url":
		u, err := url.Parse(w.URL)
		if err != nil {
			return nil, fmt.Errorf("session: file url: %w", err)
		}
		return provider.FileDataURL{URL: u}, nil
	case "text":
		return provider.FileDataText{Text: w.Text}, nil
	}
	return nil, fmt.Errorf("session: unknown file data type %q", w.Type)
}

func decodeOutput(w *wireOutput) (provider.ToolResultOutput, error) {
	if w == nil {
		return nil, nil
	}
	text := func() (string, error) {
		var s string
		if len(w.Value) == 0 {
			return "", nil
		}
		err := json.Unmarshal(w.Value, &s)
		return s, err
	}
	value := func() (provider.JSONValue, error) {
		var v provider.JSONValue
		if len(w.Value) == 0 {
			return nil, nil
		}
		err := json.Unmarshal(w.Value, &v)
		return v, err
	}

	switch w.Type {
	case "text":
		s, err := text()
		return provider.ToolOutputText{Value: s, ProviderOptions: w.Option}, err
	case "error-text":
		s, err := text()
		return provider.ToolOutputErrorText{Value: s, ProviderOptions: w.Option}, err
	case "json":
		v, err := value()
		return provider.ToolOutputJSON{Value: v, ProviderOptions: w.Option}, err
	case "error-json":
		v, err := value()
		return provider.ToolOutputErrorJSON{Value: v, ProviderOptions: w.Option}, err
	case "denied":
		return provider.ToolOutputExecutionDenied{Reason: w.Reason, ProviderOptions: w.Option}, nil
	}
	return nil, fmt.Errorf("session: unknown tool output type %q", w.Type)
}
