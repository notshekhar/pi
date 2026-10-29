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

func undo(e *Editor) { press(e, KeyCtrlMinus) }

// A typed word undoes as one unit (loop's fish-style coalescing).
func TestUndoCoalescesTyping(t *testing.T) {
	e := NewEditor()
	typeText(e, "hello")
	undo(e)
	if e.Value() != "" {
		t.Errorf("one undo left %q, want the whole word gone", e.Value())
	}
}

// Each space opens a new unit that takes the following word with it.
func TestUndoTakesASpaceWithTheWordAfterIt(t *testing.T) {
	e := NewEditor()
	typeText(e, "hello world")
	undo(e)
	if e.Value() != "hello" {
		t.Errorf("after one undo = %q, want %q", e.Value(), "hello")
	}
	undo(e)
	if e.Value() != "" {
		t.Errorf("after two undos = %q", e.Value())
	}
}

// A newline is structural, so it breaks the run.
func TestUndoBreaksOnStructuralEdits(t *testing.T) {
	e := NewEditor()
	typeText(e, "first")
	e.Handle(Key{Kind: KeyEnter, Shift: true}, "")
	typeText(e, "second")

	undo(e) // the second run
	if got := e.Value(); got != "first\n" {
		t.Errorf("after one undo = %q, want %q", got, "first\n")
	}
	undo(e) // the newline
	if got := e.Value(); got != "first" {
		t.Errorf("after two undos = %q, want %q", got, "first")
	}
}

// cmd+z under the kitty protocol is undo too.
func TestSuperZUndoes(t *testing.T) {
	e := NewEditor()
	typeText(e, "hello")
	e.Handle(Key{Kind: KeyRune, Rune: 'z', Super: true}, "")
	if e.Value() != "" {
		t.Errorf("cmd+z left %q", e.Value())
	}
}

func TestUndoOnEmptyStackIsHarmless(t *testing.T) {
	e := NewEditor()
	undo(e)
	if e.Value() != "" {
		t.Errorf("value = %q", e.Value())
	}
}

// Submitting replaces the buffer wholesale; undo must not reach back through
// a prompt that has already been sent.
func TestSubmitResetsUndo(t *testing.T) {
	e := NewEditor()
	typeText(e, "sent")
	if submit, _ := e.Handle(Key{Kind: KeyEnter}, ""); submit != "sent" {
		t.Fatalf("submit = %q", submit)
	}
	undo(e)
	if e.Value() != "" {
		t.Errorf("undo resurrected a submitted prompt: %q", e.Value())
	}
}

func TestUndoRestoresCursor(t *testing.T) {
	e := NewEditor()
	typeText(e, "hello")
	undo(e)
	if e.row != 0 || e.col != 0 {
		t.Errorf("cursor at %d,%d", e.row, e.col)
	}
	typeText(e, "x")
	if e.Value() != "x" {
		t.Errorf("editing after undo = %q", e.Value())
	}
}

// Every backspace is its own unit, as in loop.
func TestBackspaceIsUndoable(t *testing.T) {
	e := NewEditor()
	typeText(e, "hello")
	press(e, KeyBackspace)
	press(e, KeyBackspace)
	if e.Value() != "hel" {
		t.Fatalf("value = %q", e.Value())
	}
	undo(e)
	if e.Value() != "hell" {
		t.Errorf("undo after backspace = %q, want hell", e.Value())
	}
}

// The stack is bounded, so a long-lived editor does not leak.
func TestUndoStackIsBounded(t *testing.T) {
	var u undoStack
	for i := 0; i < maxUndo*3; i++ {
		u.push(editState{})
	}
	if len(u.past) > maxUndo {
		t.Errorf("stack grew to %d, cap is %d", len(u.past), maxUndo)
	}
}
