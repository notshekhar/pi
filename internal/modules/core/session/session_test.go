package session

import (
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

func user(text string) provider.Message {
	return provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: text}}}
}

func assistant(text string) provider.Message {
	return provider.AssistantMessage{Content: []provider.AssistantPart{provider.TextPart{Text: text}}}
}

func toolResult(text string) provider.Message {
	return provider.ToolMessage{Content: []provider.ToolPart{provider.ToolResultPart{
		ToolCallID: "1", ToolName: "read",
		Output: provider.ToolOutputText{Value: text},
	}}}
}

// tempSession is a real session in a scratch database.
func tempSession(t *testing.T) *Session {
	t.Helper()
	withSessionHome(t)
	s, err := Create("m", "/repo")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTextRendersBothSides(t *testing.T) {
	s := tempSession(t)
	if err := s.Add(user("hello"), assistant("hi there")); err != nil {
		t.Fatal(err)
	}
	got := s.Text()
	for _, want := range []string{"## User", "hello", "## Assistant", "hi there"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestLastAssistantSkipsTrailingToolTurns(t *testing.T) {
	s := tempSession(t)
	if err := s.Add(assistant("first"), user("more"), assistant("second"), toolResult("output")); err != nil {
		t.Fatal(err)
	}
	if got := s.LastAssistant(); got != "second" {
		t.Errorf("LastAssistant = %q, want %q", got, "second")
	}
}

func TestLastAssistantEmptyWhenNone(t *testing.T) {
	s := tempSession(t)
	if err := s.Add(user("only me")); err != nil {
		t.Fatal(err)
	}
	if got := s.LastAssistant(); got != "" {
		t.Errorf("LastAssistant = %q, want empty", got)
	}
}

// Tool results are usually the bulk of a long session; a context estimate
// that ignored them would be reassuring and wrong.
func TestCharsCountsToolResults(t *testing.T) {
	s := tempSession(t)
	if err := s.Add(user("hi")); err != nil {
		t.Fatal(err)
	}
	withoutTool := s.Chars()
	if err := s.Add(toolResult(strings.Repeat("x", 500))); err != nil {
		t.Fatal(err)
	}
	if got := s.Chars() - withoutTool; got != 500 {
		t.Errorf("tool result contributed %d chars, want 500", got)
	}
}

func TestReplaceRewritesHistoryAndTranscript(t *testing.T) {
	s := tempSession(t)
	if err := s.Add(user("one"), assistant("two"), user("three")); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(user("summary")); err != nil {
		t.Fatal(err)
	}
	if len(s.Messages) != 1 {
		t.Fatalf("expected 1 message after replace, got %d", len(s.Messages))
	}
	// The STORED conversation must be rewritten too, not appended to — a
	// compacted session whose history still holds the old messages would grow
	// forever and contradict what the model sees.
	stored, err := Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Messages) != 1 {
		t.Fatalf("stored %d messages after replace, want 1", len(stored.Messages))
	}
	if !strings.Contains(stored.Text(), "summary") {
		t.Errorf("stored conversation missing the replacement:\n%s", stored.Text())
	}
	if strings.Contains(stored.Text(), "three") {
		t.Errorf("stored conversation kept replaced content:\n%s", stored.Text())
	}
}

// Replace must copy its input: keeping the caller's slice would let a later
// append to it mutate the session's history behind its back.
func TestReplaceCopiesInput(t *testing.T) {
	s := tempSession(t)
	msgs := []provider.Message{user("a")}
	if err := s.Replace(msgs...); err != nil {
		t.Fatal(err)
	}
	msgs[0] = user("mutated")
	if got := s.Text(); strings.Contains(got, "mutated") {
		t.Errorf("session aliased the caller's slice:\n%s", got)
	}
}

func TestResetClearsBoth(t *testing.T) {
	s := tempSession(t)
	if err := s.Add(user("gone")); err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if len(s.Messages) != 0 {
		t.Errorf("messages survived reset: %d", len(s.Messages))
	}
	stored, err := Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Messages) != 0 {
		t.Errorf("the stored conversation survived reset: %d messages", len(stored.Messages))
	}
}
