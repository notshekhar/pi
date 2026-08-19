package tui

import "testing"

// typeText feeds characters through the editor the way a keyboard would.
func typeText(e *Editor, s string) {
	for _, r := range s {
		e.Handle(Key{Kind: KeyRune, Rune: r}, "")
	}
}

func press(e *Editor, kind KeyKind) {
	e.Handle(Key{Kind: kind}, "")
}

// A run of typing undoes as one unit. Undoing a keystroke at a time is
// technically correct and useless.
func TestUndoCoalescesTyping(t *testing.T) {
	e := NewEditor()
	typeText(e, "hello")
	if e.Value() != "hello" {
		t.Fatalf("value = %q", e.Value())
	}
	press(e, KeyCtrlZ)
	if e.Value() != "" {
		t.Errorf("one undo left %q, want the whole run gone", e.Value())
	}
}

// A newline is structural, so it breaks the run.
func TestUndoBreaksOnStructuralEdits(t *testing.T) {
	e := NewEditor()
	typeText(e, "first")
	e.Handle(Key{Kind: KeyEnter, Alt: true}, "")
	typeText(e, "second")

	press(e, KeyCtrlZ) // the second run
	if got := e.Value(); got != "first\n" {
		t.Errorf("after one undo = %q, want %q", got, "first\n")
	}
	press(e, KeyCtrlZ) // the newline
	if got := e.Value(); got != "first" {
		t.Errorf("after two undos = %q, want %q", got, "first")
	}
}

func TestRedo(t *testing.T) {
	e := NewEditor()
	typeText(e, "hello")
	press(e, KeyCtrlZ)
	if e.Value() != "" {
		t.Fatalf("undo failed: %q", e.Value())
	}
	press(e, KeyCtrlY)
	if e.Value() != "hello" {
		t.Errorf("redo = %q, want hello", e.Value())
	}
}

// A new edit abandons the redo branch, as in every other editor.
func TestNewEditDropsRedo(t *testing.T) {
	e := NewEditor()
	typeText(e, "hello")
	press(e, KeyCtrlZ)
	typeText(e, "world")
	press(e, KeyCtrlY)
	if e.Value() != "world" {
		t.Errorf("redo resurrected a dead branch: %q", e.Value())
	}
}

func TestUndoOnEmptyStackIsHarmless(t *testing.T) {
	e := NewEditor()
	press(e, KeyCtrlZ)
	press(e, KeyCtrlY)
	if e.Value() != "" {
		t.Errorf("value = %q", e.Value())
	}
}

// Submitting replaces the buffer wholesale; undo must not reach back through
// a prompt that has already been sent.
func TestSubmitResetsUndo(t *testing.T) {
	e := NewEditor()
	typeText(e, "sent")
	if submit, _, _ := e.Handle(Key{Kind: KeyEnter}, ""); submit != "sent" {
		t.Fatalf("submit = %q", submit)
	}
	press(e, KeyCtrlZ)
	if e.Value() != "" {
		t.Errorf("undo resurrected a submitted prompt: %q", e.Value())
	}
}

func TestUndoRestoresCursor(t *testing.T) {
	e := NewEditor()
	typeText(e, "hello")
	press(e, KeyCtrlZ)
	// After undoing to an empty buffer the cursor must be inside it, or the
	// next keystroke indexes out of range.
	if e.row != 0 || e.col != 0 {
		t.Errorf("cursor at %d,%d", e.row, e.col)
	}
	typeText(e, "x")
	if e.Value() != "x" {
		t.Errorf("editing after undo = %q", e.Value())
	}
}

func TestBackspaceIsUndoable(t *testing.T) {
	e := NewEditor()
	typeText(e, "hello")
	press(e, KeyBackspace)
	press(e, KeyBackspace)
	if e.Value() != "hel" {
		t.Fatalf("value = %q", e.Value())
	}
	press(e, KeyCtrlZ)
	if e.Value() != "hello" {
		t.Errorf("undo after backspace = %q, want hello", e.Value())
	}
}

// The stack is bounded, so a long-lived editor does not leak.
func TestUndoStackIsBounded(t *testing.T) {
	var u undoStack
	for i := 0; i < maxUndo*3; i++ {
		u.record(editState{}, kindNone)
	}
	if len(u.past) > maxUndo {
		t.Errorf("stack grew to %d, cap is %d", len(u.past), maxUndo)
	}
}
