package bedrock

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/jsonschema"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

func eventStreamHandler(frames ...[]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", eventStreamAccept)
		w.WriteHeader(http.StatusOK)
		for _, frame := range frames {
			w.Write(frame)
		}
	}
}

func frame(eventType string, payload any) []byte {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return encodeEventMessage(eventType, raw)
}

func collect(t *testing.T, res *provider.StreamResult) []provider.StreamPart {
	t.Helper()
	var parts []provider.StreamPart
	for p := range res.Stream {
		parts = append(parts, p)
	}
	return parts
}

func TestStreamText(t *testing.T) {
	p, _, path := testProvider(t, eventStreamHandler(
		frame("contentBlockStart", map[string]any{"contentBlockIndex": 0, "start": map[string]any{}}),
		frame("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": "Hello"}}),
		frame("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": ", world"}}),
		frame("contentBlockStop", map[string]any{"contentBlockIndex": 0}),
		frame("messageStop", map[string]any{"stopReason": "end_turn"}),
		frame("metadata", map[string]any{"usage": map[string]any{"inputTokens": 10, "outputTokens": 5}}),
	))

	res, err := p.LanguageModel("anthropic.claude-sonnet-4-5").DoStream(
		context.Background(), provider.CallOptions{Prompt: userPrompt("hi")})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(*path, "/converse-stream") {
		t.Errorf("path = %q", *path)
	}

	var text strings.Builder
	var finish *provider.Finish
	for _, part := range collect(t, res) {
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
	if finish == nil {
		t.Fatal("no finish")
	}
	if finish.FinishReason.Unified != provider.FinishStop {
		t.Errorf("finish = %q", finish.FinishReason.Unified)
	}
	if got := *finish.Usage.InputTokens.NoCache; got != 10 {
		t.Errorf("input = %d", got)
	}
}

func TestStreamToolCall(t *testing.T) {
	p, _, _ := testProvider(t, eventStreamHandler(
		frame("contentBlockStart", map[string]any{
			"contentBlockIndex": 0,
			"start":             map[string]any{"toolUse": map[string]any{"toolUseId": "c1", "name": "read_file"}},
		}),
		frame("contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta":             map[string]any{"toolUse": map[string]any{"input": `{"path":`}},
		}),
		frame("contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta":             map[string]any{"toolUse": map[string]any{"input": `"/tmp/a"}`}},
		}),
		frame("contentBlockStop", map[string]any{"contentBlockIndex": 0}),
		frame("messageStop", map[string]any{"stopReason": "tool_use"}),
	))

	res, err := p.LanguageModel("anthropic.claude-sonnet-4-5").DoStream(
		context.Background(), provider.CallOptions{Prompt: userPrompt("read")})
	if err != nil {
		t.Fatal(err)
	}

	var call *provider.ToolCall
	var finish *provider.Finish
	var started bool
	for _, part := range collect(t, res) {
		switch v := part.(type) {
		case provider.ToolInputStart:
			started = true
			if v.ID != "c1" || v.ToolName != "read_file" {
				t.Errorf("start = %+v", v)
			}
		case provider.ToolCall:
			call = &v
		case provider.Finish:
			finish = &v
		}
	}

	if !started {
		t.Error("no tool-input-start")
	}
	if call == nil {
		t.Fatal("no tool call")
	}
	if call.ToolCallID != "c1" || call.Input != `{"path":"/tmp/a"}` {
		t.Errorf("call = %+v", call)
	}
	if finish.FinishReason.Unified != provider.FinishToolCalls {
		t.Errorf("finish = %q", finish.FinishReason.Unified)
	}
}

func TestStreamReasoning(t *testing.T) {
	p, _, _ := testProvider(t, eventStreamHandler(
		frame("contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta":             map[string]any{"reasoningContent": map[string]any{"text": "considering"}},
		}),
		frame("contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta":             map[string]any{"reasoningContent": map[string]any{"signature": "sig-1"}},
		}),
		frame("contentBlockStop", map[string]any{"contentBlockIndex": 0}),
		frame("contentBlockDelta", map[string]any{
			"contentBlockIndex": 1,
			"delta":             map[string]any{"text": "The answer"},
		}),
		frame("contentBlockStop", map[string]any{"contentBlockIndex": 1}),
		frame("messageStop", map[string]any{"stopReason": "end_turn"}),
	))

	res, err := p.LanguageModel("anthropic.claude-sonnet-4-5").DoStream(
		context.Background(), provider.CallOptions{Prompt: userPrompt("think")})
	if err != nil {
		t.Fatal(err)
	}

	var reasoning, text strings.Builder
	var sawSignature bool
	for _, part := range collect(t, res) {
		switch v := part.(type) {
		case provider.ReasoningDelta:
			reasoning.WriteString(v.Delta)
			if v.ProviderMetadata != nil {
				if block := v.ProviderMetadata[providerID]; block != nil {
					if block["signature"] == "sig-1" {
						sawSignature = true
					}
				}
			}
		case provider.TextDelta:
			text.WriteString(v.Delta)
		}
	}

	if reasoning.String() != "considering" {
		t.Errorf("reasoning = %q", reasoning.String())
	}
	if !sawSignature {
		t.Error("signature must ride on a reasoning delta so it can be replayed")
	}
	if text.String() != "The answer" {
		t.Errorf("text = %q", text.String())
	}
}

func TestStreamException(t *testing.T) {
	p, _, _ := testProvider(t, eventStreamHandler(
		encodeExceptionMessage("throttlingException", "Rate exceeded"),
	))

	res, err := p.LanguageModel("anthropic.claude-sonnet-4-5").DoStream(
		context.Background(), provider.CallOptions{Prompt: userPrompt("hi")})
	if err != nil {
		t.Fatal(err)
	}

	var sawError bool
	var finish *provider.Finish
	for _, part := range collect(t, res) {
		switch v := part.(type) {
		case provider.ErrorPart:
			sawError = true
			if !strings.Contains(v.Err.Error(), "Rate exceeded") {
				t.Errorf("error = %v", v.Err)
			}
		case provider.Finish:
			finish = &v
		}
	}
	if !sawError {
		t.Fatal("expected an error part")
	}
	if finish.FinishReason.Unified != provider.FinishError {
		t.Errorf("finish = %q", finish.FinishReason.Unified)
	}
}

func TestStreamJSONToolBecomesText(t *testing.T) {
	p, _, _ := testProvider(t, eventStreamHandler(
		frame("contentBlockStart", map[string]any{
			"contentBlockIndex": 0,
			"start":             map[string]any{"toolUse": map[string]any{"toolUseId": "j1", "name": "json"}},
		}),
		frame("contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta":             map[string]any{"toolUse": map[string]any{"input": `{"ok":true}`}},
		}),
		frame("contentBlockStop", map[string]any{"contentBlockIndex": 0}),
		frame("messageStop", map[string]any{"stopReason": "tool_use"}),
	))

	res, err := p.LanguageModel("amazon.nova-pro-v1:0").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("shape"),
		ResponseFormat: &provider.ResponseFormat{
			Type:   "json",
			Schema: &jsonschema.Schema{Type: jsonschema.Object},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	var sawTool bool
	var finish *provider.Finish
	for _, part := range collect(t, res) {
		switch v := part.(type) {
		case provider.TextDelta:
			text.WriteString(v.Delta)
		case provider.ToolCall:
			sawTool = true
		case provider.Finish:
			finish = &v
		}
	}
	if sawTool {
		t.Error("the json tool must not appear as a tool call")
	}
	if text.String() != `{"ok":true}` {
		t.Errorf("text = %q", text.String())
	}
	if finish.FinishReason.Unified != provider.FinishStop {
		t.Errorf("finish = %q", finish.FinishReason.Unified)
	}
}

func TestStreamRawChunks(t *testing.T) {
	p, _, _ := testProvider(t, eventStreamHandler(
		frame("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": "x"}}),
		frame("contentBlockStop", map[string]any{"contentBlockIndex": 0}),
		frame("messageStop", map[string]any{"stopReason": "end_turn"}),
	))

	res, err := p.LanguageModel("anthropic.claude-sonnet-4-5").DoStream(
		context.Background(), provider.CallOptions{Prompt: userPrompt("hi"), IncludeRawChunks: true})
	if err != nil {
		t.Fatal(err)
	}

	var raws int
	for _, part := range collect(t, res) {
		if _, ok := part.(provider.Raw); ok {
			raws++
		}
	}
	if raws == 0 {
		t.Fatal("expected raw chunks")
	}
}
