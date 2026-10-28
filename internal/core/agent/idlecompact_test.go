package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/tools"
)

// A turn that leaves the context over 70% full is summarized once the
// session goes idle — as one cache-reading request (the turn's own system,
// tools and anchors, the instructions appended, no tool allowed) — and the
// summary is saved as an idle compaction. A new turn cancels a pending one.
func TestIdleCompactionReadsTheCachedConversation(t *testing.T) {
	full := strings.Replace(textStep, `"input_tokens":10`, `"input_tokens":900`, 1)
	g := &gateway{script: []string{full, full, full}}
	cfg := withGateway(t, "anthropic", g)
	cfg.CWD = t.TempDir()
	// The context is past the auto-compact threshold too; only idle runs here.
	if err := config.SetSetting("autoCompactThreshold", 0); err != nil {
		t.Fatal(err)
	}
	SetIdleCompactDelayForTests(150 * time.Millisecond)
	t.Cleanup(func() { SetIdleCompactDelayForTests(0) })

	r := &Run{Config: cfg, Session: session.New(cfg.FullID(), cfg.CWD), Tools: &tools.Context{CWD: cfg.CWD}}
	var mu sync.Mutex
	var events []string
	var end CompactEndEvent
	ended := make(chan struct{}, 1)
	emit := func(name string, data any) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, name)
		if name == "compact-end" {
			end = data.(CompactEndEvent)
			ended <- struct{}{}
		}
	}
	turn := func(input string) {
		if _, err := r.RunTurn(context.Background(), TurnOptions{Input: input, Emit: emit}); err != nil {
			t.Fatal(err)
		}
	}
	turn(strings.Repeat("first request ", 200))
	if !HasPendingIdleCompact(r.Session.ID) {
		t.Fatal("a turn leaving the context 90% full scheduled no idle compaction")
	}
	turn(strings.Repeat("second request ", 200)) // cancels the first and schedules anew
	turn("third")
	mu.Lock()
	for _, e := range events {
		if strings.HasPrefix(e, "compact") {
			t.Fatalf("compaction ran while the session was busy: %v", events)
		}
	}
	mu.Unlock()
	turns := len(g.requests())

	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("idle compaction never ran")
	}
	if end.Error != "" || end.Aborted || end.Summary == "" || end.CutAt == 0 {
		t.Fatalf("compact-end = %+v", end)
	}
	bodies := g.requests()
	if len(bodies) != turns+1 {
		t.Fatalf("summary requests = %d, want 1", len(bodies)-turns)
	}
	body := bodies[turns]
	if tc, _ := body["tool_choice"].(map[string]any); tc["type"] != "none" {
		t.Errorf("tool_choice = %v, want none", body["tool_choice"])
	}
	if len(body["tools"].([]any)) == 0 {
		t.Error("the summary request dropped the tools — the cached prefix would miss")
	}
	if bp := breakpoints(body); len(bp) != 2 || bp[0] != "system" {
		t.Errorf("breakpoints = %v", bp)
	}
	// Everything the last turn sent is resent unchanged: a cache read.
	prev, next := stripped(bodies[turns-1]), stripped(body)
	for i := range prev {
		if prev[i] != next[i] {
			t.Errorf("message %d changed:\n%s\n%s", i, prev[i], next[i])
		}
	}
	if !strings.Contains(next[len(next)-1], "gone idle") {
		t.Errorf("last message is not the idle instructions: %s", next[len(next)-1])
	}
	c, ok := r.Session.LatestCompact()
	if !ok || string(c.Extra["trigger"]) != `"idle"` {
		t.Errorf("no idle compaction entry saved: %+v", c)
	}
}

func TestIdleCompactionOnlyWhereACacheLapses(t *testing.T) {
	for _, c := range []struct {
		cfg  config.Config
		want bool
	}{
		{config.Config{Provider: "anthropic", ModelID: "claude-sonnet-4-5"}, true},
		{config.Config{Provider: "xai", ModelID: "grok-4.7"}, true},
		{config.Config{Provider: "openai", ModelID: "gpt-5"}, true},
		{config.Config{Provider: "openrouter", ModelID: "anthropic/claude-sonnet-4.5"}, true},
		{config.Config{Provider: "bedrock", ModelID: "us.anthropic.claude-sonnet-4-5"}, true},
		{config.Config{Provider: "openrouter", ModelID: "deepseek/deepseek-v4-flash"}, false},
		{config.Config{Provider: "deepseek", ModelID: "deepseek-v4-flash"}, false},
		{config.Config{Provider: "google", ModelID: "gemini-3-pro"}, false},
	} {
		if _, ok := IdleCompactDelay(c.cfg); ok != c.want {
			t.Errorf("%s: idle compaction = %v, want %v", c.cfg.FullID(), ok, c.want)
		}
	}
	if d, _ := IdleCompactDelay(config.Config{Provider: "anthropic"}); d != 4*time.Minute+15*time.Second {
		t.Errorf("delay = %v, want 4m15s", d)
	}
}
