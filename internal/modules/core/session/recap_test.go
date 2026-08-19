package session

import (
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

func TestRecapCountsTheSession(t *testing.T) {
	s := &Session{Messages: []provider.Message{
		user("do a thing"),
		toolCall("c1", "read", map[string]any{"path": "/repo/a.go"}),
		toolCall("c2", "edit", map[string]any{"path": "/repo/b.go"}),
		toolCall("c3", "bash", map[string]any{"command": "go test ./...\nsecond line"}),
		toolCall("c4", "read", map[string]any{"path": "/repo/c.go"}),
		assistant("done"),
		user("another thing"),
	}}
	r := s.Recap("/repo")

	if r.Turns != 2 {
		t.Errorf("turns = %d, want 2", r.Turns)
	}
	if r.Tools["read"] != 2 || r.Tools["edit"] != 1 || r.Tools["bash"] != 1 {
		t.Errorf("tools = %v", r.Tools)
	}
	// Paths are reported relative to the working directory.
	if len(r.Changed) != 1 || r.Changed[0] != "b.go" {
		t.Errorf("changed = %v", r.Changed)
	}
	if len(r.Read) != 2 || r.Read[0] != "a.go" {
		t.Errorf("read = %v", r.Read)
	}
	// Only a command's first line: the rest is rarely what you are recalling.
	if len(r.Commands) != 1 || r.Commands[0] != "go test ./..." {
		t.Errorf("commands = %v", r.Commands)
	}
}

// A file that was changed is not also listed as merely read — the stronger
// fact is the interesting one.
func TestRecapPrefersChangedOverRead(t *testing.T) {
	s := &Session{Messages: []provider.Message{
		toolCall("c1", "read", map[string]any{"path": "/repo/a.go"}),
		toolCall("c2", "edit", map[string]any{"path": "/repo/a.go"}),
	}}
	r := s.Recap("/repo")
	if len(r.Read) != 0 {
		t.Errorf("read = %v, want empty", r.Read)
	}
	if len(r.Changed) != 1 || r.Changed[0] != "a.go" {
		t.Errorf("changed = %v", r.Changed)
	}
}

func TestRecapOfAnEmptySession(t *testing.T) {
	r := (&Session{}).Recap("/repo")
	if r.Turns != 0 || len(r.Tools) != 0 || len(r.Changed) != 0 {
		t.Errorf("recap = %+v", r)
	}
}

// A path outside the working directory stays absolute rather than becoming a
// pile of `../`.
func TestRecapKeepsOutsidePathsAbsolute(t *testing.T) {
	s := &Session{Messages: []provider.Message{
		toolCall("c1", "read", map[string]any{"path": "/elsewhere/x.go"}),
	}}
	if got := s.Recap("/repo").Read; len(got) != 1 || got[0] != "/elsewhere/x.go" {
		t.Errorf("read = %v", got)
	}
}
