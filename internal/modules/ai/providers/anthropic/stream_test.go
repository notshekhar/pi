package anthropic

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

// newTestProvider serves a fixed body and captures the request that produced it.
func newTestProvider(t *testing.T, handler http.HandlerFunc) (*Provider, *http.Request, *[]byte) {
	t.Helper()

	var captured *http.Request
	var body []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		captured = r
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	p := New(Options{APIKey: "test-key", BaseURL: srv.URL})
	// Return pointers so the caller sees values set during the request.
	return p, captured, &body
}

// sseHandler writes a canned SSE stream.
func sseHandler(events string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, events)
	}
}

// collect drains a stream into a slice.
func collect(t *testing.T, res *provider.StreamResult) []provider.StreamPart {
	t.Helper()
	var parts []provider.StreamPart
	for p := range res.Stream {
		parts = append(parts, p)
	}
	return parts
}

const textStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-opus-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":5,"cache_creation_input_tokens":2}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":", world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":0,"output_tokens":7}}

event: message_stop
data: {"type":"message_stop"}

`

func TestStreamText(t *testing.T) {
	p, _, _ := newTestProvider(t, sseHandler(textStream))
	model := p.LanguageModel("claude-opus-5")

	res, err := model.DoStream(context.Background(), provider.CallOptions{
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	var finish *provider.Finish
	var sawStart, sawEnd bool

	for _, part := range collect(t, res) {
		switch v := part.(type) {
		case provider.TextStart:
			sawStart = true
		case provider.TextDelta:
			text.WriteString(v.Delta)
		case provider.TextEnd:
			sawEnd = true
		case provider.Finish:
			finish = &v
		case provider.ErrorPart:
			t.Fatalf("unexpected error part: %v", v.Err)
		}
	}

	if got := text.String(); got != "Hello, world" {
		t.Errorf("text = %q, want %q", got, "Hello, world")
	}
	if !sawStart || !sawEnd {
		t.Errorf("missing text block delimiters: start=%v end=%v", sawStart, sawEnd)
	}
	if finish == nil {
		t.Fatal("no finish part")
	}
	if finish.FinishReason.Unified != provider.FinishStop {
		t.Errorf("finish reason = %q, want stop", finish.FinishReason.Unified)
	}

	// input_tokens excludes cached tokens, so the total must add all three.
	if got := *finish.Usage.InputTokens.Total; got != 17 {
		t.Errorf("input total = %d, want 17 (10 fresh + 5 read + 2 write)", got)
	}
	if got := *finish.Usage.OutputTokens.Total; got != 7 {
		t.Errorf("output total = %d, want 7", got)
	}
}

const toolStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_02","type":"message","role":"assistant","model":"claude-opus-5","content":[],"usage":{"input_tokens":20,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"read_file","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":\"/tmp/a.txt\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":0,"output_tokens":30}}

event: message_stop
data: {"type":"message_stop"}

`

// jsonToolStream is what structured output looks like on the wire: the model
// answers by calling the injected schema tool.
const jsonToolStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_03","type":"message","role":"assistant","model":"claude-opus-5","content":[],"usage":{"input_tokens":20,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_02","name":"json","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":\"Pune\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":0,"output_tokens":12}}

event: message_stop
data: {"type":"message_stop"}

`

func TestStreamToolCall(t *testing.T) {
	p, _, _ := newTestProvider(t, sseHandler(toolStream))
	model := p.LanguageModel("claude-opus-5")

	res, err := model.DoStream(context.Background(), provider.CallOptions{
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "read a file"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var call *provider.ToolCall
	var deltas strings.Builder
	var finish *provider.Finish

	for _, part := range collect(t, res) {
		switch v := part.(type) {
		case provider.ToolInputDelta:
			deltas.WriteString(v.Delta)
		case provider.ToolCall:
			call = &v
		case provider.Finish:
			finish = &v
		}
	}

	if call == nil {
		t.Fatal("no tool call emitted")
	}
	if call.ToolCallID != "toolu_01" || call.ToolName != "read_file" {
		t.Errorf("tool call = %+v", call)
	}
	// The concatenated deltas must equal the final input exactly, or a UI
	// rendering them live would disagree with the executed call.
	if deltas.String() != call.Input {
		t.Errorf("deltas %q != input %q", deltas.String(), call.Input)
	}

	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
		t.Fatalf("input is not valid json: %v", err)
	}
	if args.Path != "/tmp/a.txt" {
		t.Errorf("path = %q", args.Path)
	}
	if finish.FinishReason.Unified != provider.FinishToolCalls {
		t.Errorf("finish reason = %q, want tool-calls", finish.FinishReason.Unified)
	}
}

const emptyArgsToolStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_03","model":"claude-opus-5","usage":{"input_tokens":5,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_02","name":"list_dir","input":{}}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`

func TestToolCallWithNoArgumentsYieldsEmptyObject(t *testing.T) {
	p, _, _ := newTestProvider(t, sseHandler(emptyArgsToolStream))
	res, err := p.LanguageModel("claude-opus-5").DoStream(context.Background(), provider.CallOptions{
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "ls"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, part := range collect(t, res) {
		if call, ok := part.(provider.ToolCall); ok {
			// Anthropic sends no input deltas for a zero-argument tool, and
			// "" would fail to parse downstream.
			if call.Input != "{}" {
				t.Errorf("input = %q, want {}", call.Input)
			}
			return
		}
	}
	t.Fatal("no tool call emitted")
}

const reasoningStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_04","model":"claude-opus-5","usage":{"input_tokens":5,"output_tokens":1,"output_tokens_details":{"thinking_tokens":40}}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":0,"output_tokens":100,"output_tokens_details":{"thinking_tokens":40}}}

event: message_stop
data: {"type":"message_stop"}

`

func TestStreamReasoningCarriesSignature(t *testing.T) {
	p, _, _ := newTestProvider(t, sseHandler(reasoningStream))
	res, err := p.LanguageModel("claude-opus-5").DoStream(context.Background(), provider.CallOptions{
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "think"}}},
		},
		Reasoning: provider.ReasoningHigh,
	})
	if err != nil {
		t.Fatal(err)
	}

	var reasoning strings.Builder
	var signature string
	var finish *provider.Finish

	for _, part := range collect(t, res) {
		switch v := part.(type) {
		case provider.ReasoningDelta:
			reasoning.WriteString(v.Delta)
		case provider.ReasoningEnd:
			if sig, ok := v.ProviderMetadata[providerID]["signature"].(string); ok {
				signature = sig
			}
		case provider.Finish:
			finish = &v
		}
	}

	if reasoning.String() != "Let me think" {
		t.Errorf("reasoning = %q", reasoning.String())
	}
	// Without the signature the reasoning cannot be replayed on the next turn.
	if signature != "sig-abc" {
		t.Errorf("signature = %q, want sig-abc", signature)
	}
	if got := *finish.Usage.OutputTokens.Reasoning; got != 40 {
		t.Errorf("reasoning tokens = %d, want 40", got)
	}
	if got := *finish.Usage.OutputTokens.Text; got != 60 {
		t.Errorf("text tokens = %d, want 60 (100 total - 40 reasoning)", got)
	}
}

func TestEmptyReasoningReplaysWithThinkingField(t *testing.T) {
	p, _, body := newTestProvider(t, sseHandler(textStream))

	// A signed thinking block whose text is empty is a shape the API really
	// returns. The field still has to be on the wire: omitting it fails the
	// next turn with "missing field `thinking`".
	res, err := p.LanguageModel("claude-opus-5").DoStream(context.Background(), provider.CallOptions{
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "hi"}}},
			provider.AssistantMessage{Content: []provider.AssistantPart{
				provider.ReasoningPart{
					Text: "",
					ProviderOptions: provider.ProviderOptions{
						providerID: {"signature": "sig-abc"},
					},
				},
				provider.TextPart{Text: "done"},
			}},
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "again"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, res)

	var sent struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}

	block := sent.Messages[1].Content[0]
	if block["type"] != "thinking" {
		t.Fatalf("first assistant block = %v, want thinking", block["type"])
	}
	if _, ok := block["thinking"]; !ok {
		t.Errorf("thinking field missing from %v", block)
	}
}

func TestAdaptiveThinkingRequestShape(t *testing.T) {
	p, _, body := newTestProvider(t, sseHandler(textStream))

	// claude-opus-5 supports adaptive thinking, so it must receive
	// {"type":"adaptive"} plus output_config.effort, never budget_tokens.
	res, err := p.LanguageModel("claude-opus-5").DoStream(context.Background(), provider.CallOptions{
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "hi"}}},
		},
		Reasoning: provider.ReasoningHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, res)

	var sent messagesRequest
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Thinking == nil || sent.Thinking.Type != "adaptive" {
		t.Fatalf("thinking = %+v, want adaptive", sent.Thinking)
	}
	if sent.Thinking.BudgetTokens != nil {
		t.Errorf("adaptive thinking must not send budget_tokens, got %d", *sent.Thinking.BudgetTokens)
	}
	if sent.OutputConfig == nil || sent.OutputConfig.Effort != "high" {
		t.Errorf("output_config = %+v, want effort high", sent.OutputConfig)
	}
}

func TestBudgetThinkingRequestShape(t *testing.T) {
	p, _, body := newTestProvider(t, sseHandler(textStream))

	// claude-sonnet-4-5 predates adaptive thinking, so it must receive an
	// explicit budget and no output_config.
	maxTokens := int64(10000)
	res, err := p.LanguageModel("claude-sonnet-4-5").DoStream(context.Background(), provider.CallOptions{
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "hi"}}},
		},
		Reasoning:       provider.ReasoningMedium,
		MaxOutputTokens: &maxTokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, res)

	var sent messagesRequest
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Thinking == nil || sent.Thinking.Type != "enabled" {
		t.Fatalf("thinking = %+v, want enabled", sent.Thinking)
	}
	if sent.Thinking.BudgetTokens == nil || *sent.Thinking.BudgetTokens != 3000 {
		t.Errorf("budget = %v, want 3000 (30%% of 10000)", sent.Thinking.BudgetTokens)
	}
	if sent.OutputConfig != nil {
		t.Errorf("budget-mode models must not receive output_config, got %+v", sent.OutputConfig)
	}
}

func TestSamplingParametersDroppedForOpus5(t *testing.T) {
	p, _, body := newTestProvider(t, sseHandler(textStream))

	temp := 0.7
	res, err := p.LanguageModel("claude-opus-5").DoStream(context.Background(), provider.CallOptions{
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "hi"}}},
		},
		Temperature: &temp,
	})
	if err != nil {
		t.Fatal(err)
	}

	parts := collect(t, res)
	start, ok := parts[0].(provider.StreamStart)
	if !ok {
		t.Fatalf("first part = %T, want StreamStart", parts[0])
	}
	if len(start.Warnings) == 0 {
		t.Error("expected a warning about the dropped temperature")
	}

	var sent messagesRequest
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	// Sending temperature to this model is a 400, so it must be absent.
	if sent.Temperature != nil {
		t.Errorf("temperature must be dropped for claude-opus-5, got %v", *sent.Temperature)
	}
}

func TestStructuredOutputUsesForcedTool(t *testing.T) {
	p, _, body := newTestProvider(t, sseHandler(jsonToolStream))

	properties := jsonschema.NewProperties()
	properties.Set("city", &jsonschema.Schema{Type: jsonschema.String})
	schema := &jsonschema.Schema{Type: jsonschema.Object, Properties: properties}

	res, err := p.LanguageModel("claude-opus-5").DoStream(context.Background(), provider.CallOptions{
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "where?"}}},
		},
		ResponseFormat: &provider.ResponseFormat{Type: "json", Schema: schema},
	})
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	for _, part := range collect(t, res) {
		switch v := part.(type) {
		case provider.TextDelta:
			text.WriteString(v.Delta)
		case provider.ToolCall:
			// The workaround has to be invisible: a caller asking for JSON
			// must not have to know a tool was involved.
			t.Errorf("structured output leaked a tool call: %+v", v)
		}
	}
	if got := text.String(); got != `{"city":"Pune"}` {
		t.Errorf("text = %q, want the tool arguments", got)
	}

	// The request side: the schema becomes the only tool, and it is forced.
	var sent messagesRequest
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Tools) != 1 || sent.Tools[0].Name != jsonToolName {
		t.Fatalf("tools = %+v, want the injected json tool", sent.Tools)
	}
	if sent.ToolChoice == nil || sent.ToolChoice.Type != "tool" || sent.ToolChoice.Name != jsonToolName {
		t.Errorf("tool_choice = %+v, want the json tool forced", sent.ToolChoice)
	}
}

func TestStructuredOutputWithToolsWarns(t *testing.T) {
	p, _, body := newTestProvider(t, sseHandler(textStream))

	res, err := p.LanguageModel("claude-opus-5").DoStream(context.Background(), provider.CallOptions{
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "hi"}}},
		},
		Tools: []provider.Tool{provider.FunctionTool{Name: "read_file"}},
		ResponseFormat: &provider.ResponseFormat{
			Type:   "json",
			Schema: &jsonschema.Schema{Type: jsonschema.Object},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var warnings []provider.Warning
	for _, part := range collect(t, res) {
		if start, ok := part.(provider.StreamStart); ok {
			warnings = append(warnings, start.Warnings...)
		}
	}
	if len(warnings) == 0 {
		t.Fatal("combining tools with structured output produced no warning")
	}

	// The caller's own tool must survive: dropping it silently would break the
	// run in a way that looks like the model refusing to call anything.
	var sent messagesRequest
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Tools) != 1 || sent.Tools[0].Name != "read_file" {
		t.Errorf("tools = %+v, want the caller's tool untouched", sent.Tools)
	}
}

func TestAPIErrorIsClassified(t *testing.T) {
	p, _, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	})

	// Retries are disabled so the test asserts on the error rather than waiting.
	p.retry.MaxRetries = 0

	_, err := p.LanguageModel("claude-opus-5").DoGenerate(context.Background(), provider.CallOptions{
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "hi"}}},
		},
	})
	if err == nil {
		t.Fatal("expected an error")
	}

	var apiErr *provider.APICallError
	if !errorAs(err, &apiErr) {
		t.Fatalf("error is not an APICallError: %T", err)
	}
	if apiErr.Message != "slow down" {
		t.Errorf("message = %q, want the nested provider message", apiErr.Message)
	}
	if !apiErr.IsRetryable {
		t.Error("429 should be retryable")
	}
}

// errorAs is errors.As, kept local so the test file needs no extra import.
func errorAs(err error, target any) bool {
	type unwrapper interface{ Unwrap() error }
	if e, ok := err.(*provider.APICallError); ok {
		if p, ok := target.(**provider.APICallError); ok {
			*p = e
			return true
		}
	}
	if u, ok := err.(unwrapper); ok {
		return errorAs(u.Unwrap(), target)
	}
	return false
}

// webSearchStream is a hosted web search: the call and its result both arrive
// inside the assistant turn, because Anthropic ran the tool itself.
const webSearchStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_04","type":"message","role":"assistant","model":"claude-opus-5","content":[],"usage":{"input_tokens":20,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_01","name":"web_search","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"go 1.26\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01","content":[{"type":"web_search_result","url":"https://go.dev/doc/go1.26","title":"Go 1.26 Release Notes","page_age":"3 days"}]}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":0,"output_tokens":40}}

event: message_stop
data: {"type":"message_stop"}

`

func TestHostedWebSearchRequestShape(t *testing.T) {
	p, _, body := newTestProvider(t, sseHandler(textStream))

	res, err := p.LanguageModel("claude-opus-5").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("what shipped in go 1.26?"),
		Tools: []provider.Tool{WebSearch(WebSearchOptions{
			MaxUses:        3,
			AllowedDomains: []string{"go.dev"},
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, res)

	var sent struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Tools) != 1 {
		t.Fatalf("tools = %v, want one hosted tool", sent.Tools)
	}

	tool := sent.Tools[0]
	if tool["type"] != "web_search_20250305" || tool["name"] != "web_search" {
		t.Errorf("tool = %v, want the dated web_search type", tool)
	}
	if tool["max_uses"] != float64(3) {
		t.Errorf("max_uses = %v, want 3", tool["max_uses"])
	}
	// A hosted tool has a fixed shape; sending a schema for one is rejected.
	if _, ok := tool["input_schema"]; ok {
		t.Errorf("hosted tool carries input_schema: %v", tool)
	}
}

func TestHostedToolBetaHeaderIsSent(t *testing.T) {
	var beta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beta = r.Header.Get("anthropic-beta")
		sseHandler(textStream)(w, r)
	}))
	t.Cleanup(srv.Close)

	p := New(Options{APIKey: "test-key", BaseURL: srv.URL})

	res, err := p.LanguageModel("claude-opus-5").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("run some python"),
		Tools:  []provider.Tool{CodeExecution()},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, res)

	// Without the beta header the API rejects the tool outright.
	if beta != "code-execution-2025-08-25" {
		t.Errorf("anthropic-beta = %q, want the code execution beta", beta)
	}
}

func TestHostedWebSearchResultBecomesToolResultAndSources(t *testing.T) {
	p, _, _ := newTestProvider(t, sseHandler(webSearchStream))

	res, err := p.LanguageModel("claude-opus-5").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("what shipped in go 1.26?"),
		Tools:  []provider.Tool{WebSearch(WebSearchOptions{})},
	})
	if err != nil {
		t.Fatal(err)
	}

	var call *provider.ToolCall
	var result *provider.ToolResult
	var sources []provider.Source

	for _, part := range collect(t, res) {
		switch v := part.(type) {
		case provider.ToolCall:
			call = &v
		case provider.ToolResult:
			result = &v
		case provider.Source:
			sources = append(sources, v)
		}
	}

	if call == nil {
		t.Fatal("no tool call was emitted for the hosted search")
	}
	// Marked provider-executed, or the agent loop would run web_search locally
	// and there is no local web_search to run.
	if !call.ProviderExecuted {
		t.Error("the hosted call was not marked provider-executed")
	}
	if call.ToolName != "web_search" || call.Input != `{"query":"go 1.26"}` {
		t.Errorf("call = %+v", call)
	}

	if result == nil || result.ToolCallID != "srvtoolu_01" {
		t.Fatalf("result = %+v, want one keyed to the call", result)
	}

	if len(sources) != 1 {
		t.Fatalf("sources = %d, want one per search hit", len(sources))
	}
	if sources[0].URL != "https://go.dev/doc/go1.26" || sources[0].Title != "Go 1.26 Release Notes" {
		t.Errorf("source = %+v", sources[0])
	}
}

func TestHostedToolResultReplaysAsItsOwnBlockType(t *testing.T) {
	p, _, body := newTestProvider(t, sseHandler(textStream))

	res, err := p.LanguageModel("claude-opus-5").DoStream(context.Background(), provider.CallOptions{
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "search"}}},
			provider.AssistantMessage{Content: []provider.AssistantPart{
				provider.ToolCallPart{
					ToolCallID:       "srvtoolu_01",
					ToolName:         "web_search",
					Input:            map[string]any{"query": "go"},
					ProviderExecuted: true,
				},
				provider.ToolResultPart{
					ToolCallID: "srvtoolu_01",
					ToolName:   "web_search",
					Output: provider.ToolOutputJSON{Value: []any{
						map[string]any{"type": "web_search_result", "url": "https://go.dev"},
					}},
				},
			}},
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "and now?"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, res)

	var sent struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}

	blocks := sent.Messages[1].Content
	// A plain tool_use/tool_result pair is rejected: the API matches the block
	// type against the tool that produced it.
	if blocks[0]["type"] != "server_tool_use" {
		t.Errorf("call block = %v, want server_tool_use", blocks[0]["type"])
	}
	if blocks[1]["type"] != "web_search_tool_result" {
		t.Errorf("result block = %v, want web_search_tool_result", blocks[1]["type"])
	}
	if blocks[1]["tool_use_id"] != "srvtoolu_01" {
		t.Errorf("result block lost its tool_use_id: %v", blocks[1])
	}
}

// userPrompt is a minimal valid prompt.
func userPrompt(text string) provider.Prompt {
	return provider.Prompt{
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: text}}},
	}
}
