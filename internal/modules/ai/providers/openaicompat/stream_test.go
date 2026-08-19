package openaicompat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/jsonschema"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// newTestProvider serves a canned body and captures the request that produced it.
func newTestProvider(t *testing.T, body string) (*Provider, *[]byte) {
	t.Helper()

	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return New(Options{APIKey: "test", BaseURL: srv.URL}), &sent
}

// collect drains a stream.
func collect(res *provider.StreamResult) []provider.StreamPart {
	var parts []provider.StreamPart
	for p := range res.Stream {
		parts = append(parts, p)
	}
	return parts
}

// userPrompt is a minimal valid prompt.
func userPrompt(text string) provider.Prompt {
	return provider.Prompt{
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: text}}},
	}
}

const textChunks = `data: {"id":"chatcmpl-1","model":"gpt-x","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","model":"gpt-x","choices":[{"index":0,"delta":{"content":", world"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","model":"gpt-x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"chatcmpl-1","model":"gpt-x","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17,"prompt_tokens_details":{"cached_tokens":4}}}

data: [DONE]

`

func TestStreamText(t *testing.T) {
	p, _ := newTestProvider(t, textChunks)

	res, err := p.LanguageModel("gpt-x").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("hi"),
	})
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	var finish *provider.Finish
	for _, part := range collect(res) {
		switch v := part.(type) {
		case provider.TextDelta:
			text.WriteString(v.Delta)
		case provider.Finish:
			finish = &v
		case provider.ErrorPart:
			t.Fatalf("unexpected error: %v", v.Err)
		}
	}

	if text.String() != "Hello, world" {
		t.Errorf("text = %q", text.String())
	}
	if finish.FinishReason.Unified != provider.FinishStop {
		t.Errorf("finish = %q", finish.FinishReason.Unified)
	}
	// prompt_tokens already includes cached tokens here, unlike Anthropic.
	if got := *finish.Usage.InputTokens.Total; got != 12 {
		t.Errorf("input total = %d, want 12", got)
	}
	if got := *finish.Usage.InputTokens.NoCache; got != 8 {
		t.Errorf("uncached input = %d, want 8 (12 - 4 cached)", got)
	}
}

// toolChunks splits one call across fragments the way the API does: the id and
// name arrive first, then the arguments build up.
const toolChunks = `data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"read_file","arguments":""}}]}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\""}}]}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"/tmp/a\"}"}}]}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`

func TestStreamToolCall(t *testing.T) {
	p, _ := newTestProvider(t, toolChunks)

	res, err := p.LanguageModel("gpt-x").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("read it"),
	})
	if err != nil {
		t.Fatal(err)
	}

	var call *provider.ToolCall
	var deltas strings.Builder
	var startName string
	for _, part := range collect(res) {
		switch v := part.(type) {
		case provider.ToolInputStart:
			startName = v.ToolName
		case provider.ToolInputDelta:
			deltas.WriteString(v.Delta)
		case provider.ToolCall:
			call = &v
		}
	}

	if call == nil {
		t.Fatal("no tool call emitted")
	}
	if startName != "read_file" {
		t.Errorf("tool-input-start name = %q", startName)
	}
	if call.ToolCallID != "call_abc" || call.ToolName != "read_file" {
		t.Errorf("call = %+v", call)
	}
	if call.Input != `{"path":"/tmp/a"}` {
		t.Errorf("input = %q", call.Input)
	}
	if deltas.String() != call.Input {
		t.Errorf("deltas %q != input %q", deltas.String(), call.Input)
	}
}

// parallelToolChunks interleaves two calls, which is what makes the index the
// only reliable way to tie fragments together.
const parallelToolChunks = `data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"one","arguments":"{\"v\":"}}]}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"two","arguments":"{\"v\":"}}]}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"2}"}}]}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`

func TestInterleavedParallelToolCalls(t *testing.T) {
	p, _ := newTestProvider(t, parallelToolChunks)

	res, err := p.LanguageModel("gpt-x").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("both"),
	})
	if err != nil {
		t.Fatal(err)
	}

	var calls []provider.ToolCall
	for _, part := range collect(res) {
		if v, ok := part.(provider.ToolCall); ok {
			calls = append(calls, v)
		}
	}

	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	// Fragments arrived out of order, so each call's arguments must have been
	// keyed by index rather than by arrival.
	if calls[0].ToolName != "one" || calls[0].Input != `{"v":1}` {
		t.Errorf("call 0 = %+v", calls[0])
	}
	if calls[1].ToolName != "two" || calls[1].Input != `{"v":2}` {
		t.Errorf("call 1 = %+v", calls[1])
	}
}

const reasoningChunks = `data: {"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":"Let me think"}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":" about it"}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{"content":"The answer"}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`

func TestReasoningContentBecomesReasoningParts(t *testing.T) {
	p, _ := newTestProvider(t, reasoningChunks)

	res, err := p.LanguageModel("deepseek-reasoner").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("think"),
	})
	if err != nil {
		t.Fatal(err)
	}

	var reasoning, text strings.Builder
	var reasoningClosedBeforeText bool
	var sawText bool

	for _, part := range collect(res) {
		switch v := part.(type) {
		case provider.ReasoningDelta:
			reasoning.WriteString(v.Delta)
		case provider.ReasoningEnd:
			if !sawText {
				reasoningClosedBeforeText = true
			}
		case provider.TextDelta:
			sawText = true
			text.WriteString(v.Delta)
		}
	}

	if reasoning.String() != "Let me think about it" {
		t.Errorf("reasoning = %q", reasoning.String())
	}
	if text.String() != "The answer" {
		t.Errorf("text = %q", text.String())
	}
	if !reasoningClosedBeforeText {
		t.Error("the reasoning block should close when content starts")
	}
}

func TestToolResultsBecomeSeparateToolMessages(t *testing.T) {
	p, body := newTestProvider(t, textChunks)

	prompt := provider.Prompt{
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "go"}}},
		provider.AssistantMessage{Content: []provider.AssistantPart{
			provider.ToolCallPart{ToolCallID: "call_a", ToolName: "one", Input: map[string]any{"v": 1}},
			provider.ToolCallPart{ToolCallID: "call_b", ToolName: "two", Input: map[string]any{"v": 2}},
		}},
		provider.ToolMessage{Content: []provider.ToolPart{
			provider.ToolResultPart{ToolCallID: "call_a", ToolName: "one", Output: provider.ToolOutputText{Value: "first"}},
			provider.ToolResultPart{ToolCallID: "call_b", ToolName: "two", Output: provider.ToolOutputText{Value: "second"}},
		}},
	}

	res, err := p.LanguageModel("gpt-x").DoStream(context.Background(), provider.CallOptions{Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	collect(res)

	var sent chatRequest
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}

	// user, assistant, then one tool message per result.
	if len(sent.Messages) != 4 {
		t.Fatalf("messages = %d, want 4: %+v", len(sent.Messages), sent.Messages)
	}
	if sent.Messages[1].Role != "assistant" || len(sent.Messages[1].ToolCalls) != 2 {
		t.Errorf("assistant message = %+v", sent.Messages[1])
	}
	// An assistant turn that is only tool calls must omit content entirely.
	if sent.Messages[1].Content != nil {
		t.Errorf("tool-only assistant content = %v, want omitted", sent.Messages[1].Content)
	}
	for i, want := range []string{"call_a", "call_b"} {
		msg := sent.Messages[2+i]
		if msg.Role != "tool" || msg.ToolCallID != want {
			t.Errorf("message %d = %+v, want a tool message for %s", 2+i, msg, want)
		}
	}
}

func TestMaxCompletionTokensSwitch(t *testing.T) {
	limit := int64(100)

	for _, tc := range []struct {
		name    string
		useNew  bool
		wantOld bool
	}{
		{"legacy max_tokens", false, true},
		{"max_completion_tokens", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sent, _ = io.ReadAll(r.Body)
				io.WriteString(w, textChunks)
			}))
			defer srv.Close()

			p := New(Options{APIKey: "t", BaseURL: srv.URL, UseMaxCompletionTokens: tc.useNew})
			res, err := p.LanguageModel("m").DoStream(context.Background(), provider.CallOptions{
				Prompt:          userPrompt("hi"),
				MaxOutputTokens: &limit,
			})
			if err != nil {
				t.Fatal(err)
			}
			collect(res)

			var req chatRequest
			if err := json.Unmarshal(sent, &req); err != nil {
				t.Fatal(err)
			}
			if tc.wantOld && req.MaxTokens == nil {
				t.Error("expected max_tokens")
			}
			if !tc.wantOld && req.MaxCompletionTokens == nil {
				t.Error("expected max_completion_tokens")
			}
			if tc.wantOld && req.MaxCompletionTokens != nil {
				t.Error("max_completion_tokens should not be sent")
			}
		})
	}
}

func TestProviderExtrasAreMergedAtTopLevel(t *testing.T) {
	p, body := newTestProvider(t, textChunks)

	res, err := p.LanguageModel("m").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("hi"),
		ProviderOptions: provider.ProviderOptions{
			"openai-compatible": {"logit_bias": map[string]any{"50256": -100}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(res)

	var raw map[string]any
	if err := json.Unmarshal(*body, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["logit_bias"]; !ok {
		t.Errorf("extras should be merged as siblings of the standard fields, got %v", raw)
	}
}

func TestStructuredOutputUsesJSONSchema(t *testing.T) {
	p, body := newTestProvider(t, textChunks)

	properties := jsonschema.NewProperties()
	properties.Set("city", &jsonschema.Schema{Type: jsonschema.String})

	res, err := p.LanguageModel("m").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("where?"),
		ResponseFormat: &provider.ResponseFormat{
			Type:   "json",
			Schema: &jsonschema.Schema{Type: jsonschema.Object, Properties: properties},
			Name:   "place",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(res)

	var raw struct {
		ResponseFormat struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string         `json:"name"`
				Schema map[string]any `json:"schema"`
				Strict bool           `json:"strict"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	if err := json.Unmarshal(*body, &raw); err != nil {
		t.Fatal(err)
	}

	if raw.ResponseFormat.Type != "json_schema" {
		t.Fatalf("response_format type = %q, want json_schema", raw.ResponseFormat.Type)
	}
	if raw.ResponseFormat.JSONSchema.Name != "place" {
		t.Errorf("schema name = %q, want place", raw.ResponseFormat.JSONSchema.Name)
	}
	if !raw.ResponseFormat.JSONSchema.Strict {
		t.Error("strict should be set so the shape is enforced")
	}
	// $schema is a validator keyword; some gateways reject unknown keys.
	if _, ok := raw.ResponseFormat.JSONSchema.Schema["$schema"]; ok {
		t.Error("$schema should be stripped from the wire schema")
	}
}

func TestStructuredOutputFallsBackToPromptWhenSchemaUnsupported(t *testing.T) {
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, textChunks)
	}))
	t.Cleanup(srv.Close)

	// DeepSeek's shape: json_object exists, json_schema returns a 400.
	p := New(Options{APIKey: "test", BaseURL: srv.URL, DisableJSONSchema: true})

	properties := jsonschema.NewProperties()
	properties.Set("city", &jsonschema.Schema{Type: jsonschema.String})

	res, err := p.LanguageModel("m").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("where?"),
		ResponseFormat: &provider.ResponseFormat{
			Type:   "json",
			Schema: &jsonschema.Schema{Type: jsonschema.Object, Properties: properties},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var warnings []provider.Warning
	for _, part := range collect(res) {
		if start, ok := part.(provider.StreamStart); ok {
			warnings = append(warnings, start.Warnings...)
		}
	}
	// The fallback does not enforce the shape, so the caller has to be told.
	if len(warnings) == 0 {
		t.Error("the prompt fallback produced no warning")
	}

	var raw struct {
		ResponseFormat map[string]any `json:"response_format"`
		Messages       []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(sent, &raw); err != nil {
		t.Fatal(err)
	}

	if raw.ResponseFormat["type"] != "json_object" {
		t.Errorf("response_format = %v, want json_object", raw.ResponseFormat)
	}

	last := raw.Messages[len(raw.Messages)-1]
	if last.Role != "system" || !strings.Contains(last.Content, `"city"`) {
		t.Errorf("last message = %+v, want a trailing system message carrying the schema", last)
	}
}

func TestEmbeddingsAreOrderedByReportedIndex(t *testing.T) {
	// The API does not promise the data array is in input order, and a
	// reordered batch would misalign every vector against its text.
	body := `{"data":[
		{"index":2,"embedding":[0.3]},
		{"index":0,"embedding":[0.1]},
		{"index":1,"embedding":[0.2]}
	],"usage":{"prompt_tokens":9,"total_tokens":9}}`

	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	p := New(Options{APIKey: "test", BaseURL: srv.URL})

	res, err := p.EmbeddingModel("text-embedding-3-small").DoEmbed(context.Background(),
		provider.EmbeddingCallOptions{
			Values:     []string{"a", "b", "c"},
			Dimensions: 256,
		})
	if err != nil {
		t.Fatal(err)
	}

	want := []float32{0.1, 0.2, 0.3}
	for i, e := range res.Embeddings {
		if len(e) != 1 || e[0] != want[i] {
			t.Fatalf("embedding %d = %v, want %v: results were not sorted by index", i, e, want[i])
		}
	}
	if res.Usage.Tokens == nil || *res.Usage.Tokens != 9 {
		t.Errorf("usage = %v, want 9", res.Usage.Tokens)
	}

	var req struct {
		Model      string   `json:"model"`
		Input      []string `json:"input"`
		Dimensions *int     `json:"dimensions"`
	}
	if err := json.Unmarshal(sent, &req); err != nil {
		t.Fatal(err)
	}
	if req.Dimensions == nil || *req.Dimensions != 256 {
		t.Errorf("dimensions = %v, want 256 passed through", req.Dimensions)
	}
	if len(req.Input) != 3 {
		t.Errorf("input = %v, want all three values in one call", req.Input)
	}
}

func TestImageGenerationRequestAndWarnings(t *testing.T) {
	// PNG magic bytes, base64-encoded, so the media type can be sniffed.
	png := "iVBORw0KGgoAAAANSUhEUg=="
	body := `{"data":[{"b64_json":"` + png + `"},{"url":"https://example.com/b.png"}]}`

	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	p := New(Options{APIKey: "test", BaseURL: srv.URL})

	seed := int64(7)
	res, err := p.ImageModel("gpt-image-1").DoGenerate(context.Background(), provider.ImageCallOptions{
		Prompt: "a lighthouse",
		N:      2,
		Size:   "1024x1024",
		// Neither is supported here, so both must be reported rather than
		// quietly dropped.
		AspectRatio: "16:9",
		Seed:        &seed,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Images) != 2 {
		t.Fatalf("images = %d, want 2", len(res.Images))
	}
	if res.Images[0].MediaType != "image/png" {
		t.Errorf("media type = %q, want image/png sniffed from the data", res.Images[0].MediaType)
	}
	if res.Images[1].URL != "https://example.com/b.png" {
		t.Errorf("second image = %+v, want the URL preserved", res.Images[1])
	}
	// A URL cannot be sniffed, so it is left unlabelled rather than guessed.
	if res.Images[1].MediaType != "" {
		t.Errorf("URL image media type = %q, want empty", res.Images[1].MediaType)
	}

	var sawAspect, sawSeed bool
	for _, w := range res.Warnings {
		switch w.Feature {
		case "aspectRatio":
			sawAspect = true
		case "seed":
			sawSeed = true
		}
	}
	if !sawAspect || !sawSeed {
		t.Errorf("warnings = %+v, want both unsupported settings reported", res.Warnings)
	}

	var req struct {
		Model          string `json:"model"`
		Prompt         string `json:"prompt"`
		N              int    `json:"n"`
		Size           string `json:"size"`
		ResponseFormat string `json:"response_format"`
	}
	if err := json.Unmarshal(sent, &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != "gpt-image-1" || req.Prompt != "a lighthouse" || req.N != 2 || req.Size != "1024x1024" {
		t.Errorf("request = %+v", req)
	}
	// gpt-image-1 rejects response_format outright, so it is only sent when a
	// gateway was configured to need it.
	if req.ResponseFormat != "" {
		t.Errorf("response_format = %q, want it omitted by default", req.ResponseFormat)
	}
}
