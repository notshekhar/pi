package tui

import (
	"fmt"
	"strings"
	"unicode"
)

// Editor is a multiline input with history, word navigation, and a kill
// ring. Rune-indexed internally; the view is produced by View().
type Editor struct {
	lines    [][]rune
	row, col int
	history  []string
	histIdx  int // len(history) when editing the live buffer
	stash    [][]rune
	kill     string
	complete Completer
	menu     *Menu
	undo     undoStack
	// Theme styles the prompt glyph and completion menu. Nil renders with
	// the default palette rather than unstyled.
	Theme     *Theme
	dirtyHint bool
}

// Completer finds completion candidates for the text before the cursor.
type Completer func(word string) []Item

func NewEditor() *Editor {
	return &Editor{lines: [][]rune{{}}, histIdx: 0}
}

// SetCompleter wires the @/slash completion source.
func (e *Editor) SetCompleter(c Completer) { e.complete = c }

// Value is the current text.
func (e *Editor) Value() string {
	var b strings.Builder
	for i, l := range e.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(string(l))
	}
	return b.String()
}

// SetValue replaces the buffer (used by history recall).
func (e *Editor) SetValue(s string) {
	parts := strings.Split(s, "\n")
	e.lines = make([][]rune, len(parts))
	for i, p := range parts {
		e.lines[i] = []rune(p)
	}
	e.row = len(e.lines) - 1
	e.col = len(e.lines[e.row])
}

func (e *Editor) clear() {
	e.undo.reset()
	e.lines = [][]rune{{}}
	e.row, e.col = 0, 0
	e.menu = nil
}

// PushHistory records a submitted line.
func (e *Editor) PushHistory(s string) {
	if s == "" {
		return
	}
	e.history = append(e.history, s)
	e.histIdx = len(e.history)
}

// Handle processes one key. It returns the submitted text (Enter) and
// whether the app should quit (Ctrl+C/D on empty buffer, Esc on empty).
// A Menu key press is consumed by the menu until it closes.
func (e *Editor) Handle(k Key, pasteText string) (submit string, quit bool, handled bool) {
	// The menu claims only the keys it owns — navigation, accept, cancel.
	// Everything else FALLS THROUGH to normal editing, which is what lets the
	// list narrow as the word is typed. Swallowing every key here meant the
	// menu opened and then froze the composer.
	if e.menu != nil {
		switch action, item := e.menu.Handle(k); action {
		case MenuCancel:
			e.menu = nil
			return "", false, true
		case MenuAccept:
			e.acceptCompletion(item)
			e.menu = nil
			return "", false, true
		case MenuAcceptAndSubmit:
			e.acceptCompletion(item)
			e.menu = nil
			text := strings.TrimSpace(e.Value())
			if text == "" {
				return "", false, true
			}
			e.PushHistory(text)
			e.clear()
			return text, false, true
		case MenuClaimed:
			return "", false, true
		}
	}

	// Rebindable actions first, so a custom binding wins over the built-in
	// meaning of the same key.
	switch k.Kind {
	case KeyFor(ActionUndo):
		if prev, ok := e.undo.undo(e.state()); ok {
			e.restore(prev)
		}
		return "", false, true
	case KeyFor(ActionRedo):
		if next, ok := e.undo.redo(e.state()); ok {
			e.restore(next)
		}
		return "", false, true
	}

	switch k.Kind {
	case KeyEnter:
		if k.Alt || k.Shift {
			e.newline()
			return "", false, true
		}
		text := strings.TrimSpace(e.Value())
		if text == "" {
			return "", false, true
		}
		e.PushHistory(text)
		e.clear()
		return text, false, true

	case KeyEsc:
		if e.Value() == "" {
			return "", true, true
		}
		e.clear()
		return "", false, true

	case KeyCtrlC:
		if e.Value() == "" {
			return "", true, true
		}
		e.clear()
		return "", false, true

	case KeyCtrlD:
		if e.Value() == "" {
			return "", true, true
		}

	case KeyRune:
		e.insertRune(k.Rune)
		e.maybeComplete()

	case KeyPaste:
		for _, r := range pasteText {
			if r == '\r' {
				continue
			}
			if r == '\n' {
				e.newline()
			} else {
				e.insertRune(r)
			}
		}

	case KeyBackspace:
		e.backspace()
		// Deleting narrows the word too, so the list has to follow it back
		// down — including all the way to closing when the trigger goes.
		e.maybeComplete()
	case KeyDelete:
		e.delete()
		e.maybeComplete()
	case KeyLeft:
		e.left(k.Alt)
	case KeyRight:
		e.right(k.Alt)
	case KeyHome, KeyCtrlA:
		e.col = 0
	case KeyEnd, KeyCtrlE:
		e.col = len(e.lines[e.row])
	case KeyUp:
		e.historyUp()
	case KeyDown:
		e.historyDown()
	case KeyCtrlK:
		e.kill = string(e.lines[e.row][e.col:])
		e.lines[e.row] = e.lines[e.row][:e.col]
	case KeyCtrlU:
		e.kill = string(e.lines[e.row][:e.col])
		e.lines[e.row] = e.lines[e.row][e.col:]
		e.col = 0
	case KeyCtrlW:
		e.killWord()
	default:
		return "", false, false
	}
	return "", false, true
}

func (e *Editor) insertRune(r rune) {
	e.undo.record(e.state(), kindInsert)
	e.insertRuneRaw(r)
}

// insertRuneRaw is insertRune without the undo entry, for bulk inserts that
// record one entry for the whole operation.
func (e *Editor) insertRuneRaw(r rune) {
	line := e.lines[e.row]
	line = append(line, 0)
	copy(line[e.col+1:], line[e.col:])
	line[e.col] = r
	e.lines[e.row] = line
	e.col++
}

func (e *Editor) newline() {
	e.undo.record(e.state(), kindNone)
	e.newlineRaw()
}

// newlineRaw is newline without the undo entry.
func (e *Editor) newlineRaw() {
	line := e.lines[e.row]
	next := append([]rune{}, line[e.col:]...)
	e.lines[e.row] = append([]rune{}, line[:e.col]...)
	e.lines = append(e.lines, nil)
	copy(e.lines[e.row+2:], e.lines[e.row+1:])
	e.lines[e.row+1] = next
	e.row++
	e.col = 0
}

func (e *Editor) backspace() {
	e.undo.record(e.state(), kindDelete)
	if e.col > 0 {
		line := e.lines[e.row]
		e.lines[e.row] = append(line[:e.col-1], line[e.col:]...)
		e.col--
		return
	}
	if e.row == 0 {
		return
	}
	prev := e.lines[e.row-1]
	cur := e.lines[e.row]
	e.lines = append(e.lines[:e.row], e.lines[e.row+1:]...)
	e.row--
	e.col = len(prev)
	e.lines[e.row] = append(prev, cur...)
}

func (e *Editor) delete() {
	e.undo.record(e.state(), kindDelete)
	line := e.lines[e.row]
	if e.col < len(line) {
		e.lines[e.row] = append(line[:e.col], line[e.col+1:]...)
		return
	}
	if e.row < len(e.lines)-1 {
		e.lines[e.row] = append(line, e.lines[e.row+1]...)
		e.lines = append(e.lines[:e.row+1], e.lines[e.row+2:]...)
	}
}

func (e *Editor) left(word bool) {
	if word {
		line := e.lines[e.row]
		for e.col > 0 && line[e.col-1] == ' ' {
			e.col--
		}
		for e.col > 0 && line[e.col-1] != ' ' {
			e.col--
		}
		return
	}
	if e.col > 0 {
		e.col--
	} else if e.row > 0 {
		e.row--
		e.col = len(e.lines[e.row])
	}
}

func (e *Editor) right(word bool) {
	line := e.lines[e.row]
	if word {
		for e.col < len(line) && line[e.col] != ' ' {
			e.col++
		}
		for e.col < len(line) && line[e.col] == ' ' {
			e.col++
		}
		return
	}
	if e.col < len(line) {
		e.col++
	} else if e.row < len(e.lines)-1 {
		e.row++
		e.col = 0
	}
}

func (e *Editor) killWord() {
	e.undo.record(e.state(), kindNone)
	line := e.lines[e.row]
	end := e.col
	for end > 0 && line[end-1] == ' ' {
		end--
	}
	for end > 0 && line[end-1] != ' ' {
		end--
	}
	e.kill = string(line[end:e.col])
	e.lines[e.row] = append(line[:end], line[e.col:]...)
	e.col = end
}

func (e *Editor) historyUp() {
	if len(e.history) == 0 {
		return
	}
	if e.histIdx == len(e.history) {
		e.stash = e.snapshot()
	}
	if e.histIdx > 0 {
		e.histIdx--
		e.SetValue(e.history[e.histIdx])
	}
}

func (e *Editor) historyDown() {
	if e.histIdx >= len(e.history) {
		return
	}
	e.histIdx++
	if e.histIdx == len(e.history) && e.stash != nil {
		e.lines = e.stash
		e.row = len(e.lines) - 1
		e.col = len(e.lines[e.row])
		e.stash = nil
		return
	}
	if e.histIdx < len(e.history) {
		e.SetValue(e.history[e.histIdx])
	}
}

func (e *Editor) snapshot() [][]rune {
	out := make([][]rune, len(e.lines))
	for i, l := range e.lines {
		out[i] = append([]rune{}, l...)
	}
	return out
}

// maybeComplete refreshes the completion menu for the word under the cursor.
//
// The trigger is part of the WORD, not the character before it: `/` and `@`
// are typed as the first character of what they complete, so looking behind
// the word meant the menu never opened at all for a bare `/`. The completer
// is handed the whole word, trigger included, and decides for itself.
func (e *Editor) maybeComplete() {
	if e.complete == nil {
		return
	}
	word, start := e.wordBeforeCursor()
	if word == "" {
		e.menu = nil
		return
	}
	items := e.complete(word)
	if len(items) == 0 {
		e.menu = nil
		return
	}
	if e.menu == nil {
		e.menu = &Menu{Theme: e.Theme}
	}
	// Recomputed every keystroke: the word grows as it is typed, and a stale
	// start would replace the wrong span on accept.
	e.menu.WordStart = start
	e.menu.Items = items
	e.menu.Filter = word
	e.menu.Cursor = min(e.menu.Cursor, max(len(items)-1, 0))
}

func (e *Editor) wordBeforeCursor() (string, int) {
	line := e.lines[e.row]
	start := e.col
	for start > 0 && line[start-1] != ' ' && line[start-1] != '\t' {
		start--
	}
	return string(line[start:e.col]), start
}

func (e *Editor) acceptCompletion(it Item) {
	e.undo.record(e.state(), kindNone)
	line := e.lines[e.row]
	start := e.menu.WordStart
	tail := append([]rune{}, line[e.col:]...)
	head := append([]rune{}, line[:start]...)
	head = append(head, []rune(it.Value)...)
	e.lines[e.row] = append(head, tail...)
	e.col = len(head)
}

// View renders the composer: a full-width rule, the draft with one column of
// padding, another rule, then the completion list.
//
// No prompt glyph. loop frames the composer with rules rather than marking it
// with a chevron, and the difference matters more than it sounds: a rule
// spans the width, so a wrapped draft reads as one block instead of a ragged
// column of continuation markers.
//
// It returns the lines plus the cursor cell (row, col) within them.
func (e *Editor) View(width int) (lines []string, curRow, curCol int) {
	t := e.Theme
	if t == nil {
		t = NewTheme(NightPalette)
	}
	rule := t.Fg(SlotBorder, strings.Repeat("─", max(width, 0)))

	// One column of padding each side, and one reserved for the cursor so it
	// has somewhere to sit at the end of a full line.
	const padX = 1
	layoutWidth := max(width-padX*2-1, 1)

	out := []string{rule}
	curRow, curCol = -1, 0

	// The leading command token, tinted as it is typed — `/model`, `!ls`.
	//
	// It is the fastest possible confirmation that the line will be READ as a
	// command rather than sent to the model, which is the one thing a leading
	// slash decides and the one mistake a plain composer lets you make
	// silently.
	tokenLen := commandTokenLen(e.lines)

	for i, line := range e.lines {
		body := string(line)
		folded := wrapLine(body, layoutWidth)
		colAcc := 0
		// Where in the ORIGINAL line each folded segment starts, so a token
		// that wraps is tinted across both halves rather than from the top of
		// each one.
		offset := 0
		for j, fl := range folded {
			runes := []rune(fl)
			if i == e.row && curRow == -1 {
				w := len(runes)
				if e.col <= colAcc+w || j == len(folded)-1 {
					curRow, curCol = len(out), e.col-colAcc+padX
				}
				colAcc += w
			}
			text := fl
			if i == 0 && tokenLen > offset {
				text = tintPrefix(t, runes, tokenLen-offset)
			}
			offset += len(runes)
			out = append(out, strings.Repeat(" ", padX)+text)
		}
	}
	if curRow == -1 {
		curRow, curCol = len(out)-1, padX
	}
	out = append(out, rule)

	if e.menu != nil {
		out = append(out, e.menu.View(width)...)
	}
	return out, curRow, curCol
}

// Menu is the inline completion list under the editor.
type Menu struct {
	Items     []Item
	Filter    string
	WordStart int
	Cursor    int
	Theme     *Theme
}

type MenuAction int

const (
	// MenuNone leaves the key to the editor.
	MenuNone MenuAction = iota
	// MenuClaimed means the menu handled it and the editor must not.
	MenuClaimed
	MenuAccept
	MenuCancel
	// MenuAcceptAndSubmit accepts the item and submits the line in one
	// keystroke — Enter on a slash command.
	MenuAcceptAndSubmit
)

// Handle routes one key. It claims only navigation and accept/cancel; every
// other key is left for the editor so typing keeps filtering the list.
func (m *Menu) Handle(k Key) (MenuAction, Item) {
	n := len(m.Items)
	if n == 0 {
		return MenuCancel, Item{}
	}
	switch k.Kind {
	case KeyUp:
		m.Cursor = (m.Cursor - 1 + n) % n
		return MenuClaimed, Item{}
	case KeyDown:
		m.Cursor = (m.Cursor + 1) % n
		return MenuClaimed, Item{}
	case KeyTab:
		return MenuAccept, m.Items[m.Cursor]
	case KeyEnter:
		// Enter accepts AND runs, for a slash command. Accepting alone left
		// the completed command sitting in the composer waiting for a second
		// Enter, which is not what loop does and not what the keystroke
		// reads as: you picked the command you wanted.
		//
		// A FILE completion is the opposite — `@src/main.go` is part of a
		// sentence still being written, so it accepts and stays.
		if strings.HasPrefix(m.Items[m.Cursor].Value, "/") {
			return MenuAcceptAndSubmit, m.Items[m.Cursor]
		}
		return MenuAccept, m.Items[m.Cursor]
	case KeyEsc:
		return MenuCancel, Item{}
	}
	return MenuNone, Item{}
}

// View renders the completion list under the composer.
//
// Matches loop's select-list: an arrow marks the selection, names sit in an
// aligned column with descriptions beside them, and a counter appears once
// the list is longer than the window. The window follows the selection
// rather than paging, so arrowing down never jumps the list under the eye.
func (m *Menu) View(width int) []string {
	t := m.Theme
	if t == nil {
		t = NewTheme(NightPalette)
	}
	if len(m.Items) == 0 {
		return []string{t.Fg(SlotDim, "  no matches")}
	}

	// The name column is as wide as the widest name plus a gap, clamped so a
	// single long name cannot squeeze every description off the row.
	nameWidth := 0
	for _, it := range m.Items {
		nameWidth = max(nameWidth, visibleWidth(it.Label)+menuColumnGap)
	}
	nameWidth = min(max(nameWidth, menuMinColumn), min(menuMaxColumn, max(width/3, menuMinColumn)))

	start := max(0, min(m.Cursor-menuVisible/2, len(m.Items)-menuVisible))
	end := min(start+menuVisible, len(m.Items))

	var out []string
	for i := start; i < end; i++ {
		it := m.Items[i]
		// One column of indent, matching the composer's padding, so the list
		// hangs under the draft rather than off the left edge.
		prefix := "   "
		if i == m.Cursor {
			prefix = " → "
		}
		name := truncate(it.Label, max(nameWidth-menuColumnGap, 1))
		row := prefix + padRight(name, nameWidth)
		if it.Description != "" && width > 40 {
			room := width - visibleWidth(row) - 2
			if room > menuMinDescription {
				row += truncate(it.Description, room)
			}
		}
		if i == m.Cursor {
			out = append(out, t.Fg(SlotAccent, fitRow(row, width)))
			continue
		}
		// Only the name carries text weight; the description recedes.
		desc := row[len(prefix)+len(padRight(name, nameWidth)):]
		out = append(out, prefix+t.Fg(SlotText, padRight(name, nameWidth))+t.Fg(SlotDim, desc))
	}

	if start > 0 || end < len(m.Items) {
		out = append(out, t.Fg(SlotDim, fmt.Sprintf("   (%d/%d)", m.Cursor+1, len(m.Items))))
	}
	return out
}

// Completion list geometry, matching loop's select-list.
const (
	// menuVisible is how many rows the window shows at once.
	menuVisible = 5
	// menuColumnGap separates the name column from the descriptions.
	menuColumnGap = 2
	menuMinColumn = 12
	menuMaxColumn = 28
	// menuMinDescription is the narrowest description worth showing; below
	// it the row is just a name and a few truncated letters.
	menuMinDescription = 12
)

// Insert types text into the draft at the cursor.
//
// Newlines become real lines rather than being stripped: pasting a multi-line
// snippet should look like what was pasted.
func (e *Editor) Insert(text string) {
	e.undo.record(e.state(), kindNone)
	for _, r := range text {
		switch r {
		case '\r':
		case '\n':
			e.newlineRaw()
		default:
			e.insertRuneRaw(r)
		}
	}
	e.menu = nil
}

// commandTokenLen is the length in runes of a leading command token, or 0.
//
// Only on the FIRST line, and only when the token is the very start of it: a
// slash further in is a path, and `/usr/bin` in the middle of a sentence is
// not a command. A lone "/" is not tinted either — nothing has been typed
// yet, and colouring the bare character would announce a command the user has
// not written.
func commandTokenLen(lines [][]rune) int {
	if len(lines) == 0 {
		return 0
	}
	first := lines[0]
	if len(first) < 2 {
		return 0
	}
	if first[0] != '/' && first[0] != '!' {
		return 0
	}
	// `!` runs the rest of the line as a shell command, so only its marker is
	// the token; `/name` is a word.
	if first[0] == '!' {
		return 1
	}
	n := 1
	for n < len(first) && !unicode.IsSpace(first[n]) {
		n++
	}
	return n
}

// tintPrefix colours the first n runes of a segment.
func tintPrefix(t *Theme, runes []rune, n int) string {
	if n <= 0 {
		return string(runes)
	}
	if n >= len(runes) {
		return t.Fg(SlotAccent, string(runes))
	}
	return t.Fg(SlotAccent, string(runes[:n])) + string(runes[n:])
}
