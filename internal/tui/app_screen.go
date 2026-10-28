package tui

import (
	"fmt"
	"os"
	"strings"
)

// The frame, assembled the way loop's interactive app assembles it:
//
//	document = history, then chrome
//	chrome   = working-indicator slot, todo panel, editor, status line, spacer
//
// The completion list and every picker are OVERLAYS anchored to the editor,
// not rows appended to the frame — appended, a menu made the frame taller for
// as long as it was open and cost the rows it pushed off the top.

func (a *App) initScreen() {
	a.screen = NewScreen(func(s string) { os.Stdout.WriteString(s) }, func() (int, int) {
		return a.size.Cols, a.size.Rows
	})
	a.screen.RequestRender = func() {
		select {
		case a.wake <- struct{}{}:
		default:
		}
	}
	// Scrolled up while a turn streams, this is the one-click way back —
	// loop's label, centred on the last row of the transcript.
	a.screen.ScrollToEndIndicator = func() string {
		return a.theme.Bg(SlotSelected, a.theme.Fg(SlotText, " ↓ Jump to latest message · end "))
	}
	// Three lines a notch, loop's setting: one reads as a stuck wheel.
	a.screen.WheelLines = 3
	a.screen.OnClick = a.clickAt

	a.history = NewFunc(a.transcriptAt)
	a.chrome = NewFunc(a.renderChrome)
	a.screen.Children.Add(a.history, a.chrome)
	a.transcriptView = NewScrollView(a.history, true, true)
	a.pinnedFrame = VStack(
		&StackEntry{Component: a.transcriptView, Basis: -1, Grow: 1},
		&StackEntry{Component: a.chrome, Basis: -1, Shrink: NoShrink},
	)

	a.editor.PopupHosted = true
	a.menuOverlay = a.screen.ShowOverlay(NewFunc(a.editor.PopupView), OverlayOptions{
		Width: Percent(100), MaxHeight: Percent(100), Anchor: "bottom-left",
		Margin: func() Margin {
			return Margin{Bottom: a.rowsBelowFrame() + a.rowsBelowEditor() + len(a.editorRows(a.size.Cols))}
		},
		Visible: func(int, int) bool {
			return a.editor.ShowingMenu() && !a.overlayOpen() && a.promptOnScreen()
		},
	})
	a.selectorOverlay = a.screen.ShowOverlay(NewFunc(a.renderSelector), OverlayOptions{
		Width: Percent(100), MaxHeight: Percent(100), Anchor: "bottom-left",
		Margin: func() Margin {
			return Margin{Bottom: a.rowsBelowFrame() + a.rowsBelowEditor()}
		},
		Visible: func(int, int) bool {
			return a.overlayOpen() && a.promptOnScreen()
		},
	})
}

// applyPinnedInput swaps the layout: pinned holds the chrome on the last rows
// with the transcript in its own scroll view; flowing scrolls the whole
// document, prompt included.
func (a *App) applyPinnedInput() {
	if a.pinnedInput {
		a.screen.SetLayoutRoot(a.pinnedFrame)
	} else {
		a.screen.SetLayoutRoot(nil)
	}
	a.screen.Invalidate()
}

// rowsBelowFrame is the rows between the end of the document and the bottom
// of the screen: an overlay aimed at the screen's last row would float under
// a short transcript's prompt instead of sitting on it.
func (a *App) rowsBelowFrame() int { return max(0, a.screen.RowsBelowContent()) }

// promptOnScreen is false while the reader has scrolled the prompt off the
// bottom; the menus that sit on it hide with it.
func (a *App) promptOnScreen() bool { return a.screen.RowsBelowContent() >= 0 }

// rowsBelowEditor is the status line plus the trailing spacer.
func (a *App) rowsBelowEditor() int { return len(a.statusRows(a.size.Cols)) + 1 }

// revealPrompt brings the prompt back into view on a keystroke — a shell's
// "scroll on keystroke". Pinned, the prompt never leaves.
func (a *App) revealPrompt() {
	if !a.pinnedInput && !a.screen.IsFollowingOutput() {
		a.screen.ScrollToBottom()
	}
}

// scrollTranscriptToEnd is what sending a message does: an explicit "show me
// what happens next", even for a pinned reader scrolled back.
func (a *App) scrollTranscriptToEnd() {
	if a.pinnedInput {
		a.transcriptView.ScrollToEnd()
	} else if !a.screen.IsFollowingOutput() {
		a.screen.ScrollToBottom()
	}
}

func (a *App) statusRows(width int) []string {
	return a.statusLine(width)
}

// editorRows is the composer with the cursor marker placed in it.
func (a *App) editorRows(width int) []string {
	lines, row, col := a.editor.View(width)
	if !a.nav && !a.overlayOpen() && row >= 0 && row < len(lines) {
		lines = append([]string{}, lines...)
		lines[row] = insertAtColumn(lines[row], col, CursorMarker)
	}
	return lines
}

// insertAtColumn puts s at a visible column of a styled line.
func insertAtColumn(line string, col int, s string) string {
	w := visibleWidth(line)
	if col >= w {
		return line + strings.Repeat(" ", col-w) + s
	}
	return sliceByColumn(line, 0, col, true) + s + sliceByColumn(line, col, w-col, false)
}

// renderChrome is everything under the transcript.
func (a *App) renderChrome(width int) []string {
	// A fixed two-line gap between the transcript and the composer, which the
	// working indicator OCCUPIES rather than adds to — so the composer does
	// not jump down a line the moment a turn starts.
	out := []string{"", ""}
	if a.loader != nil {
		out[1] = pad(ContentIndent) + a.loader.Tick(a.theme)
	}
	if a.confirm == nil && a.prompt == nil && len(a.todos) > 0 {
		out = append(out, renderTodos(a.theme, a.todos, width)...)
	}
	// Queued input, between the panels and the editor: loop's
	// PendingMessageLine — " queued 1/2: " dim, the message muted on one line.
	for i, msg := range a.queued {
		prefix := fmt.Sprintf(" queued %d/%d: ", i+1, len(a.queued))
		line := strings.Join(strings.Fields(msg), " ")
		out = append(out, a.theme.Fg(SlotDim, prefix)+
			a.theme.Fg(SlotMuted, truncateToWidth(line, max(1, width-len(prefix)), "...")))
	}
	out = append(out, a.editorRows(width)...)
	out = append(out, a.statusRows(width)...)
	return append(out, "")
}

// renderSelector is the open picker, prompt or approval, padded to at least
// the editor's height so it always covers the composer it replaces.
func (a *App) renderSelector(width int) []string {
	var lines []string
	switch {
	case a.confirm != nil:
		lines = a.confirm.View(width)
	case a.tree != nil:
		lines = a.tree.View(width)
	case a.modal != nil:
		lines = a.modal.View(width)
	case a.prompt != nil:
		var row, col int
		lines, row, col = a.prompt.View(width)
		if row >= 0 && row < len(lines) {
			lines = append([]string{}, lines...)
			lines[row] = insertAtColumn(lines[row], col, CursorMarker)
		}
	}
	if minRows := len(a.editorRows(width)); len(lines) < minRows {
		lines = append(make([]string, minRows-len(lines)), lines...)
	}
	return lines
}

// viewportKey gives the screen first refusal on input, the way loop's
// viewport input listener runs before the focused component: the mouse,
// focus reports, and the scrolling keys.
func (a *App) viewportKey(k Key) bool {
	switch k.Kind {
	case KeyMouse:
		a.screen.HandleMouse(k.Mouse)
		return true
	case KeyFocusIn, KeyFocusOut:
		if k.Kind == KeyFocusOut {
			a.screen.ClearSelection()
		}
		return true
	}
	a.screen.NoteKey()
	// A picker that owns the keyboard owns its page keys too.
	if a.overlayOpen() {
		return false
	}
	if k.Alt || k.Shift {
		return false
	}
	switch k.Kind {
	case KeyPageUp:
		a.screen.PageUp()
		return true
	case KeyPageDown:
		a.screen.PageDown()
		return true
	case KeyUp, KeyDown:
		if !k.Ctrl {
			return false
		}
		a.scrollToPrompt(map[KeyKind]int{KeyUp: -1, KeyDown: 1}[k.Kind])
		return true
	case KeyHome:
		if k.Ctrl {
			return false
		}
		a.screen.ScrollToTop()
		return true
	case KeyEnd:
		if k.Ctrl {
			return false
		}
		a.screen.ScrollToBottom()
		return true
	}
	return false
}

// entryRanges is each entry's [start, end) row in the transcript, for the
// rows on screen at the current width.
func (a *App) entryRanges(width int) map[int][2]int {
	tick := AnimTick()
	groups := a.groups()
	out := map[int][2]int{}
	row := 0
	for i := 0; i < len(a.entries); i++ {
		var n int
		if g, ok := groups[i]; ok {
			n = len(renderGroupHeader(a.theme, g, a.nav && a.sel == i, a.nav, width))
			out[i] = [2]int{row, row + n}
			row += n
			i += g.count - 1
			continue
		}
		n = len(a.entries[i].render(a.theme, width, tick, a.expandAll, a.nav))
		out[i] = [2]int{row, row + n}
		row += n
	}
	return out
}

// viewport is the transcript rows the reader can see: its own pane when
// pinned, otherwise the screen less the chrome under it.
func (a *App) viewport() (top, height int) {
	if a.pinnedInput {
		return a.transcriptView.ScrollTop(), a.transcriptView.ViewportHeight()
	}
	return a.screen.ViewportTop(), max(1, a.size.Rows-len(a.renderChrome(a.size.Cols)))
}

func (a *App) scrollTranscriptTo(next int) {
	top, _ := a.viewport()
	if next == top {
		return
	}
	if a.pinnedInput {
		a.transcriptView.ScrollTo(next, false)
	} else {
		a.screen.ScrollBy(next - top)
	}
}

// revealSelection scrolls the selected entry into view. An entry taller than
// the window pins to its TOP: fitting both ends ping-pongs the page on every
// keystroke.
func (a *App) revealSelection() {
	r, ok := a.entryRanges(a.size.Cols)[a.sel]
	if !ok {
		return
	}
	top, h := a.viewport()
	next := top
	switch {
	case r[1]-r[0] >= h || r[0] < top:
		next = r[0]
	case r[1] > top+h:
		next = r[1] - h
	}
	a.scrollTranscriptTo(next)
}

// scrollToPrompt jumps to the previous or next user prompt.
func (a *App) scrollToPrompt(dir int) {
	ranges := a.entryRanges(a.size.Cols)
	top, _ := a.viewport()
	best := -1
	for i, e := range a.entries {
		if e.kind != entUser {
			continue
		}
		r, ok := ranges[i]
		if !ok {
			continue
		}
		if dir < 0 && r[0] < top && (best < 0 || r[0] > best) {
			best = r[0]
		}
		if dir > 0 && r[0] > top && (best < 0 || r[0] < best) {
			best = r[0]
		}
	}
	if best >= 0 {
		a.scrollTranscriptTo(best)
	}
}

// clickAt is loop's clickAtScreenRow. There is ONE door into navigation,
// ctrl+e; the pointer does not open it. While the prompt has the keyboard a
// click is the terminal's business (a selection), and must not move the
// selection or the focus. Only once navigating does a click select the entry
// under it.
func (a *App) clickAt(_, y int) {
	if !a.nav {
		return
	}
	top, h := a.viewport()
	if y < 0 || y >= h {
		return
	}
	row := top + y
	for i, r := range a.entryRanges(a.size.Cols) {
		if row < r[0] || row >= r[1] {
			continue
		}
		for _, u := range a.navigable() {
			if u == i {
				a.setSelection(i)
				return
			}
		}
	}
}

// SetClipboard routes a mouse selection's copy through the platform
// clipboard. Without it the copy is an OSC 52 write, which some terminals
// accept silently and drop.
func (a *App) SetClipboard(copy func(text string) error) {
	a.screen.CopySelection = func(text string) bool { return copy(text) == nil }
}

// SetURLOpener makes a clicked OSC 8 link open in the browser.
func (a *App) SetURLOpener(open func(url string)) { a.screen.OpenURL = open }
