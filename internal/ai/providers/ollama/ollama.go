// Package ollama implements the pi language model spec against Ollama's
// native chat API (POST /api/chat), as loop's ollama-ai-provider-v2 does.
//
// The native API rather than Ollama's OpenAI-compatible /v1: only it takes
// options.num_ctx, and without that Ollama runs every model at its small
// default context and silently truncates a long agent session.
package ollama

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/ai/providerutil"
)

const providerID = "ollama"

// DefaultBaseURL is the local daemon's API root.
const DefaultBaseURL = "http://127.0.0.1:11434/api"

// Options configures a Provider.
type Options struct {
	// BaseURL is the API root, ending in /api.
	BaseURL    string
	Headers    provider.Headers
	HTTPClient *http.Client
	Retry      providerutil.RetryConfig
}

// Provider creates Ollama models.
type Provider struct {
	client *providerutil.Client
	retry  providerutil.RetryConfig
}

// New returns a Provider.
func New(opts Options) *Provider {
	base := opts.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	retry := opts.Retry
	if retry == (providerutil.RetryConfig{}) {
		retry = providerutil.DefaultRetryConfig
	}
	headers := opts.Headers
	return &Provider{retry: retry, client: &providerutil.Client{
		BaseURL: providerutil.TrimTrailingSlash(base), HTTPClient: opts.HTTPClient,
		Headers: func(context.Context) (provider.Headers, error) { return headers, nil },
	}}
}

// LanguageModel returns a model handle.
func (p *Provider) LanguageModel(modelID string) provider.LanguageModel {
	return &model{id: modelID, p: p}
}

type model struct {
	id string
	p  *Provider
}

func (m *model) SpecificationVersion() string { return provider.SpecificationVersion }
func (m *model) Provider() string             { return providerID }
func (m *model) ModelID() string              { return m.id }

var httpURL = regexp.MustCompile(`^https?://.*`)

func (m *model) SupportedURLs(context.Context) (map[string][]*regexp.Regexp, error) {
	return map[string][]*regexp.Regexp{"image/*": {httpURL}}, nil
}

// buildRequest is the provider's buildBaseArgs + prepareResponsesTools.
func (m *model) buildRequest(opts provider.CallOptions) (map[string]any, []provider.Warning, error) {
	var warnings []provider.Warning
	messages, err := convertPrompt(opts.Prompt)
	if err != nil {
		return nil, nil, err
	}
	o := opts.ProviderOptions.Get(providerID)
	think, _ := o["think"].(bool)
	req := map[string]any{"model": m.id, "messages": messages, "stream": true, "think": think}
	if v, ok := o["options"]; ok {
		req["options"] = v
	}
	if v, ok := o["keepAlive"]; ok {
		req["keep_alive"] = v
	}
	if opts.Temperature != nil {
		req["temperature"] = *opts.Temperature
	}
	if opts.TopP != nil {
		req["top_p"] = *opts.TopP
	}
	if opts.MaxOutputTokens != nil {
		req["max_output_tokens"] = *opts.MaxOutputTokens
	}
	if rf := opts.ResponseFormat; rf != nil && rf.Type == "json" {
		if rf.Schema != nil {
			req["format"] = rf.Schema
		} else {
			req["format"] = "json"
		}
	}
	var tools []any
	for _, tool := range opts.Tools {
		t, ok := tool.(provider.FunctionTool)
		if !ok {
			warnings = append(warnings, provider.Unsupported("tool", "provider-executed tools are not supported"))
			continue
		}
		params := map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}}
		if t.InputSchema != nil {
			raw, err := json.Marshal(t.InputSchema)
			if err != nil {
				return nil, nil, err
			}
			params = map[string]any{}
			_ = json.Unmarshal(raw, &params)
			delete(params, "$schema")
			if props, _ := params["properties"].(map[string]any); params["type"] == "object" && len(props) == 0 {
				params["properties"], params["required"] = map[string]any{}, []any{}
			}
		}
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
			"name": t.Name, "description": t.Description, "parameters": params}})
	}
	if len(tools) > 0 {
		req["tools"] = tools
		if tc := opts.ToolChoice; tc != nil {
			switch tc.Type {
			case provider.ToolChoiceAuto, provider.ToolChoiceNone, provider.ToolChoiceRequired:
				req["tool_choice"] = string(tc.Type)
			case provider.ToolChoiceTool:
				req["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": tc.ToolName}}
			}
		}
	}
	return req, warnings, nil
}

// convertPrompt is the provider's convertToOllamaChatMessages.
func convertPrompt(prompt provider.Prompt) ([]any, error) {
	var out []any
	for _, msg := range prompt {
		switch m := msg.(type) {
		case provider.SystemMessage:
			out = append(out, map[string]any{"role": "system", "content": m.Content})
		case provider.UserMessage:
			if len(m.Content) == 1 {
				if t, ok := m.Content[0].(provider.TextPart); ok {
					out = append(out, map[string]any{"role": "user", "content": t.Text})
					continue
				}
			}
			var text strings.Builder
			var images []string
			for _, part := range m.Content {
				switch p := part.(type) {
				case provider.TextPart:
					text.WriteString(p.Text)
				case provider.FilePart:
					if strings.HasPrefix(p.MediaType, "image/") {
						if d, ok := p.Data.(provider.FileDataBytes); ok {
							enc := d.Base64
							if enc == "" {
								enc = base64.StdEncoding.EncodeToString(d.Data)
							}
							images = append(images, enc)
						} else if u, ok := p.Data.(provider.FileDataURL); ok {
							images = append(images, u.URL.String())
						}
					}
				}
			}
			item := map[string]any{"role": "user", "content": text.String()}
			if len(images) > 0 {
				item["images"] = images
			}
			out = append(out, item)
		case provider.AssistantMessage:
			var text, thinking strings.Builder
			var calls []any
			for _, part := range m.Content {
				switch p := part.(type) {
				case provider.TextPart:
					text.WriteString(p.Text)
				case provider.ReasoningPart:
					thinking.WriteString(p.Text)
				case provider.ToolCallPart:
					calls = append(calls, map[string]any{"id": p.ToolCallID, "type": "function",
						"function": map[string]any{"name": p.ToolName, "arguments": p.Input}})
				}
			}
			item := map[string]any{"role": "assistant", "content": text.String()}
			if thinking.Len() > 0 {
				item["thinking"] = thinking.String()
			}
			if len(calls) > 0 {
				item["tool_calls"] = calls
			}
			out = append(out, item)
		case provider.ToolMessage:
			for _, part := range m.Content {
				p, ok := part.(provider.ToolResultPart)
				if !ok {
					continue
				}
				var content string
				switch o := p.Output.(type) {
				case provider.ToolOutputText:
					content = o.Value
				case provider.ToolOutputErrorText:
					content = o.Value
				case provider.ToolOutputExecutionDenied:
					content = o.Reason
				case provider.ToolOutputJSON:
					raw, _ := json.Marshal(o.Value)
					content = string(raw)
				case provider.ToolOutputErrorJSON:
					raw, _ := json.Marshal(o.Value)
					content = string(raw)
				case provider.ToolOutputContent:
					raw, _ := json.Marshal(o.Value)
					content = string(raw)
				}
				out = append(out, map[string]any{"role": "tool", "tool_call_id": p.ToolCallID, "content": content})
			}
		default:
			return nil, &provider.InvalidPromptError{Message: fmt.Sprintf("unsupported message type %T", msg)}
		}
	}
	return out, nil
}

// chunk is one line of the NDJSON stream.
type chunk struct {
	Model   string `json:"model"`
	Message *struct {
		Content   string `json:"content"`
		Thinking  string `json:"thinking"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int64  `json:"prompt_eval_count"`
	EvalCount       int64  `json:"eval_count"`
	Error           string `json:"error"`
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// DoStream implements provider.LanguageModel.
func (m *model) DoStream(ctx context.Context, opts provider.CallOptions) (*provider.StreamResult, error) {
	req, warnings, err := m.buildRequest(opts)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := providerutil.Retry(ctx, m.p.retry, func(ctx context.Context) (io.ReadCloser, error) {
		r, err := m.p.client.PostJSON(ctx, "/chat", json.RawMessage(body), opts.Headers)
		if err != nil {
			return nil, err
		}
		return r.Body, nil
	})
	if err != nil {
		return nil, err
	}
	out := make(chan provider.StreamPart, 64)
	go func() {
		defer close(out)
		run(ctx, resp, out, warnings)
	}()
	return &provider.StreamResult{Stream: out, Request: &provider.RequestInfo{Body: string(body)}}, nil
}

// run is the provider's OllamaStreamProcessor.
func run(ctx context.Context, body io.ReadCloser, out chan<- provider.StreamPart, warnings []provider.Warning) {
	defer body.Close()
	var end providerutil.StreamEnd
	emit := func(p provider.StreamPart) {
		end.Observe(p)
		select {
		case out <- p:
		case <-ctx.Done():
		}
	}
	emit(provider.StreamStart{Warnings: warnings})
	const textID, reasoningID = "text-0", "reasoning-0"
	var (
		textOpen, reasoningOpen, first, done, toolCalls bool
		finish                                          = provider.FinishReason{Unified: provider.FinishOther}
		usage                                           provider.Usage
	)
	first = true
	closeBlocks := func() {
		if textOpen {
			textOpen = false
			emit(provider.TextEnd{ID: textID})
		}
		if reasoningOpen {
			reasoningOpen = false
			emit(provider.ReasoningEnd{ID: reasoningID})
		}
	}
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() && ctx.Err() == nil {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c chunk
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			emit(provider.ErrorPart{Err: &provider.InvalidResponseError{Message: "could not decode stream chunk", Response: line, Cause: err}})
			continue
		}
		if c.Error != "" {
			finish = provider.FinishReason{Unified: provider.FinishError}
			emit(provider.ErrorPart{Err: &provider.APICallError{Message: c.Error}})
			continue
		}
		if first {
			first = false
			emit(provider.ResponseMetadataPart{ResponseMetadata: provider.ResponseMetadata{ModelID: c.Model}})
		}
		if c.Done {
			done = true
			finish = mapFinish(c.DoneReason)
			in, outTok := c.PromptEvalCount, c.EvalCount
			usage = provider.Usage{InputTokens: provider.InputTokens{Total: &in}, OutputTokens: provider.OutputTokens{Total: &outTok}}
			closeBlocks()
		}
		if c.Message == nil {
			continue
		}
		if c.Message.Content != "" {
			if !textOpen {
				textOpen = true
				emit(provider.TextStart{ID: textID})
			}
			emit(provider.TextDelta{ID: textID, Delta: c.Message.Content})
		}
		if c.Message.Thinking != "" {
			if !reasoningOpen {
				reasoningOpen = true
				emit(provider.ReasoningStart{ID: reasoningID})
			}
			emit(provider.ReasoningDelta{ID: reasoningID, Delta: c.Message.Thinking})
		}
		for _, call := range c.Message.ToolCalls {
			if call.Function.Name == "" {
				emit(provider.ErrorPart{Err: &provider.InvalidResponseError{Message: "expected 'function.name' to be a string"}})
				continue
			}
			id := call.ID
			if id == "" {
				id = newID()
			}
			args := string(call.Function.Arguments)
			if args == "" || args == "null" {
				args = "{}"
			}
			emit(provider.ToolInputStart{ID: id, ToolName: call.Function.Name})
			emit(provider.ToolInputDelta{ID: id, Delta: args})
			emit(provider.ToolInputEnd{ID: id})
			emit(provider.ToolCall{ToolCallID: id, ToolName: call.Function.Name, Input: args})
			toolCalls = true
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
		emit(provider.ErrorPart{Err: fmt.Errorf("ollama: reading stream: %w", err)})
	}
	closeBlocks()
	if toolCalls && finish.Unified != provider.FinishToolCalls {
		finish = provider.FinishReason{Unified: provider.FinishToolCalls, Raw: "tool_calls"}
	}
	if ctx.Err() == nil {
		reason, err := end.Reason(done, finish)
		if err != nil {
			emit(provider.ErrorPart{Err: fmt.Errorf("ollama: %w", err)})
		}
		finish = reason
	}
	emit(provider.Finish{Usage: usage, FinishReason: finish})
}

func mapFinish(reason string) provider.FinishReason {
	switch reason {
	case "stop":
		return provider.FinishReason{Unified: provider.FinishStop, Raw: reason}
	case "length":
		return provider.FinishReason{Unified: provider.FinishLength, Raw: reason}
	case "content_filter":
		return provider.FinishReason{Unified: provider.FinishContentFilter, Raw: reason}
	}
	return provider.FinishReason{Unified: provider.FinishOther, Raw: reason}
}

// DoGenerate folds the stream.
func (m *model) DoGenerate(ctx context.Context, opts provider.CallOptions) (*provider.GenerateResult, error) {
	res, err := m.DoStream(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := &provider.GenerateResult{Request: res.Request, Response: &provider.ResponseInfo{}}
	var text, reasoning strings.Builder
	var streamErr error
	for part := range res.Stream {
		switch p := part.(type) {
		case provider.StreamStart:
			out.Warnings = p.Warnings
		case provider.TextDelta:
			text.WriteString(p.Delta)
		case provider.ReasoningDelta:
			reasoning.WriteString(p.Delta)
		case provider.ToolCall:
			out.Content = append(out.Content, p)
		case provider.Finish:
			out.Usage, out.FinishReason = p.Usage, p.FinishReason
		case provider.ErrorPart:
			if streamErr == nil {
				streamErr = p.Err
			}
		}
	}
	if streamErr != nil {
		return nil, streamErr
	}
	if reasoning.Len() > 0 {
		out.Content = append([]provider.Content{provider.Reasoning{Text: reasoning.String()}}, out.Content...)
	}
	if text.Len() > 0 {
		out.Content = append(out.Content, provider.Text{Text: text.String()})
	}
	return out, nil
}
