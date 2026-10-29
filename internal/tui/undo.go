package tui

import "maps"

// The editor's undo stack: loop's undo-stack.ts. Snapshots, not operations —
// a prompt is a handful of lines, so copying it costs nothing, and copying
// cannot get an inverse subtly wrong. What groups keystrokes into one undo
// unit is the editor's job (fish-style: a typed word is one unit, each space
// opens the next); the stack only stores what it is handed. There is no
// redo, as in loop.

// maxUndo bounds the stack. A prompt is small, but an unbounded stack in a
// long-lived process is still a leak.
const maxUndo = 100

// editState is a point the editor can return to: the text, the cursor, and
// the paste registry the text's markers point into.
type editState struct {
	lines        []string
	row, col     int
	pastes       map[int]string
	pasteCounter int
}

type undoStack struct{ past []editState }

func (u *undoStack) push(s editState) {
	u.past = append(u.past, s)
	if len(u.past) > maxUndo {
		u.past = u.past[len(u.past)-maxUndo:]
	}
}

func (u *undoStack) pop() (editState, bool) {
	if len(u.past) == 0 {
		return editState{}, false
	}
	last := u.past[len(u.past)-1]
	u.past = u.past[:len(u.past)-1]
	return last, true
}

func (u *undoStack) reset() { u.past = nil }

// snapshot captures the editor's current state, detached from it.
func (e *Editor) snapshot() editState {
	return editState{
		lines:        append([]string{}, e.lines...),
		row:          e.row,
		col:          e.col,
		pastes:       maps.Clone(e.pastes),
		pasteCounter: e.pasteCounter,
	}
}

// restore puts the editor back to a captured state.
func (e *Editor) restore(s editState) {
	e.lines = s.lines
	e.row = min(s.row, max(len(e.lines)-1, 0))
	e.col = min(s.col, len(e.lines[e.row]))
	e.pastes = s.pastes
	if e.pastes == nil {
		e.pastes = map[int]string{}
	}
	e.pasteCounter = s.pasteCounter
}
