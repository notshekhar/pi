package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/config"
)

const toolStep = `event: message_start
data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"claude-x","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_%d","name":"echo","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"s\":\"x\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`

const textStep = `event: message_start
data: {"type":"message_start","message":{"id":"m2","type":"message","role":"assistant","model":"claude-x","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`

// gateway is a fake Anthropic API: it records every request body and plays
// the scripted SSE responses in order.
type gateway struct {
	mu     sync.Mutex
	bodies []map[string]any
	script []string
}

// requests is a snapshot of the bodies recorded so far.
func (g *gateway) requests() []map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]map[string]any{}, g.bodies...)
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	g.mu.Lock()
	g.bodies = append(g.bodies, body)
	n := len(g.bodies)
	reply := textStep
	if n <= len(g.script) {
		reply = g.script[n-1]
	}
	g.mu.Unlock()
	if status, body, ok := strings.Cut(reply, "HTTP "); ok && status == "" {
		// "HTTP <code> <body>": a refused request.
		code, msg, _ := strings.Cut(body, " ")
		w.Header().Set("Content-Type", "application/json")
		n, _ := strconv.Atoi(code)
		w.WriteHeader(n)
		_, _ = io.WriteString(w, msg)
		return
	}
	if body["stream"] != true {
		// A non-streaming call (the summarizer): one JSON message.
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-x",`+
			`"content":[{"type":"text","text":"## Goal\nsummary"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":5}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, strings.ReplaceAll(reply, "%d", string(rune('0'+n))))
}

// withGateway registers a custom provider "gw" speaking the given SDK at a
// fake server, in a scratch home.
func withGateway(t *testing.T, sdk string, g http.Handler) config.Config {
	t.Helper()
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	auth := map[string]any{"providers": map[string]any{}, "customProviders": map[string]any{
		"gw": map[string]any{"sdk": sdk, "baseURL": srv.URL, "apiKey": "k", "models": []map[string]any{{"id": "claude-x", "contextWindow": 1000}}},
	}}
	raw, _ := json.Marshal(auth)
	if err := os.MkdirAll(filepath.Join(dir, ".loop"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".loop", "auth.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return config.Config{Provider: "gw", ModelID: "claude-x"}
}

func echoTool() ai.Tool {
	return ai.NewTool("echo", "echo", func(_ context.Context, a struct {
		S string `json:"s"`
	}) (ai.ToolResult, error) {
		return ai.ToolText(a.S), nil
	})
}

// breakpoints lists where cache_control sits in a request: "system" or
// "<message index>/<block index>".
func breakpoints(body map[string]any) []string {
	var out []string
	if sys, ok := body["system"].([]any); ok {
		for _, b := range sys {
			if b.(map[string]any)["cache_control"] != nil {
				out = append(out, "system")
			}
		}
	}
	for i, m := range body["messages"].([]any) {
		content, _ := m.(map[string]any)["content"].([]any)
		for j, b := range content {
			if b.(map[string]any)["cache_control"] != nil {
				out = append(out, string(rune('0'+i))+"/"+string(rune('0'+j)))
			}
		}
	}
	return out
}

func lastBlock(body map[string]any) string {
	msgs := body["messages"].([]any)
	i := len(msgs) - 1
	content := msgs[i].(map[string]any)["content"].([]any)
	return string(rune('0'+i)) + "/" + string(rune('0'+len(content)-1))
}

// A multi-step Anthropic turn anchors the system prompt and the tail of
// every step's request, moving the tail as the conversation grows and never
// exceeding Anthropic's four breakpoints — loop's withAnthropicCaching +
// moveAnthropicCacheTail, observed on the wire.
func TestAnthropicRequestsCarryMovingCacheBreakpoints(t *testing.T) {
	g := &gateway{script: []string{toolStep, toolStep, textStep}}
	cfg := withGateway(t, "anthropic", g)
	if !AnthropicCaching(cfg.Provider) {
		t.Fatal("a custom provider speaking Anthropic's SDK must get Anthropic caching")
	}
	model, err := config.LanguageModel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	history := []provider.Message{ai.UserText("first")}
	run := func() *ai.Result {
		res, err := ai.StreamText(context.Background(), cachedOptions(ai.Options{
			Model: model, System: "SYSTEM", Messages: history, Tools: []ai.Tool{echoTool()}, MaxSteps: 8,
		}, cfg, "conv"))
		if err != nil {
			t.Fatal(err)
		}
		for range res.Stream {
		}
		final, err := res.Final()
		if err != nil {
			t.Fatal(err)
		}
		return final
	}
	res := run()
	if len(g.bodies) != 3 {
		t.Fatalf("requests = %d, want 3 (two tool steps and an answer)", len(g.bodies))
	}
	for i, body := range g.bodies {
		bp := breakpoints(body)
		if len(bp) != 2 || bp[0] != "system" || bp[1] != lastBlock(body) {
			t.Errorf("step %d breakpoints = %v, want [system %s]", i+1, bp, lastBlock(body))
		}
		if sys := body["system"].([]any)[0].(map[string]any)["text"]; sys != "SYSTEM" {
			t.Errorf("step %d system = %v", i+1, sys)
		}
	}
	lastOfFirst := g.bodies[2]

	// Next turn: the history the first one left, plus a new prompt.
	for _, st := range res.Steps {
		history = append(history, st.Messages...)
	}
	history = append(history, ai.UserText("second"))
	g.mu.Lock()
	g.bodies, g.script = nil, nil
	g.mu.Unlock()
	run()
	body := g.bodies[0]
	if bp := breakpoints(body); len(bp) != 2 || bp[1] != lastBlock(body) {
		t.Errorf("second turn breakpoints = %v, want [system %s]", bp, lastBlock(body))
	}
	// Everything the first turn's last request sent is resent unchanged
	// (anchors aside), so the second turn reads it from the cache.
	prev, next := stripped(lastOfFirst), stripped(body)
	if len(next) != len(prev)+2 { // + the answer and the new prompt
		t.Fatalf("second turn sends %d messages, want %d", len(next), len(prev)+2)
	}
	for i := range prev {
		if prev[i] != next[i] {
			t.Errorf("message %d changed between turns:\n%s\n%s", i, prev[i], next[i])
		}
	}
}

// stripped is each message's JSON with the cache anchors removed.
func stripped(body map[string]any) []string {
	var out []string
	for _, m := range body["messages"].([]any) {
		content, _ := m.(map[string]any)["content"].([]any)
		for _, b := range content {
			delete(b.(map[string]any), "cache_control")
		}
		raw, _ := json.Marshal(m)
		out = append(out, string(raw))
	}
	return out
}

// Providers that cache on their own get the plain request: no system moved
// into the messages, no cache_control anywhere.
func TestNonAnthropicRequestsAreLeftAlone(t *testing.T) {
	cfg := config.Config{Provider: "xai", ModelID: "grok-4"}
	opts := cachedOptions(ai.Options{System: "S", Messages: []provider.Message{ai.UserText("hi")}}, cfg, "sess-1")
	if opts.System != "S" || opts.PrepareStep != nil || len(opts.Messages) != 1 {
		t.Fatalf("xai options were reshaped: %+v", opts)
	}
	if opts.Headers["x-grok-conv-id"] != "sess-1" {
		t.Errorf("xai requests must pin the conversation's server: headers = %v", opts.Headers)
	}
	if opts.Messages[0].(provider.UserMessage).ProviderOptions != nil {
		t.Fatal("xai prompt carries provider options")
	}
	for _, p := range []string{"openai", "google", "deepseek", "kimi", "openrouter", "bedrock"} {
		if AnthropicCaching(p) {
			t.Errorf("%s must not get Anthropic caching", p)
		}
	}
	if !AnthropicCaching("anthropic") {
		t.Error("anthropic must get Anthropic caching")
	}
}

func TestMoveAnthropicCacheTailKeepsOtherOptions(t *testing.T) {
	msgs := WithAnthropicCaching("S", []provider.Message{
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "a"}},
			ProviderOptions: provider.ProviderOptions{"openai": {"x": 1}}},
		ai.UserText("b"),
	})
	moved := MoveAnthropicCacheTail(append(msgs, ai.UserText("c")))
	if moved[0].(provider.SystemMessage).ProviderOptions.Get("anthropic") == nil {
		t.Error("the system anchor was stripped")
	}
	if moved[2].(provider.UserMessage).ProviderOptions != nil {
		t.Error("the old tail anchor survived")
	}
	if o := moved[1].(provider.UserMessage).ProviderOptions; o.Get("openai") == nil {
		t.Error("another provider's options were lost")
	}
	if moved[3].(provider.UserMessage).ProviderOptions.Get("anthropic") == nil {
		t.Error("the new tail has no anchor")
	}
	// The input is not mutated: a run's own history stays clean.
	if msgs[2].(provider.UserMessage).ProviderOptions.Get("anthropic") == nil {
		t.Error("MoveAnthropicCacheTail mutated its input")
	}
}

// Claude through OpenRouter's chat completions and Bedrock's Claude models
// cache only past an explicit mark too, each under its own provider key.
func TestExplicitCachingBeyondAnthropic(t *testing.T) {
	for _, c := range []struct {
		cfg config.Config
		key string
	}{
		{config.Config{Provider: "openrouter", ModelID: "anthropic/claude-sonnet-4.5"}, "openrouter"},
		{config.Config{Provider: "bedrock", ModelID: "us.anthropic.claude-sonnet-4-5-v1:0"}, "bedrock"},
		{config.Config{Provider: "openrouter", ModelID: "deepseek/deepseek-v4-flash"}, ""},
	} {
		opts := cachedOptions(ai.Options{System: "S", Messages: []provider.Message{ai.UserText("hi")}}, c.cfg, "conv")
		if c.key == "" {
			if opts.System != "S" || opts.PrepareStep != nil {
				t.Errorf("%s: reshaped a self-caching model", c.cfg.FullID())
			}
			continue
		}
		sys, ok := opts.Messages[0].(provider.SystemMessage)
		if !ok || opts.System != "" || sys.ProviderOptions.Get(c.key) == nil {
			t.Errorf("%s: system not anchored under %q: %+v", c.cfg.FullID(), c.key, opts.Messages[0])
		}
		if opts.Messages[1].(provider.UserMessage).ProviderOptions.Get(c.key) == nil || opts.PrepareStep == nil {
			t.Errorf("%s: no moving tail anchor", c.cfg.FullID())
		}
		if opts.MaxOutputTokens != nil {
			t.Errorf("%s: output cap is Anthropic's alone", c.cfg.FullID())
		}
	}
}

// OpenAI-shaped requests carry the session as prompt_cache_key, which
// routes them to the same cache (as x-grok-conv-id does on xAI); a key the
// caller set wins.
func TestOpenAIRequestsCarryAPromptCacheKey(t *testing.T) {
	opts := cachedOptions(ai.Options{}, config.Config{Provider: "openai", ModelID: "gpt-6.1-sol"}, "sess-9")
	if opts.ProviderOptions.Get("openai")["promptCacheKey"] != "sess-9" {
		t.Errorf("provider options = %v", opts.ProviderOptions)
	}
	own := ai.Options{ProviderOptions: provider.ProviderOptions{"openai": {"promptCacheKey": "mine"}}}
	if got := cachedOptions(own, config.Config{Provider: "openai"}, "sess-9").ProviderOptions.Get("openai")["promptCacheKey"]; got != "mine" {
		t.Errorf("caller's key replaced: %v", got)
	}
	if own.ProviderOptions.Get("openai")["promptCacheKey"] != "mine" {
		t.Error("the caller's options were mutated")
	}
}
