package tui

// The editor's undo stack.
//
// Snapshot-based rather than operation-based: a prompt is a handful of lines,
// so copying it costs nothing, and an operation log has to get every inverse
// right — including the ones that interact, like a completion accepted over a
// half-typed word. Copying cannot be subtly wrong.
//
// Edits COALESCE BY KIND: a run of typed characters undoes as one word
// rather than one keystroke at a time, which is what people expect and what
// makes the feature worth having. The kind matters — typing and deleting are
// both repeatable, but merging them means backspacing once and undoing
// throws away the word you had just typed. Structural edits (a newline, a
// kill, a completion) coalesce with nothing.

// maxUndo bounds the stack. A prompt is small, but an unbounded stack in a
// long-lived process is still a leak.
const maxUndo = 100

// editState is a point the editor can return to.
type editState struct {
	lines [][]rune
	row   int
	col   int
}

// undoStack holds the states behind and ahead of the cursor.
type undoStack struct {
	past   []editState
	future []editState
	// run names the kind of edit currently coalescing, or "" for none.
	run editKind
}

// editKind distinguishes runs that may merge. Structural edits use
// kindNone, which never coalesces.
type editKind string

const (
	kindNone   editKind = ""
	kindInsert editKind = "insert"
	kindDelete editKind = "delete"
)

// record pushes the state BEFORE an edit.
//
// An edit of the same kind as the one in progress folds into it; anything
// else opens a new entry.
func (u *undoStack) record(s editState, kind editKind) {
	if kind != kindNone && kind == u.run && len(u.past) > 0 {
		// Already inside a run of this kind: the entry that opened it is the
		// one worth returning to.
		u.future = nil
		return
	}
	u.past = append(u.past, s)
	if len(u.past) > maxUndo {
		u.past = u.past[len(u.past)-maxUndo:]
	}
	u.run = kind
	// Any new edit abandons the redo branch, as in every other editor.
	u.future = nil
}

// undo steps back, returning the state to restore.
func (u *undoStack) undo(current editState) (editState, bool) {
	if len(u.past) == 0 {
		return editState{}, false
	}
	last := u.past[len(u.past)-1]
	u.past = u.past[:len(u.past)-1]
	u.future = append(u.future, current)
	u.run = kindNone
	return last, true
}

// redo steps forward again.
func (u *undoStack) redo(current editState) (editState, bool) {
	if len(u.future) == 0 {
		return editState{}, false
	}
	next := u.future[len(u.future)-1]
	u.future = u.future[:len(u.future)-1]
	u.past = append(u.past, current)
	u.run = kindNone
	return next, true
}

// reset clears the stack, for when the buffer is replaced wholesale.
func (u *undoStack) reset() {
	u.past, u.future, u.run = nil, nil, kindNone
}

// state captures the editor's current position.
func (e *Editor) state() editState {
	return editState{lines: e.snapshot(), row: e.row, col: e.col}
}

// restore puts the editor back to a captured state.
func (e *Editor) restore(s editState) {
	e.lines = s.lines
	e.row = min(s.row, max(len(e.lines)-1, 0))
	e.col = min(s.col, len(e.lines[e.row]))
	e.menu = nil
}
