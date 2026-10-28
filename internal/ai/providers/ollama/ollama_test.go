package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
)

const stream = `{"model":"qwen3","created_at":"t","message":{"role":"assistant","content":"","thinking":"Let me look."},"done":false}
{"model":"qwen3","created_at":"t","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"ls","arguments":{"path":"."}}}]},"done":false}
{"model":"qwen3","created_at":"t","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":120,"eval_count":30}
`

// The native /api/chat: num_ctx and think in the body, NDJSON parsed into
// reasoning and an id-generated tool call, usage from the counts, and the
// finish corrected to tool-calls as ollama-ai-provider-v2 does.
func TestNativeChat(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sent)
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, stream)
	}))
	t.Cleanup(srv.Close)
	m := New(Options{BaseURL: srv.URL + "/api"}).LanguageModel("qwen3")
	res, err := m.DoStream(context.Background(), provider.CallOptions{
		Prompt:          provider.Prompt{provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "hi"}}}},
		Tools:           []provider.Tool{provider.FunctionTool{Name: "ls"}},
		ProviderOptions: provider.ProviderOptions{"ollama": {"think": true, "options": map[string]any{"num_ctx": 32768}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var reasoning string
	var call *provider.ToolCall
	var finish provider.Finish
	for p := range res.Stream {
		switch v := p.(type) {
		case provider.ReasoningDelta:
			reasoning += v.Delta
		case provider.ToolCall:
			call = &v
		case provider.Finish:
			finish = v
		case provider.ErrorPart:
			t.Fatalf("error %v", v.Err)
		}
	}
	if sent["think"] != true || sent["options"].(map[string]any)["num_ctx"] != float64(32768) || sent["messages"].([]any)[0].(map[string]any)["content"] != "hi" {
		t.Errorf("request = %v", sent)
	}
	if reasoning != "Let me look." || call == nil || call.ToolName != "ls" || call.ToolCallID == "" || call.Input != `{"path":"."}` {
		t.Errorf("reasoning %q call %+v", reasoning, call)
	}
	if finish.FinishReason.Unified != provider.FinishToolCalls || *finish.Usage.InputTokens.Total != 120 || *finish.Usage.OutputTokens.Total != 30 {
		t.Errorf("finish %+v", finish)
	}
}
