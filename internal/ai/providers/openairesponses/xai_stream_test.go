package openairesponses

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
)

// xAI's own recorded streams (@ai-sdk/xai's fixtures) come through whole:
// every text delta, the reasoning, and a stop.
func TestXAIRecordedStreams(t *testing.T) {
	for _, name := range []string{"xai-text-with-reasoning-streaming.1.chunks.txt", "xai-x-search-tool.chunks.txt"} {
		raw, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Count(string(raw), `"type":"response.output_text.delta"`)
		p, _ := serve(t, fixtureSSE(t, name))
		p.xai = true
		res, err := p.LanguageModel("grok-4").DoStream(context.Background(), provider.CallOptions{Prompt: userPrompt("hi")})
		if err != nil {
			t.Fatal(err)
		}
		deltas := 0
		var finish provider.FinishReason
		for _, part := range parts(t, res) {
			switch v := part.(type) {
			case provider.TextDelta:
				deltas++
			case provider.Finish:
				finish = v.FinishReason
			case provider.ErrorPart:
				t.Fatalf("%s: %v", name, v.Err)
			}
		}
		if deltas != want || finish.Unified != provider.FinishStop || finish.Raw != "completed" {
			t.Errorf("%s: %d/%d deltas, finish %+v", name, deltas, want, finish)
		}
	}
}

// A call's argument deltas are matched by output index, as the AI SDK
// matches them, and xAI's response.done is a completion like
// response.completed.
func TestXAIToolCallStream(t *testing.T) {
	body := strings.Join([]string{
		`{"type":"response.created","response":{"id":"r","model":"grok-4"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"a.go\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"a.go\"}"}}`,
		`{"type":"response.done","response":{"id":"r","status":"completed","usage":{"input_tokens":5,"output_tokens":3}}}`,
	}, "\n\ndata: ")
	p, _ := serve(t, "data: "+body+"\n\n")
	p.xai = true
	res, err := p.LanguageModel("grok-4").DoStream(context.Background(), provider.CallOptions{Prompt: userPrompt("hi")})
	if err != nil {
		t.Fatal(err)
	}
	var args strings.Builder
	var finish provider.FinishReason
	for _, part := range parts(t, res) {
		switch v := part.(type) {
		case provider.ToolInputDelta:
			args.WriteString(v.Delta)
		case provider.Finish:
			finish = v.FinishReason
		case provider.ErrorPart:
			t.Fatal(v.Err)
		}
	}
	if args.String() != `{"path":"a.go"}` {
		t.Errorf("streamed arguments = %q", args.String())
	}
	if finish.Unified != provider.FinishToolCalls {
		t.Errorf("finish = %+v, want tool-calls (not interrupted)", finish)
	}
}
