package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
)

// The compaction summary runs at the model's LOWEST reasoning, whatever the
// session uses (loop's summary-reasoning.test.ts). A model that refuses its
// lowest level is asked once more at the session's own.

func TestLowestThinkingLevel(t *testing.T) {
	for model, want := range map[string]string{
		"claude-opus-5":         "off",
		"gpt-5.5":               "off",
		"grok-build-0.1":        "off",
		"deepseek-chat":         "off",
		"claude-fable-5":        "minimal",
		"claude-mythos-preview": "minimal",
	} {
		if got := lowestThinkingLevel(model); got != want {
			t.Errorf("lowestThinkingLevel(%q) = %q, want %q", model, got, want)
		}
	}
}

// compactOnHigh fills a session on the fake gateway with the session at high
// reasoning, compacts it, and returns the summary requests' thinking blocks.
func compactOnHigh(t *testing.T, summaryReplies ...string) ([]any, error) {
	t.Helper()
	r, g, _ := newTurn(t)
	r.Config.Reasoning = provider.ReasoningHigh
	for i := 0; i < 3; i++ {
		if _, err := r.RunTurn(context.Background(), TurnOptions{Input: strings.Repeat("history ", 400)}); err != nil {
			t.Fatal(err)
		}
	}
	turns := len(g.requests())
	g.mu.Lock()
	for len(g.script) < turns {
		g.script = append(g.script, textStep)
	}
	g.script = append(g.script, summaryReplies...)
	g.mu.Unlock()
	_, err := Compact(context.Background(), r, CompactOptions{Manual: true})
	var thinking []any
	for _, body := range g.requests()[turns:] {
		thinking = append(thinking, body["thinking"])
	}
	return thinking, err
}

func TestSummaryRunsWithReasoningOff(t *testing.T) {
	thinking, err := compactOnHigh(t)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{map[string]any{"type": "disabled"}}
	if !reflect.DeepEqual(thinking, want) {
		t.Errorf("summary thinking = %v, want %v", thinking, want)
	}
}

func TestSummaryFallsBackToTheSessionLevelWhenRefused(t *testing.T) {
	refused := `HTTP 400 {"type":"error","error":{"type":"invalid_request_error","message":"thinking.type.disabled is not supported for this model"}}`
	thinking, err := compactOnHigh(t, refused)
	if err != nil {
		t.Fatal(err)
	}
	if len(thinking) != 2 {
		t.Fatalf("summary requests = %d, want 2: %v", len(thinking), thinking)
	}
	if !reflect.DeepEqual(thinking[0], map[string]any{"type": "disabled"}) {
		t.Errorf("first try thinking = %v, want disabled", thinking[0])
	}
	if th, _ := thinking[1].(map[string]any); th["type"] != "adaptive" {
		t.Errorf("fallback thinking = %v, want the session's adaptive thinking", thinking[1])
	}
}

func TestSummaryRefusalNotRetriedForOtherErrors(t *testing.T) {
	badKey := `HTTP 401 {"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
	thinking, err := compactOnHigh(t, badKey)
	if err != nil {
		t.Fatalf("a failed summary should truncate, got %v", err)
	}
	if len(thinking) != 1 {
		t.Errorf("summary requests = %d, want 1", len(thinking))
	}
}
