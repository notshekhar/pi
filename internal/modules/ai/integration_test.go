package ai_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providers/anthropic"
	"github.com/notshekhar/pi/internal/modules/ai/providers/google"
	"github.com/notshekhar/pi/internal/modules/ai/providers/openaicompat"
)

// scriptedServer replays one canned response per request and records the
// bodies it received, so a test can assert on what the second turn was told.
type scriptedServer struct {
	mu        sync.Mutex
	responses []string
	calls     int
	bodies    [][]byte
}

func (s *scriptedServer) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	s.mu.Lock()
	s.bodies = append(s.bodies, body)
	i := s.calls
	s.calls++
	s.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	if i < len(s.responses) {
		io.WriteString(w, s.responses[i])
		return
	}
	w.WriteHeader(http.StatusInternalServerError)
}

func (s *scriptedServer) body(i int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[i]
}

// weatherArgs is the input to the integration test's tool.
type weatherArgs struct {
	City string `json:"city" jsonschema:"description=City to look up"`
}

// runAgent drives a two-step conversation and returns the streamed text.
func runAgent(t *testing.T, model provider.LanguageModel) (*ai.Result, string, []string) {
	t.Helper()

	weather := ai.NewTool("get_weather", "Look up the weather in a city",
		func(ctx context.Context, a weatherArgs) (ai.ToolResult, error) {
			return ai.ToolTextf("It is 22C and sunny in %s.", a.City), nil
		})

	res, err := ai.StreamText(context.Background(), ai.Options{
		Model:    model,
		System:   "You are a weather assistant.",
		Messages: []provider.Message{ai.UserText("what is the weather in Paris?")},
		Tools:    []ai.Tool{weather},
	})
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	var events []string
	for part := range res.Stream {
		events = append(events, part.StreamPartType())
		if d, ok := part.(provider.TextDelta); ok {
			text.WriteString(d.Delta)
		}
		if e, ok := part.(provider.ErrorPart); ok {
			t.Fatalf("stream error: %v", e.Err)
		}
	}

	final, err := res.Final()
	if err != nil {
		t.Fatal(err)
	}
	return final, text.String(), events
}

const anthropicToolTurn = `event: message_start
data: {"type":"message_start","message":{"id":"m1","model":"claude-opus-5","usage":{"input_tokens":30,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":0,"output_tokens":15}}

event: message_stop
data: {"type":"message_stop"}

`

const anthropicAnswerTurn = `event: message_start
data: {"type":"message_start","message":{"id":"m2","model":"claude-opus-5","usage":{"input_tokens":60,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"It is 22C and sunny in Paris."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":0,"output_tokens":12}}

event: message_stop
data: {"type":"message_stop"}

`

func TestEndToEndAnthropic(t *testing.T) {
	script := &scriptedServer{responses: []string{anthropicToolTurn, anthropicAnswerTurn}}
	srv := httptest.NewServer(http.HandlerFunc(script.handler))
	defer srv.Close()

	model := anthropic.New(anthropic.Options{APIKey: "test", BaseURL: srv.URL}).
		LanguageModel("claude-opus-5")

	final, text, events := runAgent(t, model)

	if text != "It is 22C and sunny in Paris." {
		t.Errorf("text = %q", text)
	}
	if len(final.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(final.Steps))
	}
	if script.calls != 2 {
		t.Errorf("model calls = %d, want 2", script.calls)
	}

	// Usage must span both turns: 30+60 fresh input, 15+12 output.
	if got := *final.Usage.InputTokens.Total; got != 90 {
		t.Errorf("input total = %d, want 90", got)
	}
	if got := *final.Usage.OutputTokens.Total; got != 27 {
		t.Errorf("output total = %d, want 27", got)
	}

	// The second request must replay the tool result in Anthropic's shape:
	// a user message carrying a tool_result block.
	var second struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
				Content   any    `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(script.body(1), &second); err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, m := range second.Messages {
		for _, c := range m.Content {
			if c.Type == "tool_result" && c.ToolUseID == "toolu_1" {
				found = true
				if !strings.Contains(c.Content.(string), "22C") {
					t.Errorf("tool result content = %v", c.Content)
				}
			}
		}
	}
	if !found {
		t.Errorf("second request did not replay the tool result: %s", script.body(1))
	}

	// The event sequence a consumer sees should be well-formed.
	assertContainsInOrder(t, events, []string{
		"step-start", "tool-input-start", "tool-call", "tool-executed",
		"step-finish", "step-start", "text-delta", "step-finish", "run-finish",
	})
}

const openaiToolTurn = `data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"c1","choices":[],"usage":{"prompt_tokens":30,"completion_tokens":15}}

data: [DONE]

`

const openaiAnswerTurn = `data: {"id":"c2","choices":[{"index":0,"delta":{"role":"assistant","content":"It is 22C and sunny in Paris."}}]}

data: {"id":"c2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"c2","choices":[],"usage":{"prompt_tokens":60,"completion_tokens":12}}

data: [DONE]

`

func TestEndToEndOpenAICompatible(t *testing.T) {
	script := &scriptedServer{responses: []string{openaiToolTurn, openaiAnswerTurn}}
	srv := httptest.NewServer(http.HandlerFunc(script.handler))
	defer srv.Close()

	model := openaicompat.New(openaicompat.Options{APIKey: "test", BaseURL: srv.URL}).
		LanguageModel("gpt-x")

	final, text, _ := runAgent(t, model)

	if text != "It is 22C and sunny in Paris." {
		t.Errorf("text = %q", text)
	}
	if len(final.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(final.Steps))
	}

	// The second request must replay the result as a tool-role message.
	var second struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    any    `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(script.body(1), &second); err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, m := range second.Messages {
		if m.Role == "tool" && m.ToolCallID == "call_1" {
			found = true
			if !strings.Contains(m.Content.(string), "22C") {
				t.Errorf("tool message content = %v", m.Content)
			}
		}
	}
	if !found {
		t.Errorf("second request did not replay the tool result: %s", script.body(1))
	}
}

const googleToolTurn = `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":30,"candidatesTokenCount":15}}

`

const googleAnswerTurn = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"It is 22C and sunny in Paris."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":60,"candidatesTokenCount":12}}

`

func TestEndToEndGoogle(t *testing.T) {
	script := &scriptedServer{responses: []string{googleToolTurn, googleAnswerTurn}}
	srv := httptest.NewServer(http.HandlerFunc(script.handler))
	defer srv.Close()

	model := google.New(google.Options{APIKey: "test", BaseURL: srv.URL}).
		LanguageModel("gemini-3-pro")

	final, text, _ := runAgent(t, model)

	if text != "It is 22C and sunny in Paris." {
		t.Errorf("text = %q", text)
	}
	// Google reports STOP alongside a function call; if that were taken at
	// face value the loop would stop before running the tool.
	if len(final.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(final.Steps))
	}

	// The second request must replay the result as a functionResponse in a
	// user turn, which is Google's equivalent of a tool message.
	var second struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				FunctionResponse *struct {
					Name     string         `json:"name"`
					Response map[string]any `json:"response"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(script.body(1), &second); err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, c := range second.Contents {
		for _, p := range c.Parts {
			if p.FunctionResponse != nil && p.FunctionResponse.Name == "get_weather" {
				found = true
				if c.Role != "user" {
					t.Errorf("functionResponse role = %q, want user", c.Role)
				}
				if !strings.Contains(p.FunctionResponse.Response["result"].(string), "22C") {
					t.Errorf("response = %v", p.FunctionResponse.Response)
				}
			}
		}
	}
	if !found {
		t.Errorf("second request did not replay the tool result: %s", script.body(1))
	}
}

// assertContainsInOrder checks that want appears as a subsequence of got.
func assertContainsInOrder(t *testing.T, got, want []string) {
	t.Helper()

	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Errorf("event sequence missing %q\ngot: %v\nwant subsequence: %v", want[i], got, want)
	}
}
