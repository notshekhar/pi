package openairesponses

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
)

// fixtureSSE is the first response of an AI SDK fixture (one event per line)
// as an SSE body. The fixtures are @ai-sdk/openai's recorded streams.
func fixtureSSE(t *testing.T, name string) string {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Text()
		b.WriteString("data: " + line + "\n\n")
		if strings.Contains(line, `"type":"response.completed"`) || strings.Contains(line, `"type":"response.failed"`) {
			break
		}
	}
	return b.String()
}

func serve(t *testing.T, body string) (*Provider, *[]map[string]any) {
	t.Helper()
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %s, want /v1/responses", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		sent = append(sent, req)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return New(Options{APIKey: "k", BaseURL: srv.URL + "/v1"}), &sent
}

func parts(t *testing.T, res *provider.StreamResult) []provider.StreamPart {
	t.Helper()
	var out []provider.StreamPart
	for p := range res.Stream {
		out = append(out, p)
	}
	return out
}

func userPrompt(text string) provider.Prompt {
	return provider.Prompt{provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: text}}}}
}

// A recorded reasoning + function-call stream: reasoning carries its item id
// and encrypted content for replay, calls arrive whole with their call ids,
// usage comes from response.completed, and the finish is tool-calls.
func TestStreamReasoningAndToolCalls(t *testing.T) {
	p, _ := serve(t, fixtureSSE(t, "openai-reasoning-encrypted-content.1.chunks.txt"))
	res, err := p.LanguageModel("gpt-5.1-codex-max").DoStream(context.Background(), provider.CallOptions{Prompt: userPrompt("hi")})
	if err != nil {
		t.Fatal(err)
	}
	var calls []provider.ToolCall
	var reasoningEnd *provider.ReasoningEnd
	var finish *provider.Finish
	for _, part := range parts(t, res) {
		switch v := part.(type) {
		case provider.ToolCall:
			calls = append(calls, v)
		case provider.ReasoningEnd:
			reasoningEnd = &v
		case provider.Finish:
			finish = &v
		case provider.ErrorPart:
			t.Fatalf("error: %v", v.Err)
		}
	}
	if len(calls) == 0 || calls[0].ToolName != "calculator" || calls[0].ToolCallID != "call_AB6AaRZ1FYZB2RwS6A5vbdqn" ||
		calls[0].Input != `{"a":12,"b":7,"op":"add"}` {
		t.Fatalf("calls = %+v", calls)
	}
	if reasoningEnd == nil {
		t.Fatal("no reasoning end")
	}
	md := reasoningEnd.ProviderMetadata["openai"]
	if md["itemId"] != "rs_01830d662ab3856501693c321405c88190be3ab04d5782d5f9" || md["reasoningEncryptedContent"] == nil {
		t.Errorf("reasoning metadata = %v", md)
	}
	if finish == nil || finish.FinishReason.Unified != provider.FinishToolCalls {
		t.Fatalf("finish = %+v", finish)
	}
	if *finish.Usage.InputTokens.Total == 0 || finish.Usage.OutputTokens.Total == nil {
		t.Errorf("usage = %+v", finish.Usage)
	}
}

// A recorded quota failure is one retry-classifiable error, finish error.
func TestStreamError(t *testing.T) {
	p, _ := serve(t, fixtureSSE(t, "openai-error.1.chunks.txt"))
	res, err := p.LanguageModel("gpt-5-nano").DoStream(context.Background(), provider.CallOptions{Prompt: userPrompt("hi")})
	if err != nil {
		t.Fatal(err)
	}
	var errs []error
	var finish provider.Finish
	for _, part := range parts(t, res) {
		switch v := part.(type) {
		case provider.ErrorPart:
			errs = append(errs, v.Err)
		case provider.Finish:
			finish = v
		}
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "exceeded your current quota") {
		t.Fatalf("errors = %v", errs)
	}
	if finish.FinishReason.Unified != provider.FinishError {
		t.Errorf("finish = %q", finish.FinishReason.Unified)
	}
}

// The request: a reasoning model's system prompt as a developer message,
// reasoning with an effort and a summary, earlier reasoning replayed by item
// reference, the call in full, its output by call id, function tools, and the
// prompt cache key from provider options.
func TestRequestShape(t *testing.T) {
	p, sent := serve(t, fixtureSSE(t, "openai-reasoning-encrypted-content.1.chunks.txt"))
	prompt := provider.Prompt{
		provider.SystemMessage{Content: "be brief"},
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "add"}}},
		provider.AssistantMessage{Content: []provider.AssistantPart{
			provider.ReasoningPart{Text: "thinking", ProviderOptions: provider.ProviderOptions{"openai": {"itemId": "rs_1"}}},
			provider.ToolCallPart{ToolCallID: "call_1", ToolName: "calculator", Input: map[string]any{"a": 1}},
		}},
		provider.ToolMessage{Content: []provider.ToolPart{
			provider.ToolResultPart{ToolCallID: "call_1", ToolName: "calculator", Output: provider.ToolOutputText{Value: "2"}},
		}},
	}
	res, err := p.LanguageModel("azure/gpt-6.1-sol").DoStream(context.Background(), provider.CallOptions{
		Prompt: prompt, Reasoning: provider.ReasoningMedium,
		Tools:           []provider.Tool{provider.FunctionTool{Name: "calculator", Description: "math"}},
		ProviderOptions: provider.ProviderOptions{"openai": {"promptCacheKey": "sess-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parts(t, res)
	req := (*sent)[0]
	got, _ := json.Marshal(req["input"])
	want := `[{"content":"be brief","role":"developer"},{"content":[{"text":"add","type":"input_text"}],"role":"user"},` +
		`{"id":"rs_1","type":"item_reference"},{"arguments":"{\"a\":1}","call_id":"call_1","name":"calculator","type":"function_call"},` +
		`{"call_id":"call_1","output":"2","type":"function_call_output"}]`
	if string(got) != want {
		t.Errorf("input =\n%s\nwant\n%s", got, want)
	}
	if r, _ := req["reasoning"].(map[string]any); r["effort"] != "medium" || r["summary"] != "auto" {
		t.Errorf("reasoning = %v", req["reasoning"])
	}
	if req["prompt_cache_key"] != "sess-1" || req["stream"] != true {
		t.Errorf("prompt_cache_key = %v, stream = %v", req["prompt_cache_key"], req["stream"])
	}
	if tools, _ := req["tools"].([]any); len(tools) != 1 || tools[0].(map[string]any)["type"] != "function" {
		t.Errorf("tools = %v", req["tools"])
	}
}

// GPT-6 models without a "none" effort do not get one (the AI SDK drops it
// with a warning); a non-reasoning model gets no reasoning block at all.
func TestReasoningCapabilities(t *testing.T) {
	for id, want := range map[string]capabilities{
		"gpt-6.1-sol":       {reasoning: true, developerRole: true, supportedEfforts: []string{"low", "medium", "high", "xhigh", "max"}},
		"azure/gpt-6-sol":   {reasoning: true, developerRole: true, supportedEfforts: []string{"none", "low", "medium", "high", "xhigh", "max"}},
		"gpt-5.6-terra":     {reasoning: true, developerRole: true},
		"o3":                {reasoning: true, developerRole: true},
		"gpt-4.1":           {},
		"gpt-5-chat-latest": {},
	} {
		got := capabilitiesOf(id)
		if got.reasoning != want.reasoning || strings.Join(got.supportedEfforts, ",") != strings.Join(want.supportedEfforts, ",") {
			t.Errorf("%s: %+v, want %+v", id, got, want)
		}
	}
	p, sent := serve(t, fixtureSSE(t, "openai-error.1.chunks.txt"))
	res, _ := p.LanguageModel("gpt-6.1-sol").DoStream(context.Background(), provider.CallOptions{Prompt: userPrompt("hi"), Reasoning: provider.ReasoningNone})
	parts(t, res)
	if _, has := (*sent)[0]["reasoning"]; has {
		t.Errorf("gpt-6.1-sol was sent an unsupported effort: %v", (*sent)[0]["reasoning"])
	}
}

// xAI's dialect (@ai-sdk/xai's convertToXaiResponsesInput and getArgs): a
// system message, reasoning and text replayed whole with their ids, calls
// with id and status, efforts mapped onto low/medium/high, and none for a
// model that rejects one.
func TestXAIRequestShape(t *testing.T) {
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		sent = append(sent, req)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, fixtureSSE(t, "openai-error.1.chunks.txt"))
	}))
	t.Cleanup(srv.Close)
	p := New(Options{Name: "xai", XAI: true, APIKey: "k", BaseURL: srv.URL})
	x := provider.ProviderOptions{"xai": {"itemId": "rs_1", "reasoningEncryptedContent": "enc"}}
	prompt := provider.Prompt{
		provider.SystemMessage{Content: "sys"},
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "q"}}},
		provider.AssistantMessage{Content: []provider.AssistantPart{
			provider.ReasoningPart{Text: "think", ProviderOptions: x},
			provider.ToolCallPart{ToolCallID: "call_1", ToolName: "ls", Input: map[string]any{},
				ProviderOptions: provider.ProviderOptions{"xai": {"itemId": "fc_1"}}},
		}},
		provider.ToolMessage{Content: []provider.ToolPart{
			provider.ToolResultPart{ToolCallID: "call_1", Output: provider.ToolOutputErrorText{Value: "boom"}},
		}},
		provider.AssistantMessage{Content: []provider.AssistantPart{
			provider.TextPart{Text: "done", ProviderOptions: provider.ProviderOptions{"xai": {"itemId": "msg_1"}}},
		}},
	}
	for _, c := range []struct {
		model  string
		effort provider.ReasoningEffort
		want   string
	}{
		{"grok-4.7", provider.ReasoningXHigh, "high"},
		{"grok-3-mini", provider.ReasoningMinimal, "low"},
		{"grok-4.20-0309-reasoning", provider.ReasoningHigh, ""},
	} {
		sent = nil
		res, err := p.LanguageModel(c.model).DoStream(context.Background(), provider.CallOptions{Prompt: prompt, Reasoning: c.effort})
		if err != nil {
			t.Fatal(err)
		}
		parts(t, res)
		r, _ := sent[0]["reasoning"].(map[string]any)
		if got, _ := r["effort"].(string); got != c.want {
			t.Errorf("%s: effort %q, want %q", c.model, got, c.want)
		}
	}
	got, _ := json.Marshal(sent[0]["input"])
	want := `[{"content":"sys","role":"system"},{"content":[{"text":"q","type":"input_text"}],"role":"user"},` +
		`{"encrypted_content":"enc","id":"rs_1","status":"completed","summary":[{"text":"think","type":"summary_text"}],"type":"reasoning"},` +
		`{"arguments":"{}","call_id":"call_1","id":"fc_1","name":"ls","status":"completed","type":"function_call"},` +
		`{"call_id":"call_1","output":"boom","type":"function_call_output"},{"content":"done","id":"msg_1","role":"assistant"}]`
	if string(got) != want {
		t.Errorf("input =\n%s\nwant\n%s", got, want)
	}
}
