package session

import (
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

func TestPartsFlattensUserAndAssistant(t *testing.T) {
	if parts := Parts(user("hello")); len(parts) != 1 {
		t.Fatalf("user → %d parts", len(parts))
	} else if p, ok := parts[0].(ReplayUser); !ok || p.Text != "hello" {
		t.Errorf("user part = %#v", parts[0])
	}
	if parts := Parts(assistant("hi")); len(parts) != 1 {
		t.Fatalf("assistant → %d parts", len(parts))
	} else if p, ok := parts[0].(ReplayAssistant); !ok || p.Text != "hi" {
		t.Errorf("assistant part = %#v", parts[0])
	}
}

// Prose written before a tool call has to stay above its row, or a replayed
// turn reads in the wrong order.
func TestPartsKeepsProseBeforeToolCalls(t *testing.T) {
	msg := provider.AssistantMessage{Content: []provider.AssistantPart{
		provider.TextPart{Text: "Let me look."},
		provider.ToolCallPart{ToolCallID: "c1", ToolName: "read",
			Input: map[string]any{"path": "a.go"}},
		provider.TextPart{Text: "Found it."},
	}}
	parts := Parts(msg)
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3: %#v", len(parts), parts)
	}
	if p, ok := parts[0].(ReplayAssistant); !ok || p.Text != "Let me look." {
		t.Errorf("part 0 = %#v", parts[0])
	}
	call, ok := parts[1].(ReplayToolCall)
	if !ok || call.Name != "read" || call.Input["path"] != "a.go" {
		t.Errorf("part 1 = %#v", parts[1])
	}
	if p, ok := parts[2].(ReplayAssistant); !ok || p.Text != "Found it." {
		t.Errorf("part 2 = %#v", parts[2])
	}
}

// Signed reasoning blobs are for the model, not the reader.
func TestPartsSkipsReasoning(t *testing.T) {
	msg := provider.AssistantMessage{Content: []provider.AssistantPart{
		provider.ReasoningPart{Text: "internal monologue"},
		provider.TextPart{Text: "answer"},
	}}
	parts := Parts(msg)
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1: %#v", len(parts), parts)
	}
	if p := parts[0].(ReplayAssistant); p.Text != "answer" {
		t.Errorf("part = %q", p.Text)
	}
}

func TestToolResultsIndexesByCallID(t *testing.T) {
	s := &Session{Messages: []provider.Message{
		toolOK("c1", "read", "file contents"),
		provider.ToolMessage{Content: []provider.ToolPart{
			provider.ToolResultPart{ToolCallID: "c2", ToolName: "bash",
				Output: provider.ToolOutputErrorText{Value: "exit 1"}},
		}},
		provider.ToolMessage{Content: []provider.ToolPart{
			provider.ToolResultPart{ToolCallID: "c3", ToolName: "x",
				Output: provider.ToolOutputExecutionDenied{Reason: "nope"}},
		}},
	}}
	got := s.ToolResults()
	if got["c1"].Text != "file contents" || got["c1"].IsError {
		t.Errorf("c1 = %+v", got["c1"])
	}
	if got["c2"].Text != "exit 1" || !got["c2"].IsError {
		t.Errorf("c2 = %+v", got["c2"])
	}
	if !got["c3"].IsError {
		t.Errorf("a denied call should read as an error: %+v", got["c3"])
	}
}

// A non-object input must not crash a replay; the row just shows no summary.
func TestPartsToleratesNonObjectToolInput(t *testing.T) {
	msg := provider.AssistantMessage{Content: []provider.AssistantPart{
		provider.ToolCallPart{ToolCallID: "c1", ToolName: "x", Input: []any{1, 2}},
	}}
	parts := Parts(msg)
	if len(parts) != 1 {
		t.Fatalf("got %d parts", len(parts))
	}
	if call := parts[0].(ReplayToolCall); call.Input != nil {
		t.Errorf("expected nil input, got %#v", call.Input)
	}
}
