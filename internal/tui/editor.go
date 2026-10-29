package tui

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Editor is the composer: loop's packages/tui/src/components/editor.ts,
// ported key for key. Lines are strings and the cursor column is a byte
// offset into the cursor's line; every movement steps by grapheme, so the
// offset only ever lands on a cluster boundary.
type Editor struct {
	lines      []string
	row, col   int
	history    []string // most recent first
	histIdx    int      // -1 when not browsing
	histDraft  *editState
	killRing   []string
	lastAction string // "kill", "yank", "type-word" or ""
	jump       int    // 0 none, 1 forward, -1 backward

	// The sticky column for vertical moves, and the column the cursor had
	// before it was snapped onto the start of a paste marker.
	preferredCol, snappedFrom int
	hasPreferred, hasSnapped  bool

	undo undoStack

	// Large pastes stand in the draft as "[paste #1 +40 lines]" markers and
	// are expanded on submit.
	pastes       map[int]string
	pasteCounter int

	lastWidth int
	scroll    int

	complete   Completer
	menu       *Menu
	menuPrefix string
	menuForce  bool

	// Theme styles the rules, the command tint and the completion menu. Nil
	// renders with the default palette rather than unstyled.
	Theme *Theme
	// PopupHosted hands the completion list to a host that paints it over
	// the frame (see PopupView); View stops appending it.
	PopupHosted bool
	// TermRows is the terminal height, which caps the draft at 30% of it.
	TermRows int
	// DisableSubmit makes Enter do nothing.
	DisableSubmit bool
}

// Completer answers loop's autocomplete provider: the suggestions for the
// text before the cursor, and the prefix they replace. force is a Tab with no
// list open, which completes a bare path. No items means no list.
type Completer func(textBeforeCursor string, force bool) (items []Item, prefix string)

func NewEditor() *Editor {
	return &Editor{lines: []string{""}, histIdx: -1, pastes: map[int]string{}, lastWidth: 80}
}

// SetCompleter wires the @/slash completion source.
func (e *Editor) SetCompleter(c Completer) {
	e.cancelAutocomplete()
	e.complete = c
}

// Value is the current text, paste markers unexpanded.
func (e *Editor) Value() string { return strings.Join(e.lines, "\n") }

// ExpandedValue is the text with paste markers expanded.
func (e *Editor) ExpandedValue() string { return e.expandPasteMarkers(e.Value()) }

// Cursor is the cursor's line and byte column.
func (e *Editor) Cursor() (line, col int) { return e.row, e.col }

// SetValue replaces the buffer (loop's setText).
func (e *Editor) SetValue(s string) {
	e.cancelAutocomplete()
	e.lastAction = ""
	e.exitHistoryBrowsing()
	normalized := normalizeEditorText(s)
	if e.Value() != normalized {
		e.pushUndo()
	}
	e.pastes = map[int]string{}
	e.pasteCounter = 0
	e.setTextInternal(normalized, false)
}

// PushHistory records a submitted prompt for Up/Down (loop's addToHistory).
func (e *Editor) PushHistory(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if len(e.history) > 0 && e.history[0] == s {
		return
	}
	e.history = append([]string{s}, e.history...)
	if len(e.history) > 100 {
		e.history = e.history[:100]
	}
}

// Insert types text at the cursor as one undoable edit (loop's
// insertTextAtCursor).
func (e *Editor) Insert(text string) {
	if text == "" {
		return
	}
	e.cancelAutocomplete()
	e.pushUndo()
	e.lastAction = ""
	e.exitHistoryBrowsing()
	e.insertTextInternal(text)
}

// Paste is a bracketed paste (loop's handlePaste).
func (e *Editor) Paste(text string) {
	e.cancelAutocomplete()
	e.exitHistoryBrowsing()
	e.lastAction = ""
	e.pushUndo()

	// tmux re-encodes control bytes inside a paste as CSI-u ctrl+letter.
	text = csiCtrlLetter.ReplaceAllStringFunc(text, func(m string) string {
		cp, _ := strconv.Atoi(csiCtrlLetter.FindStringSubmatch(m)[1])
		switch {
		case cp >= 'a' && cp <= 'z':
			return string(rune(cp - 96))
		case cp >= 'A' && cp <= 'Z':
			return string(rune(cp - 64))
		}
		return m
	})
	clean := normalizeEditorText(text)
	var b strings.Builder
	for _, r := range clean {
		if r == '\n' || r >= 32 {
			b.WriteRune(r)
		}
	}
	filtered := b.String()
	// A pasted path right after a word gets a space, for readability.
	if filtered != "" && strings.ContainsRune("/~.", rune(filtered[0])) && e.col > 0 {
		if prev := e.lines[e.row][e.col-1]; isWordByte(prev) {
			filtered = " " + filtered
		}
	}
	pasted := strings.Split(filtered, "\n")
	total := utf16Len(filtered)
	if len(pasted) > 10 || total > 1000 {
		e.pasteCounter++
		id := e.pasteCounter
		e.pastes[id] = filtered
		marker := fmt.Sprintf("[paste #%d %d chars]", id, total)
		if len(pasted) > 10 {
			marker = fmt.Sprintf("[paste #%d +%d lines]", id, len(pasted))
		}
		e.insertTextInternal(marker)
		return
	}
	e.insertTextInternal(filtered)
}

var csiCtrlLetter = regexp.MustCompile(`\x1b\[(\d+);5u`)

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// utf16Len counts as JavaScript's String.length does, which is what loop's
// paste thresholds measure.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func normalizeEditorText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\t", "    ")
}

func (e *Editor) setTextInternal(text string, atStart bool) {
	e.lines = strings.Split(text, "\n")
	if atStart {
		e.row = 0
		e.setCol(0)
	} else {
		e.row = len(e.lines) - 1
		e.setCol(len(e.lines[e.row]))
	}
	e.scroll = 0
}

func (e *Editor) insertTextInternal(text string) {
	if text == "" {
		return
	}
	inserted := strings.Split(normalizeEditorText(text), "\n")
	line := e.lines[e.row]
	before, after := line[:e.col], line[e.col:]
	if len(inserted) == 1 {
		e.lines[e.row] = before + inserted[0] + after
		e.setCol(e.col + len(inserted[0]))
		return
	}
	last := inserted[len(inserted)-1]
	out := append([]string{}, e.lines[:e.row]...)
	out = append(out, before+inserted[0])
	out = append(out, inserted[1:len(inserted)-1]...)
	out = append(out, last+after)
	out = append(out, e.lines[e.row+1:]...)
	e.lines = out
	e.row += len(inserted) - 1
	e.setCol(len(last))
}

func (e *Editor) setCol(col int) {
	e.col = col
	e.hasPreferred = false
	e.hasSnapped = false
}

// clear empties the buffer after a submit.
func (e *Editor) clear() {
	e.cancelAutocomplete()
	e.lines = []string{""}
	e.row, e.col = 0, 0
	e.pastes = map[int]string{}
	e.pasteCounter = 0
	e.exitHistoryBrowsing()
	e.scroll = 0
	e.undo.reset()
	e.lastAction = ""
}

// Handle processes one key (loop's handleInput). It reports whether the key
// submitted, and the submitted text: trimmed, with paste markers expanded,
// and possibly empty.
func (e *Editor) Handle(k Key, pasteText string) (submit string, submitted bool) {
	// Character jump: the next printable key is the target.
	if e.jump != 0 {
		if k.Kind == KeyCtrlBracket {
			e.jump = 0
			return "", false
		}
		if r, ok := printable(k); ok {
			dir := e.jump
			e.jump = 0
			e.jumpToChar(string(r), dir)
			return "", false
		}
		e.jump = 0
	}

	if k.Kind == KeyPaste {
		if pasteText != "" {
			e.Paste(pasteText)
		}
		return "", false
	}

	// ctrl+c belongs to the app.
	if k.Kind == KeyCtrlC {
		return "", false
	}

	if isUndoKey(k) {
		e.undoOnce()
		return "", false
	}

	if e.menu != nil {
		switch {
		case k.Kind == KeyEsc:
			e.cancelAutocomplete()
			return "", false
		case k.Kind == KeyUp && !k.Alt && !k.Ctrl && !k.Super:
			e.menu.move(-1)
			return "", false
		case k.Kind == KeyDown && !k.Alt && !k.Ctrl && !k.Super:
			e.menu.move(1)
			return "", false
		case k.Kind == KeyTab:
			if it, ok := e.menu.selected(); ok {
				e.pushUndo()
				e.lastAction = ""
				e.applyCompletion(it, e.menuPrefix)
				e.cancelAutocomplete()
			}
			return "", false
		case isSubmitKey(k):
			if it, ok := e.menu.selected(); ok {
				e.pushUndo()
				e.lastAction = ""
				prefix := e.menuPrefix
				e.applyCompletion(it, prefix)
				e.cancelAutocomplete()
				if !strings.HasPrefix(prefix, "/") {
					return "", false
				}
				// A slash command falls through to submit.
			}
		}
	}

	if k.Kind == KeyTab && e.menu == nil {
		e.handleTabCompletion()
		return "", false
	}

	switch {
	// Deletion.
	case k.Kind == KeyCtrlK:
		e.deleteToEndOfLine()
		return "", false
	case k.Kind == KeyCtrlU, k.Kind == KeyBackspace && k.Super:
		e.deleteToStartOfLine()
		return "", false
	case k.Kind == KeyCtrlW, k.Kind == KeyBackspace && k.Alt:
		e.deleteWordBackwards()
		return "", false
	case k.Kind == KeyRune && k.Alt && k.Rune == 'd', k.Kind == KeyDelete && k.Alt:
		e.deleteWordForward()
		return "", false
	case k.Kind == KeyBackspace:
		e.backspace()
		return "", false
	case k.Kind == KeyDelete, k.Kind == KeyCtrlD:
		e.forwardDelete()
		return "", false

	// Kill ring.
	case k.Kind == KeyCtrlY:
		e.yank()
		return "", false
	case k.Kind == KeyRune && k.Alt && k.Rune == 'y':
		e.yankPop()
		return "", false

	// Line and word motion.
	case k.Kind == KeyHome, k.Kind == KeyCtrlA, k.Kind == KeyLeft && k.Super:
		e.lastAction = ""
		e.setCol(0)
		return "", false
	case k.Kind == KeyEnd, k.Kind == KeyCtrlE, k.Kind == KeyRight && k.Super:
		e.moveToLineEnd()
		return "", false
	case k.Kind == KeyLeft && (k.Alt || k.Ctrl), k.Kind == KeyRune && k.Alt && k.Rune == 'b':
		e.moveWordBackwards()
		return "", false
	case k.Kind == KeyRight && (k.Alt || k.Ctrl), k.Kind == KeyRune && k.Alt && k.Rune == 'f':
		e.moveWordForwards()
		return "", false
	}

	// New line: shift+enter, alt+enter, ctrl+j (a bare LF).
	if k.Kind == KeyEnter && (k.Shift || k.Alt || k.Ctrl) {
		e.addNewLine()
		return "", false
	}

	if isSubmitKey(k) {
		if e.DisableSubmit {
			return "", false
		}
		// Terminals without shift+enter: a backslash before the cursor turns
		// Enter into a newline.
		if e.col > 0 && e.lines[e.row][e.col-1] == '\\' {
			e.backspace()
			e.addNewLine()
			return "", false
		}
		return e.submitValue(), true
	}

	switch {
	case k.Kind == KeyUp && k.Super:
		e.lastAction = ""
		e.row = 0
		e.setCol(0)
	case k.Kind == KeyDown && k.Super:
		e.lastAction = ""
		e.row = len(e.lines) - 1
		e.setCol(len(e.lines[e.row]))
	case k.Kind == KeyUp && !k.Ctrl && !k.Alt:
		// Readline/zsh: Up on the first visual line steps into history,
		// wherever the column is.
		if e.isOnFirstVisualLine() {
			e.navigateHistory(-1)
		} else {
			e.moveCursor(-1, 0)
		}
	case k.Kind == KeyDown && !k.Ctrl && !k.Alt:
		switch {
		case e.histIdx > -1 && e.isOnLastVisualLine():
			e.navigateHistory(1)
		case e.isOnLastVisualLine():
			e.moveToLineEnd()
		default:
			e.moveCursor(1, 0)
		}
	case k.Kind == KeyRight && !k.Shift, k.Kind == KeyCtrlF:
		e.moveCursor(0, 1)
	case k.Kind == KeyLeft && !k.Shift, k.Kind == KeyCtrlB:
		e.moveCursor(0, -1)
	case k.Kind == KeyPageUp:
		e.pageScroll(-1)
	case k.Kind == KeyPageDown:
		e.pageScroll(1)
	case k.Kind == KeyCtrlBracket:
		if k.Alt {
			e.jump = -1
		} else {
			e.jump = 1
		}
	default:
		if r, ok := printable(k); ok {
			e.insertCharacter(string(r))
		}
	}
	return "", false
}

// printable is the character a key types, if it types one. Alt and Super
// chords type nothing, as in loop, where they never decode as printable.
func printable(k Key) (rune, bool) {
	if k.Kind != KeyRune || k.Alt || k.Super || k.Ctrl || k.Rune < 32 || k.Rune == 127 {
		return 0, false
	}
	return k.Rune, true
}

func isSubmitKey(k Key) bool {
	return k.Kind == KeyEnter && !k.Shift && !k.Alt && !k.Ctrl
}

// isUndoKey: ctrl+- and super+z (cmd+z under the kitty protocol).
func isUndoKey(k Key) bool {
	return (k.Kind == KeyCtrlMinus && !k.Alt) || (k.Kind == KeyRune && k.Super && (k.Rune == 'z' || k.Rune == 'Z') && !k.Shift)
}

func (e *Editor) submitValue() string {
	e.cancelAutocomplete()
	result := strings.TrimSpace(e.expandPasteMarkers(e.Value()))
	e.clear()
	return result
}

func (e *Editor) insertCharacter(ch string) {
	e.exitHistoryBrowsing()
	// Fish-style coalescing: a run of word characters undoes as one; each
	// space opens a new unit that takes the following word with it.
	if isWhitespace(ch) || e.lastAction != "type-word" {
		e.pushUndo()
	}
	e.lastAction = "type-word"

	line := e.lines[e.row]
	e.lines[e.row] = line[:e.col] + ch + line[e.col:]
	e.setCol(e.col + len(ch))

	if e.menu == nil {
		before := e.lines[e.row][:e.col]
		switch {
		case ch == "/" && e.isAtStartOfMessage():
			e.requestAutocomplete(false)
		case ch == "@" || ch == "#":
			if len(before) == 1 || before[len(before)-2] == ' ' || before[len(before)-2] == '\t' {
				e.requestAutocomplete(false)
			}
		case isCompletionWordChar(ch):
			if e.isInSlashCommandContext(before) || triggerPattern.MatchString(before) {
				e.requestAutocomplete(false)
			}
		}
	} else {
		e.updateAutocomplete()
	}
}

var triggerPattern = regexp.MustCompile(`(?:^|\s)[@#][^\s]*$`)

func isCompletionWordChar(ch string) bool {
	if len(ch) != 1 {
		return false
	}
	c := ch[0]
	return isWordByte(c) || c == '.' || c == '-'
}

func isWhitespace(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return s != "" && unicode.IsSpace(r)
}

func (e *Editor) addNewLine() {
	e.cancelAutocomplete()
	e.exitHistoryBrowsing()
	e.lastAction = ""
	e.pushUndo()
	line := e.lines[e.row]
	before, after := line[:e.col], line[e.col:]
	out := append([]string{}, e.lines[:e.row]...)
	out = append(out, before, after)
	out = append(out, e.lines[e.row+1:]...)
	e.lines = out
	e.row++
	e.setCol(0)
}

func (e *Editor) backspace() {
	e.exitHistoryBrowsing()
	e.lastAction = ""
	if e.col > 0 {
		e.pushUndo()
		segs := e.graphemes(e.lines[e.row][:e.col])
		last := segs[len(segs)-1].text
		if m := pasteMarkerSingle.FindStringSubmatch(last); m != nil {
			e.removePaste(m[1])
		}
		line := e.lines[e.row]
		e.lines[e.row] = line[:e.col-len(last)] + line[e.col:]
		e.setCol(e.col - len(last))
	} else if e.row > 0 {
		e.pushUndo()
		prev := e.lines[e.row-1]
		e.lines[e.row-1] = prev + e.lines[e.row]
		e.lines = append(e.lines[:e.row], e.lines[e.row+1:]...)
		e.row--
		e.setCol(len(prev))
	}
	e.refreshAutocompleteAfterDelete()
}

// removePaste drops a deleted marker's paste and renumbers the ones after it,
// so [paste #3] becomes [paste #2] when [paste #1] goes.
func (e *Editor) removePaste(idText string) {
	target, _ := strconv.Atoi(idText)
	delete(e.pastes, target)
	e.pasteCounter--
	var higher []int
	for id := range e.pastes {
		if id > target {
			higher = append(higher, id)
		}
	}
	sort.Ints(higher)
	for _, id := range higher {
		e.pastes[id-1] = e.pastes[id]
		delete(e.pastes, id)
	}
	for i, line := range e.lines {
		e.lines[i] = pasteMarker.ReplaceAllStringFunc(line, func(m string) string {
			sub := pasteMarker.FindStringSubmatch(m)
			x, _ := strconv.Atoi(sub[1])
			if x <= target {
				return m
			}
			return fmt.Sprintf("[paste #%d%s]", x-1, sub[2])
		})
	}
}

func (e *Editor) forwardDelete() {
	e.exitHistoryBrowsing()
	e.lastAction = ""
	line := e.lines[e.row]
	if e.col < len(line) {
		e.pushUndo()
		first := e.graphemes(line[e.col:])[0].text
		e.lines[e.row] = line[:e.col] + line[e.col+len(first):]
	} else if e.row < len(e.lines)-1 {
		e.pushUndo()
		e.lines[e.row] = line + e.lines[e.row+1]
		e.lines = append(e.lines[:e.row+1], e.lines[e.row+2:]...)
	}
	e.refreshAutocompleteAfterDelete()
}

// refreshAutocompleteAfterDelete narrows an open list, or reopens one when
// the deletion lands back inside something completable.
func (e *Editor) refreshAutocompleteAfterDelete() {
	if e.menu != nil {
		e.updateAutocomplete()
		return
	}
	before := e.lines[e.row][:e.col]
	if e.isInSlashCommandContext(before) || triggerPattern.MatchString(before) {
		e.requestAutocomplete(false)
	}
}

func (e *Editor) moveToLineEnd() {
	e.lastAction = ""
	e.setCol(len(e.lines[e.row]))
}

func (e *Editor) killPush(text string, prepend, accumulate bool) {
	if text == "" {
		return
	}
	if accumulate && len(e.killRing) > 0 {
		last := e.killRing[len(e.killRing)-1]
		if prepend {
			text += last
		} else {
			text = last + text
		}
		e.killRing[len(e.killRing)-1] = text
		return
	}
	e.killRing = append(e.killRing, text)
}

func (e *Editor) deleteToStartOfLine() {
	e.exitHistoryBrowsing()
	line := e.lines[e.row]
	if e.col > 0 {
		e.pushUndo()
		e.killPush(line[:e.col], true, e.lastAction == "kill")
		e.lastAction = "kill"
		e.lines[e.row] = line[e.col:]
		e.setCol(0)
	} else if e.row > 0 {
		e.pushUndo()
		e.killPush("\n", true, e.lastAction == "kill")
		e.lastAction = "kill"
		e.joinWithPrevious()
	}
}

func (e *Editor) deleteToEndOfLine() {
	e.exitHistoryBrowsing()
	line := e.lines[e.row]
	if e.col < len(line) {
		e.pushUndo()
		e.killPush(line[e.col:], false, e.lastAction == "kill")
		e.lastAction = "kill"
		e.lines[e.row] = line[:e.col]
	} else if e.row < len(e.lines)-1 {
		e.pushUndo()
		e.killPush("\n", false, e.lastAction == "kill")
		e.lastAction = "kill"
		e.lines[e.row] = line + e.lines[e.row+1]
		e.lines = append(e.lines[:e.row+1], e.lines[e.row+2:]...)
	}
}

func (e *Editor) joinWithPrevious() {
	prev := e.lines[e.row-1]
	e.lines[e.row-1] = prev + e.lines[e.row]
	e.lines = append(e.lines[:e.row], e.lines[e.row+1:]...)
	e.row--
	e.setCol(len(prev))
}

func (e *Editor) deleteWordBackwards() {
	e.exitHistoryBrowsing()
	line := e.lines[e.row]
	if e.col == 0 {
		if e.row > 0 {
			e.pushUndo()
			e.killPush("\n", true, e.lastAction == "kill")
			e.lastAction = "kill"
			e.joinWithPrevious()
		}
		return
	}
	e.pushUndo()
	wasKill := e.lastAction == "kill"
	old := e.col
	e.moveWordBackwards()
	from := e.col
	e.killPush(line[from:old], true, wasKill)
	e.lastAction = "kill"
	e.lines[e.row] = line[:from] + line[old:]
	e.setCol(from)
}

func (e *Editor) deleteWordForward() {
	e.exitHistoryBrowsing()
	line := e.lines[e.row]
	if e.col >= len(line) {
		if e.row < len(e.lines)-1 {
			e.pushUndo()
			e.killPush("\n", false, e.lastAction == "kill")
			e.lastAction = "kill"
			e.lines[e.row] = line + e.lines[e.row+1]
			e.lines = append(e.lines[:e.row+1], e.lines[e.row+2:]...)
		}
		return
	}
	e.pushUndo()
	wasKill := e.lastAction == "kill"
	old := e.col
	e.moveWordForwards()
	to := e.col
	e.setCol(old)
	e.killPush(line[old:to], false, wasKill)
	e.lastAction = "kill"
	e.lines[e.row] = line[:old] + line[to:]
}

func (e *Editor) yank() {
	if len(e.killRing) == 0 {
		return
	}
	e.pushUndo()
	e.insertYanked(e.killRing[len(e.killRing)-1])
	e.lastAction = "yank"
}

func (e *Editor) yankPop() {
	if e.lastAction != "yank" || len(e.killRing) <= 1 {
		return
	}
	e.pushUndo()
	e.deleteYanked()
	last := e.killRing[len(e.killRing)-1]
	e.killRing = append([]string{last}, e.killRing[:len(e.killRing)-1]...)
	e.insertYanked(e.killRing[len(e.killRing)-1])
	e.lastAction = "yank"
}

func (e *Editor) insertYanked(text string) {
	e.exitHistoryBrowsing()
	e.insertTextInternal(text)
}

func (e *Editor) deleteYanked() {
	yanked := e.killRing[len(e.killRing)-1]
	parts := strings.Split(yanked, "\n")
	if len(parts) == 1 {
		line := e.lines[e.row]
		e.lines[e.row] = line[:e.col-len(yanked)] + line[e.col:]
		e.setCol(e.col - len(yanked))
		return
	}
	start := e.row - (len(parts) - 1)
	startCol := len(e.lines[start]) - len(parts[0])
	after := e.lines[e.row][e.col:]
	before := e.lines[start][:startCol]
	out := append([]string{}, e.lines[:start]...)
	out = append(out, before+after)
	out = append(out, e.lines[e.row+1:]...)
	e.lines = out
	e.row = start
	e.setCol(startCol)
}

func (e *Editor) pushUndo() { e.undo.push(e.snapshot()) }

func (e *Editor) undoOnce() {
	e.exitHistoryBrowsing()
	s, ok := e.undo.pop()
	if !ok {
		return
	}
	e.restore(s)
	e.lastAction = ""
	e.hasPreferred = false
}

func (e *Editor) jumpToChar(ch string, dir int) {
	e.lastAction = ""
	for i := e.row; i >= 0 && i < len(e.lines); i += dir {
		line := e.lines[i]
		idx := -1
		if dir > 0 {
			from := 0
			if i == e.row {
				from = e.col + 1
			}
			if from <= len(line) {
				if j := strings.Index(line[from:], ch); j >= 0 {
					idx = from + j
				}
			}
		} else {
			to := len(line)
			if i == e.row {
				// lastIndexOf(ch, col-1): a match may START at col-1.
				to = min(e.col-1+len(ch), len(line))
			}
			if to >= 0 {
				idx = strings.LastIndex(line[:max(to, 0)], ch)
			}
		}
		if idx >= 0 {
			e.row = i
			e.setCol(idx)
			return
		}
	}
}

func (e *Editor) moveWordBackwards() {
	e.lastAction = ""
	if e.col == 0 {
		if e.row > 0 {
			e.row--
			e.setCol(len(e.lines[e.row]))
		}
		return
	}
	e.setCol(findWordBackward(e.wordSegments(e.lines[e.row][:e.col]), e.col))
}

func (e *Editor) moveWordForwards() {
	e.lastAction = ""
	line := e.lines[e.row]
	if e.col >= len(line) {
		if e.row < len(e.lines)-1 {
			e.row++
			e.setCol(0)
		}
		return
	}
	e.setCol(e.col + findWordForward(e.wordSegments(line[e.col:])))
}

// History.

func (e *Editor) navigateHistory(dir int) {
	e.lastAction = ""
	if len(e.history) == 0 {
		return
	}
	idx := e.histIdx - dir
	if idx < -1 || idx >= len(e.history) {
		return
	}
	if e.histIdx == -1 && idx >= 0 {
		e.pushUndo()
		s := e.snapshot()
		e.histDraft = &s
	}
	e.histIdx = idx
	if idx == -1 {
		draft := e.histDraft
		e.histDraft = nil
		if draft != nil {
			e.lines, e.row, e.col = draft.lines, draft.row, draft.col
			e.hasPreferred, e.hasSnapped = false, false
			e.scroll = 0
		} else {
			e.setTextInternal("", false)
		}
		return
	}
	e.setTextInternal(e.history[idx], dir == -1)
}

func (e *Editor) exitHistoryBrowsing() {
	e.histIdx = -1
	e.histDraft = nil
}

// Visual lines.

type visualLine struct{ line, start, length int }

func (e *Editor) buildVisualLineMap(width int) []visualLine {
	var out []visualLine
	for i, line := range e.lines {
		switch {
		case line == "":
			out = append(out, visualLine{i, 0, 0})
		case visibleWidth(line) <= width:
			out = append(out, visualLine{i, 0, len(line)})
		default:
			for _, c := range wordWrapLine(line, width, e.graphemes(line)) {
				out = append(out, visualLine{i, c.start, c.end - c.start})
			}
		}
	}
	return out
}

func findVisualLineAt(vls []visualLine, line, col int) int {
	for i, vl := range vls {
		if vl.line != line {
			continue
		}
		off := col - vl.start
		last := i == len(vls)-1 || vls[i+1].line != vl.line
		if off >= 0 && (off < vl.length || (last && off == vl.length)) {
			return i
		}
	}
	return len(vls) - 1
}

func (e *Editor) currentVisualLine(vls []visualLine) int {
	return findVisualLineAt(vls, e.row, e.col)
}

func (e *Editor) isOnFirstVisualLine() bool {
	return e.currentVisualLine(e.buildVisualLineMap(e.lastWidth)) == 0
}

func (e *Editor) isOnLastVisualLine() bool {
	vls := e.buildVisualLineMap(e.lastWidth)
	return e.currentVisualLine(vls) == len(vls)-1
}

func (e *Editor) moveCursor(dLine, dCol int) {
	e.lastAction = ""
	vls := e.buildVisualLineMap(e.lastWidth)
	cur := e.currentVisualLine(vls)
	if dLine != 0 {
		if t := cur + dLine; t >= 0 && t < len(vls) {
			e.moveToVisualLine(vls, cur, t)
		}
	}
	if dCol != 0 {
		line := e.lines[e.row]
		if dCol > 0 {
			switch {
			case e.col < len(line):
				e.setCol(e.col + len(e.graphemes(line[e.col:])[0].text))
			case e.row < len(e.lines)-1:
				e.row++
				e.setCol(0)
			default:
				if cur >= 0 && cur < len(vls) {
					e.preferredCol, e.hasPreferred = utf8.RuneCountInString(line[vls[cur].start:e.col]), true
				}
			}
		} else {
			switch {
			case e.col > 0:
				segs := e.graphemes(line[:e.col])
				e.setCol(e.col - len(segs[len(segs)-1].text))
			case e.row > 0:
				e.row--
				e.setCol(len(e.lines[e.row]))
			}
		}
	}
	// A list computed for the old cursor position is stale.
	if e.menu != nil {
		e.updateAutocomplete()
	}
}

func (e *Editor) pageScroll(dir int) {
	e.lastAction = ""
	page := e.maxVisibleLines()
	vls := e.buildVisualLineMap(e.lastWidth)
	cur := e.currentVisualLine(vls)
	t := max(0, min(len(vls)-1, cur+dir*page))
	e.moveToVisualLine(vls, cur, t)
}

func (e *Editor) moveToVisualLine(vls []visualLine, cur, target int) {
	if cur < 0 || cur >= len(vls) || target < 0 || target >= len(vls) {
		return
	}
	// Columns here count characters, as loop's count UTF-16 units: a byte
	// column would make the sticky column drift over any non-ASCII text.
	cv, tv := vls[cur], vls[target]
	units := func(vl visualLine) int {
		line := e.lines[vl.line]
		return utf8.RuneCountInString(line[vl.start : vl.start+vl.length])
	}
	var curCol int
	if e.hasSnapped {
		i := findVisualLineAt(vls, cv.line, e.snappedFrom)
		line := e.lines[cv.line]
		curCol = utf8.RuneCountInString(line[vls[i].start:min(e.snappedFrom, len(line))])
	} else {
		curCol = utf8.RuneCountInString(e.lines[cv.line][cv.start:e.col])
	}
	lastSrc := cur == len(vls)-1 || vls[cur+1].line != cv.line
	srcMax := units(cv)
	if !lastSrc {
		srcMax = max(0, srcMax-1)
	}
	lastTgt := target == len(vls)-1 || vls[target+1].line != tv.line
	tgtMax := units(tv)
	if !lastTgt {
		tgtMax = max(0, tgtMax-1)
	}
	moveTo := e.verticalMoveColumn(curCol, srcMax, tgtMax)

	e.row = tv.line
	line := e.lines[tv.line]
	col := tv.start
	for n := 0; n < moveTo && col < len(line); n++ {
		_, size := utf8.DecodeRuneInString(line[col:])
		col += size
	}
	e.col = col

	// Snap onto the start of a multi-character cluster (a paste marker, an
	// emoji sequence) so the cursor never lands inside one.
	for _, seg := range e.graphemes(line) {
		if seg.idx > e.col {
			break
		}
		if utf8.RuneCountInString(seg.text) <= 1 {
			continue
		}
		if e.col < seg.idx+len(seg.text) {
			if seg.idx < tv.start && target > cur {
				end := seg.idx + len(seg.text)
				next := target + 1
				for next < len(vls) && vls[next].line == tv.line && vls[next].start < end {
					next++
				}
				if next < len(vls) {
					e.moveToVisualLine(vls, cur, next)
					return
				}
			}
			e.snappedFrom, e.hasSnapped = e.col, true
			e.col = seg.idx
			return
		}
	}
	e.hasSnapped = false
}

// verticalMoveColumn is loop's sticky-column decision table.
func (e *Editor) verticalMoveColumn(cur, srcMax, tgtMax int) int {
	inMiddle := cur < srcMax
	tooShort := tgtMax < cur
	if !e.hasPreferred || inMiddle {
		if tooShort {
			e.preferredCol, e.hasPreferred = cur, true
			return tgtMax
		}
		e.hasPreferred = false
		return cur
	}
	if tooShort || tgtMax < e.preferredCol {
		return tgtMax
	}
	e.hasPreferred = false
	return e.preferredCol
}

// Autocomplete.

func (e *Editor) isSlashMenuAllowed() bool { return e.row == 0 }

func (e *Editor) isAtStartOfMessage() bool {
	if !e.isSlashMenuAllowed() {
		return false
	}
	before := strings.TrimSpace(e.lines[e.row][:e.col])
	return before == "" || before == "/"
}

func (e *Editor) isInSlashCommandContext(before string) bool {
	return e.isSlashMenuAllowed() && strings.HasPrefix(strings.TrimLeft(before, " \t\n\r\f\v"), "/")
}

func (e *Editor) handleTabCompletion() {
	if e.complete == nil {
		return
	}
	before := e.lines[e.row][:e.col]
	if e.isInSlashCommandContext(before) && !strings.Contains(strings.TrimLeft(before, " \t"), " ") {
		e.requestAutocompleteTab(false)
		return
	}
	// shouldTriggerFileCompletion: not while typing a bare slash command.
	if t := strings.TrimSpace(before); strings.HasPrefix(t, "/") && !strings.Contains(t, " ") {
		return
	}
	e.requestAutocompleteTab(true)
}

func (e *Editor) requestAutocomplete(force bool) { e.runAutocomplete(force, false) }

func (e *Editor) requestAutocompleteTab(force bool) { e.runAutocomplete(force, true) }

func (e *Editor) runAutocomplete(force, explicitTab bool) {
	if e.complete == nil {
		return
	}
	items, prefix := e.complete(e.lines[e.row][:e.col], force)
	if len(items) == 0 {
		e.cancelAutocomplete()
		return
	}
	// A forced Tab with one candidate applies it outright.
	if force && explicitTab && len(items) == 1 {
		e.pushUndo()
		e.lastAction = ""
		e.applyCompletion(items[0], prefix)
		return
	}
	cursor := 0
	if e.menu != nil {
		cursor = e.menu.Cursor
	}
	e.menuPrefix = prefix
	e.menuForce = force
	e.menu = &Menu{Items: items, Theme: e.Theme, Cursor: min(cursor, len(items)-1)}
	if best := bestMatchIndex(items, prefix); best >= 0 {
		e.menu.Cursor = best
	} else {
		e.menu.Cursor = 0
	}
}

// bestMatchIndex: an exact value match, else the first prefix match.
func bestMatchIndex(items []Item, prefix string) int {
	if prefix == "" {
		return -1
	}
	first := -1
	for i, it := range items {
		v := completionValue(it)
		if v == prefix {
			return i
		}
		if first == -1 && strings.HasPrefix(v, prefix) {
			first = i
		}
	}
	return first
}

// completionValue is an item's value as loop's provider holds it: a slash
// command's name without the slash.
func completionValue(it Item) string { return strings.TrimPrefix(it.Value, "/") }

func (e *Editor) updateAutocomplete() {
	if e.menu == nil || e.complete == nil {
		return
	}
	e.runAutocomplete(e.menuForce, false)
}

func (e *Editor) cancelAutocomplete() {
	e.menu = nil
	e.menuPrefix = ""
	e.menuForce = false
}

// applyCompletion is loop's CombinedAutocompleteProvider.applyCompletion.
func (e *Editor) applyCompletion(it Item, prefix string) {
	line := e.lines[e.row]
	cut := max(0, e.col-len(prefix))
	before := line[:cut]
	after := line[e.col:]
	value := it.Value
	quoted := strings.HasPrefix(prefix, `"`) || strings.HasPrefix(prefix, `@"`)
	if quoted && strings.HasSuffix(value, `"`) && strings.HasPrefix(after, `"`) {
		after = after[1:]
	}
	isDir := strings.HasSuffix(it.Label, "/")
	offset := len(value)
	if isDir && strings.HasSuffix(value, `"`) {
		offset--
	}
	switch {
	case strings.HasPrefix(prefix, "/") && strings.TrimSpace(before) == "" && !strings.Contains(prefix[1:], "/"):
		name := completionValue(it)
		e.lines[e.row] = before + "/" + name + " " + after
		e.setCol(len(before) + len(name) + 2)
	case strings.HasPrefix(prefix, "@"):
		suffix := " "
		if isDir {
			suffix = ""
		}
		e.lines[e.row] = before + value + suffix + after
		e.setCol(len(before) + offset + len(suffix))
	default:
		e.lines[e.row] = before + value + after
		e.setCol(len(before) + offset)
	}
}

// ShowingMenu reports whether the completion list is up.
func (e *Editor) ShowingMenu() bool { return e.menu != nil }

// Rendering.

func (e *Editor) maxVisibleLines() int {
	rows := e.TermRows
	if rows <= 0 {
		rows = 24
	}
	return max(5, rows*3/10)
}

type layoutLine struct {
	text      string
	hasCursor bool
	cursorPos int
}

func (e *Editor) layoutText(width int) []layoutLine {
	if len(e.lines) == 1 && e.lines[0] == "" {
		return []layoutLine{{text: "", hasCursor: true}}
	}
	var out []layoutLine
	for i, line := range e.lines {
		current := i == e.row
		if visibleWidth(line) <= width {
			out = append(out, layoutLine{text: line, hasCursor: current, cursorPos: e.col})
			continue
		}
		chunks := wordWrapLine(line, width, e.graphemes(line))
		for j, c := range chunks {
			has, pos := false, 0
			if current {
				if j == len(chunks)-1 {
					has, pos = e.col >= c.start, e.col-c.start
				} else if e.col >= c.start && e.col < c.end {
					has, pos = true, min(e.col-c.start, len(c.text))
				}
			}
			out = append(out, layoutLine{text: c.text, hasCursor: has, cursorPos: pos})
		}
	}
	return out
}

// View renders the composer: a rule, the draft with one column of padding
// each side, a rule, then the completion list. The cursor is drawn as a
// reverse-video cell, as loop draws it; the returned cell is where the
// hardware cursor belongs, for IME placement.
func (e *Editor) View(width int) (lines []string, curRow, curCol int) {
	t := e.Theme
	if t == nil {
		t = NewTheme(NightPalette)
	}
	padX := min(1, max(0, (width-1)/2))
	contentWidth := max(1, width-padX*2)
	layoutWidth := contentWidth
	if padX == 0 {
		layoutWidth = max(1, contentWidth-1)
	}
	e.lastWidth = layoutWidth

	layout := e.layoutText(layoutWidth)
	maxVisible := e.maxVisibleLines()
	cursorIdx := 0
	for i, l := range layout {
		if l.hasCursor {
			cursorIdx = i
			break
		}
	}
	if cursorIdx < e.scroll {
		e.scroll = cursorIdx
	} else if cursorIdx >= e.scroll+maxVisible {
		e.scroll = cursorIdx - maxVisible + 1
	}
	e.scroll = max(0, min(e.scroll, max(0, len(layout)-maxVisible)))
	visible := layout[e.scroll:min(len(layout), e.scroll+maxVisible)]

	border := func(s string) string { return t.Fg(SlotBorder, s) }
	if e.scroll > 0 {
		lines = append(lines, border(scrollBorder("↑", e.scroll, width)))
	} else {
		lines = append(lines, border(strings.Repeat("─", max(width, 0))))
	}

	pad := strings.Repeat(" ", padX)
	slashLen := 0
	if m := slashToken.FindString(e.lines[0]); m != "" {
		slashLen = len(m)
	}
	curRow, curCol = -1, padX
	for i, l := range visible {
		first := e.scroll+i == 0
		tint := func(s string, offset int) string {
			if s == "" || slashLen <= 0 || !first {
				return s
			}
			end := slashLen - offset
			if end <= 0 {
				return s
			}
			if end >= len(s) {
				return t.Fg(SlotInputCommand, s)
			}
			return t.Fg(SlotInputCommand, s[:end]) + s[end:]
		}
		text := l.text
		w := visibleWidth(text)
		cursorInPad := false
		if l.hasCursor {
			before, after := text[:min(l.cursorPos, len(text))], text[min(l.cursorPos, len(text)):]
			curRow, curCol = len(lines), padX+visibleWidth(before)
			if after != "" {
				g := e.graphemes(after)[0].text
				text = tint(before, 0) + "\x1b[7m" + g + "\x1b[0m" + tint(after[len(g):], l.cursorPos+len(g))
			} else {
				text = tint(before, 0) + "\x1b[7m \x1b[0m"
				w++
				if w > contentWidth && padX > 0 {
					cursorInPad = true
				}
			}
		} else {
			text = tint(text, 0)
		}
		right := pad
		if cursorInPad {
			right = right[1:]
		}
		lines = append(lines, pad+text+strings.Repeat(" ", max(0, contentWidth-w))+right)
	}
	if curRow == -1 {
		curRow = 1
	}

	below := len(layout) - (e.scroll + len(visible))
	if below > 0 {
		lines = append(lines, border(scrollBorder("↓", below, width)))
	} else {
		lines = append(lines, border(strings.Repeat("─", max(width, 0))))
	}

	if e.menu != nil && !e.PopupHosted {
		for _, l := range e.menu.View(contentWidth) {
			lines = append(lines, pad+l+strings.Repeat(" ", max(0, contentWidth-visibleWidth(l)))+pad)
		}
	}
	return lines, curRow, curCol
}

var slashToken = regexp.MustCompile(`^/\S+`)

func scrollBorder(dir string, hidden, width int) string {
	width = max(0, width)
	indicator := fmt.Sprintf("─── %s %d more ", dir, hidden)
	if rest := width - visibleWidth(indicator); rest >= 0 {
		return indicator + strings.Repeat("─", rest)
	}
	ellipsis := "..."[:min(3, width)]
	return sliceByColumn(indicator, 0, width-len(ellipsis), true) + ellipsis
}

// PopupView is the completion list for a host that draws it above the
// editor, straight onto the conversation: a rule in the editor's border
// colour on top, since there it has no edge of its own. Empty when the list
// is not up, so a host can ask every frame.
func (e *Editor) PopupView(width int) []string {
	if e.menu == nil {
		return nil
	}
	t := e.Theme
	if t == nil {
		t = NewTheme(NightPalette)
	}
	padX := min(1, max(0, (width-1)/2))
	contentWidth := max(1, width-padX*2)
	pad := strings.Repeat(" ", padX)
	rows := []string{t.Fg(SlotBorder, strings.Repeat("─", max(width, 0)))}
	for _, l := range e.menu.View(contentWidth) {
		rows = append(rows, pad+l+strings.Repeat(" ", max(0, contentWidth-visibleWidth(l)))+pad)
	}
	return rows
}

// HardwareCursor reports whether the terminal's own cursor is shown, which
// loop only does when LOOP_HARDWARE_CURSOR=1; otherwise the editor's drawn
// cell is the cursor.
func HardwareCursor() bool { return os.Getenv("LOOP_HARDWARE_CURSOR") == "1" }

// Menu is the inline completion list under the editor.
type Menu struct {
	Items  []Item
	Cursor int
	Theme  *Theme
}

// move steps the selection, wrapping at either end as loop's select list does.
func (m *Menu) move(d int) {
	if n := len(m.Items); n > 0 {
		m.Cursor = (m.Cursor + d + n) % n
	}
}

func (m *Menu) selected() (Item, bool) {
	if m.Cursor < 0 || m.Cursor >= len(m.Items) {
		return Item{}, false
	}
	return m.Items[m.Cursor], true
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
		prefix := "  "
		if i == m.Cursor {
			prefix = "→ "
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
		out = append(out, t.Fg(SlotDim, fmt.Sprintf("  (%d/%d)", m.Cursor+1, len(m.Items))))
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
	menuMaxColumn = 32
	// menuMinDescription is the narrowest description worth showing; below
	// it the row is just a name and a few truncated letters.
	menuMinDescription = 12
)

// Paste markers.

var (
	pasteMarker       = regexp.MustCompile(`\[paste #(\d+)( (?:\+\d+ lines|\d+ chars))?\]`)
	pasteMarkerSingle = regexp.MustCompile(`^\[paste #(\d+)( (?:\+\d+ lines|\d+ chars))?\]$`)
)

func isPasteMarker(s string) bool { return len(s) >= 10 && pasteMarkerSingle.MatchString(s) }

func (e *Editor) expandPasteMarkers(text string) string {
	for id, content := range e.pastes {
		re := regexp.MustCompile(`\[paste #` + strconv.Itoa(id) + `( (?:\+\d+ lines|\d+ chars))?\]`)
		text = re.ReplaceAllLiteralString(text, content)
	}
	return text
}

// segment is a grapheme (or a whole paste marker) and its byte offset.
type segment struct {
	text string
	idx  int
}

// graphemes splits s into clusters, merging each live paste marker into one
// atomic segment so the cursor steps over it whole.
func (e *Editor) graphemes(s string) []segment {
	var segs []segment
	off := 0
	forEachGrapheme(s, func(g string) {
		segs = append(segs, segment{g, off})
		off += len(g)
	})
	if len(e.pastes) == 0 || !strings.Contains(s, "[paste #") {
		return segs
	}
	var spans [][2]int
	for _, m := range pasteMarker.FindAllStringSubmatchIndex(s, -1) {
		id, _ := strconv.Atoi(s[m[2]:m[3]])
		if _, ok := e.pastes[id]; ok {
			spans = append(spans, [2]int{m[0], m[1]})
		}
	}
	if len(spans) == 0 {
		return segs
	}
	var out []segment
	si := 0
	for _, seg := range segs {
		for si < len(spans) && spans[si][1] <= seg.idx {
			si++
		}
		if si < len(spans) && seg.idx >= spans[si][0] && seg.idx < spans[si][1] {
			if seg.idx == spans[si][0] {
				out = append(out, segment{s[spans[si][0]:spans[si][1]], spans[si][0]})
			}
			continue
		}
		out = append(out, seg)
	}
	return out
}

// Word segmentation, approximating Intl.Segmenter's word mode: runs of
// letters and digits (joined across one inner apostrophe or dot, as
// "don't" and "v1.2" are one word), whitespace runs, and single
// punctuation marks.

type wordSeg struct {
	text     string
	wordLike bool
}

type graphemeClass int

const (
	gcSpace graphemeClass = iota
	gcWord
	gcPunct
)

func classify(g string) graphemeClass {
	r, _ := utf8.DecodeRuneInString(g)
	switch {
	case unicode.IsSpace(r):
		return gcSpace
	case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_':
		return gcWord
	}
	return gcPunct
}

// midWord are the characters Intl keeps inside a word when letters or digits
// sit on both sides (UAX #29 MidLetter, MidNum, MidNumLet).
func midWord(g string) bool {
	switch g {
	case "'", ".", ":", "’", ",", ";":
		return true
	}
	return false
}

func (e *Editor) wordSegments(s string) []wordSeg {
	gs := e.graphemes(s)
	var out []wordSeg
	for i := 0; i < len(gs); {
		g := gs[i].text
		if isPasteMarker(g) && len(g) > 1 {
			out = append(out, wordSeg{g, false})
			i++
			continue
		}
		switch classify(g) {
		case gcWord:
			j := i + 1
			for j < len(gs) {
				if isPasteMarker(gs[j].text) && len(gs[j].text) > 1 {
					break
				}
				c := classify(gs[j].text)
				if c == gcWord && !isCJK(gs[j].text) && !isCJK(gs[j-1].text) {
					j++
					continue
				}
				if midWord(gs[j].text) && j+1 < len(gs) && classify(gs[j+1].text) == gcWord && !isCJK(gs[j+1].text) {
					j += 2
					continue
				}
				break
			}
			var b strings.Builder
			for _, x := range gs[i:j] {
				b.WriteString(x.text)
			}
			out = append(out, wordSeg{b.String(), true})
			i = j
		case gcSpace:
			j := i + 1
			for j < len(gs) && classify(gs[j].text) == gcSpace && !isPasteMarker(gs[j].text) {
				j++
			}
			var b strings.Builder
			for _, x := range gs[i:j] {
				b.WriteString(x.text)
			}
			out = append(out, wordSeg{b.String(), false})
			i = j
		default:
			out = append(out, wordSeg{g, false})
			i++
		}
	}
	return out
}

var punctuation = regexp.MustCompile(`[(){}\[\]<>.,;:'"!?+\-=*/\\|&%^$#@~` + "`" + `]`)

// findWordBackward is loop's word-navigation.ts, over segments of the text
// before the cursor; it returns the new cursor.
func findWordBackward(segs []wordSeg, cursor int) int {
	n := cursor
	for len(segs) > 0 && !isPasteMarker(segs[len(segs)-1].text) && isWhitespace(segs[len(segs)-1].text) {
		n -= len(segs[len(segs)-1].text)
		segs = segs[:len(segs)-1]
	}
	if len(segs) == 0 {
		return n
	}
	last := segs[len(segs)-1]
	switch {
	case isPasteMarker(last.text):
		n -= len(last.text)
	case last.wordLike:
		ms := punctuation.FindAllStringIndex(last.text, -1)
		if len(ms) == 0 {
			n -= len(last.text)
		} else {
			n -= len(last.text) - ms[len(ms)-1][1]
		}
	default:
		for len(segs) > 0 {
			s := segs[len(segs)-1]
			if isPasteMarker(s.text) || s.wordLike || isWhitespace(s.text) {
				break
			}
			n -= len(s.text)
			segs = segs[:len(segs)-1]
		}
	}
	return n
}

// findWordForward returns how far the cursor moves over the text after it.
func findWordForward(segs []wordSeg) int {
	n, i := 0, 0
	for i < len(segs) && !isPasteMarker(segs[i].text) && isWhitespace(segs[i].text) {
		n += len(segs[i].text)
		i++
	}
	if i >= len(segs) {
		return n
	}
	s := segs[i]
	switch {
	case isPasteMarker(s.text):
		n += len(s.text)
	case s.wordLike:
		if loc := punctuation.FindStringIndex(s.text); loc != nil {
			n += loc[0]
		} else {
			n += len(s.text)
		}
	default:
		for i < len(segs) && !isPasteMarker(segs[i].text) && !segs[i].wordLike && !isWhitespace(segs[i].text) {
			n += len(segs[i].text)
			i++
		}
	}
	return n
}

func isCJK(g string) bool {
	r, _ := utf8.DecodeRuneInString(g)
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Bopomofo)
}

// textChunk is one visual row of a wrapped line, by byte offsets.
type textChunk struct {
	text       string
	start, end int
}

// wordWrapLine is loop's: break after whitespace or beside CJK, falling back
// to a hard break inside a word longer than the row.
func wordWrapLine(line string, maxWidth int, segs []segment) []textChunk {
	if line == "" || maxWidth <= 0 {
		return []textChunk{{"", 0, 0}}
	}
	if visibleWidth(line) <= maxWidth {
		return []textChunk{{line, 0, len(line)}}
	}
	var chunks []textChunk
	curW, chunkStart := 0, 0
	oppIdx, oppW := -1, 0
	for i, seg := range segs {
		g := seg.text
		gw := visibleWidth(g)
		marker := isPasteMarker(g)
		ws := !marker && isWhitespace(g)

		if curW+gw > maxWidth {
			if oppIdx >= 0 && curW-oppW+gw <= maxWidth {
				chunks = append(chunks, textChunk{line[chunkStart:oppIdx], chunkStart, oppIdx})
				chunkStart = oppIdx
				curW -= oppW
			} else if chunkStart < seg.idx {
				chunks = append(chunks, textChunk{line[chunkStart:seg.idx], chunkStart, seg.idx})
				chunkStart = seg.idx
				curW = 0
			}
			oppIdx = -1
		}

		if gw > maxWidth {
			// An atomic segment wider than the row is split visually only.
			sub := wordWrapLine(g, maxWidth, plainGraphemes(g))
			for _, sc := range sub[:len(sub)-1] {
				chunks = append(chunks, textChunk{sc.text, seg.idx + sc.start, seg.idx + sc.end})
			}
			last := sub[len(sub)-1]
			chunkStart = seg.idx + last.start
			curW = visibleWidth(last.text)
			oppIdx = -1
			continue
		}

		curW += gw
		if i+1 < len(segs) {
			next := segs[i+1]
			nextMarker := isPasteMarker(next.text)
			if ws && (nextMarker || !isWhitespace(next.text)) {
				oppIdx, oppW = next.idx, curW
			} else if !ws && !isWhitespace(next.text) {
				if (!marker && isCJK(g)) || (!nextMarker && isCJK(next.text)) {
					oppIdx, oppW = next.idx, curW
				}
			}
		}
	}
	chunks = append(chunks, textChunk{line[chunkStart:], chunkStart, len(line)})
	return chunks
}

func plainGraphemes(s string) []segment {
	var segs []segment
	off := 0
	forEachGrapheme(s, func(g string) {
		segs = append(segs, segment{g, off})
		off += len(g)
	})
	return segs
}
