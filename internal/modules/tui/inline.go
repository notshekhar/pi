package tui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
)

// The inline renderer: a live region painted into the NORMAL screen buffer,
// which is how loop draws and why its sessions survive in scrollback.
//
// The alternate screen was the wrong model, and the difference is visible in
// the first frame. On the alt screen the grid is always exactly as tall as
// the terminal, so a short transcript has to be pushed somewhere — pi-agent
// pushed it down, which parked the masthead at the BOTTOM of an otherwise
// empty screen with the composer under it. loop starts at the top and grows
// downward, so at startup the masthead sits at the top with the composer
// directly beneath it. Everything else about the two frames matched; the
// anchor did not.
//
// It also cost the session: the alt screen has no scrollback, and on exit
// the terminal restores the primary buffer and the entire conversation is
// gone. Inline, what scrolls off the top is the terminal's own history and
// stays scrollable and selectable with the mouse.
//
// The region is capped at the terminal height, so a full repaint is at most
// one screenful of bytes and the cursor arithmetic below never has to move
// above the top of the screen — which it cannot do, and which is the failure
// that makes hand-rolled inline renderers walk up the page leaving copies of
// themselves behind.
//
// But a full repaint per frame is not what the frame usually needs. Two
// mechanisms keep a frame down to what actually changed, and they are the
// same two loop uses:
//
//  1. DIFFING. The frame on screen is kept in `prev` and compared row by row
//     against the frame being painted; only the span from the first changed
//     row to the last is rewritten. A streaming token changes one row of the
//     transcript, and a spinner changes one row of the chrome — repainting
//     the other forty is bytes down the wire for a screen that already says
//     the right thing. It matters most exactly where it is least visible
//     locally: over ssh, where a screenful per token is what turns a stream
//     into a slideshow.
//
//  2. SYNCHRONIZED OUTPUT (DECSET 2026). The terminal is told to hold the
//     frame back and present it in one go, so a repaint is never caught
//     half-drawn. Without it a frame that rewrites a span while the terminal
//     is mid-refresh tears — the top half of the new frame above the bottom
//     half of the old. Terminals that do not implement the mode ignore the
//     sequence, which is why it is emitted unconditionally.

// inlineWriter owns the live region: how many rows it occupies, where the
// cursor sits inside it, and what is currently printed there.
//
// Every move is RELATIVE to the cursor. That is deliberate: the region has no
// fixed screen address, because writing its last line at the bottom row
// scrolls the whole screen up by one and silently moves it. Relative moves
// travel with the scroll; an absolute anchor would drift by one row every
// time the terminal scrolled and smear the frame down the page.
type inlineWriter struct {
	painted int // rows the region currently occupies
	row     int // cursor's row within the region, 0-based
	// prev is the frame on screen — the diff baseline. It is only ever what
	// this writer actually printed: anything else on those rows (a child
	// program's output, a cleared screen) has to come through Reset, or the
	// diff will call a row unchanged that the writer cannot vouch for.
	prev []string
	// cols is the width prev was painted at. A resize reflows every row, so
	// a width change retires the whole baseline.
	cols int
	// full records that the region occupies the WHOLE screen, which is the
	// only state in which a newline at its last row scrolls the terminal.
	// Set by the caller, because the writer deliberately does not know the
	// screen's height — it knows only its own region.
	full bool
	// out defaults to stdout. Injectable so the escape sequences can be
	// asserted on: the cursor arithmetic here is the part that cannot be
	// checked by looking at a rendered screen, because a frame that walks up
	// the page still LOOKS like a frame.
	out io.Writer
	// buf is reused between frames. At 60fps a fresh screenful of bytes per
	// frame is pure garbage for the collector to walk.
	buf bytes.Buffer
}

const (
	syncBegin = "\x1b[?2026h"
	syncEnd   = "\x1b[?2026l"
)

func (w *inlineWriter) write(s string) {
	if w.out == nil {
		w.out = os.Stdout
	}
	io.WriteString(w.out, s)
}

// Paint updates the region to `lines` and leaves the cursor at (curRow,
// curCol) within it. lines must be no taller than the terminal.
//
// commit is printed ABOVE the region and never touched again — it is how a
// retired block is handed to the terminal. Those rows are written over the
// top of the old region and the region is redrawn beneath them, so the
// screen scrolls by exactly the amount committed and the terminal takes the
// overflow into its scrollback.
func (w *inlineWriter) Paint(commit, lines []string, cols, curRow, curCol int, showCursor bool) {
	if len(lines) == 0 {
		return
	}
	b := &w.buf
	b.Reset()
	// One sequence pair around the whole frame. Hiding the cursor as well:
	// otherwise it is dragged through every row it passes on the way down
	// and the composer's caret smears.
	b.WriteString(syncBegin)
	b.WriteString("\x1b[?25l")

	prev := w.prev
	if cols != w.cols {
		prev = nil
	}

	// Back to the top of the region, which is where both the commit and the
	// row arithmetic below start counting from.
	if w.painted > 0 {
		up(b, w.row)
		b.WriteString("\r")
	}
	w.row = 0

	if len(commit) > 0 {
		for _, line := range commit {
			b.WriteString(truncateToWidth(line, cols, ""))
			b.WriteString("\x1b[K\r\n")
		}
		// The committed rows were written OVER the top of the old region, so
		// what survives of it is the rows below them — and those rows are
		// exactly the baseline for the new frame, because retirement drops
		// the blocks that produced the committed lines and leaves the rest
		// where they were. Keeping the baseline is what lets a frame that
		// retires a block still only repaint what moved.
		if len(commit) >= len(prev) {
			prev = nil
		} else {
			prev = prev[len(commit):]
		}
		w.painted = max(w.painted-len(commit), 0)
	}

	// The frame is the PREVIOUS one scrolled up: the transcript grew past the
	// screen and the window slid. Scroll the terminal instead of rewriting
	// every row.
	//
	// This is the common case during a long answer, and rewriting is both the
	// slowest and the most visible way to handle it: a full-screen repaint per
	// streamed token is a screenful of bytes 60 times a second, and on a
	// terminal that does not honour synchronized output the repaint is watched
	// in progress — rows blanking and refilling, which reads as the transcript
	// flickering or tearing.
	//
	// Scrolling is also the only way those rows reach the terminal's
	// SCROLLBACK. A rewritten row is overwritten in place and gone; a scrolled
	// one goes into history where it can still be read and selected.
	if shift := slideOf(prev, lines, w.full); shift > 0 {
		w.moveTo(b, w.painted-1)
		b.WriteString(strings.Repeat("\r\n", shift))
		// The region did not move relative to the cursor — the SCREEN did —
		// so the row count is unchanged and only the newly exposed rows at
		// the bottom have to be written.
		w.row = w.painted - 1
		for i := len(lines) - shift; i < len(lines); i++ {
			if i > len(lines)-shift {
				b.WriteString("\r\n")
			}
			b.WriteString(truncateToWidth(lines[i], cols, ""))
			b.WriteString("\x1b[K")
		}
		w.prev = append(w.prev[:0], lines...)
		w.cols = cols
		up(b, w.row-curRow)
		down(b, curRow-w.row)
		w.row = curRow
		fmt.Fprintf(b, "\x1b[%dG", curCol+1)
		if showCursor {
			b.WriteString("\x1b[?25h")
		}
		b.WriteString(syncEnd)
		w.write(b.String())
		return
	}

	first, last := changedRows(prev, lines)
	// Rows that changed only because the frame got SHORTER have no content to
	// write — they are handled below, by blanking. Clamping here rather than
	// in the diff keeps changedRows a straight answer about where the two
	// frames disagree.
	if last >= len(lines) {
		last = len(lines) - 1
	}
	if first >= 0 && first <= last {
		w.moveTo(b, first)
		b.WriteString("\r")
		for i := first; i <= last; i++ {
			if i > first {
				b.WriteString("\r\n")
			}
			b.WriteString(truncateToWidth(lines[i], cols, ""))
			// Clear to end of line rather than pre-padding: a short line must
			// erase whatever the previous frame left to its right.
			b.WriteString("\x1b[K")
		}
		w.row = last
	}

	// The region shrank — the rows it no longer occupies still hold the old
	// frame and have to be blanked, or a collapsing menu leaves its tail
	// printed under the composer.
	if extra := w.painted - len(lines); extra > 0 {
		w.moveTo(b, len(lines)-1)
		for i := 0; i < extra; i++ {
			b.WriteString("\r\n\x1b[K")
		}
		w.row = len(lines) - 1 + extra
	}

	w.painted = len(lines)
	// Reusing the slice: `lines` is a fresh view built by the caller each
	// frame, never this buffer handed back.
	w.prev = append(w.prev[:0], lines...)
	w.cols = cols

	// The cursor can now be ABOVE the last row written — a frame whose only
	// change was in the transcript leaves it there while the caret belongs
	// in the composer below — so this moves both ways. Every row it crosses
	// is inside the region and therefore exists.
	up(b, w.row-curRow)
	down(b, curRow-w.row)
	w.row = curRow
	// Absolute column, 1-indexed. Safe where absolute rows are not: the
	// region scrolls vertically, never horizontally.
	fmt.Fprintf(b, "\x1b[%dG", curCol+1)
	if showCursor {
		b.WriteString("\x1b[?25h")
	}
	b.WriteString(syncEnd)
	w.write(b.String())
}

// slideOf reports how far the frame scrolled, or 0 when it did not.
//
// A slide is only a slide when the region FILLS the screen: that is the state
// in which a newline at the bottom row scrolls the terminal, which is what
// makes this correct. Anywhere else the region simply grows downward and the
// rows are still there to be written into.
//
// The frames must also be the same height. A window that slid AND changed
// height is two edits at once, and unpicking them is the diff's job.
func slideOf(prev, lines []string, full bool) int {
	if !full || len(prev) == 0 || len(prev) != len(lines) {
		return 0
	}
	// Bounded: past a handful of rows the scroll stops being cheaper than the
	// rewrite, and a "slide" that large is more likely a coincidence than a
	// window moving.
	limit := min(len(lines)/2, 8)
	for shift := 1; shift <= limit; shift++ {
		match := true
		for i := 0; i+shift < len(prev); i++ {
			if prev[i+shift] != lines[i] {
				match = false
				break
			}
		}
		if match {
			return shift
		}
	}
	return 0
}

// changedRows is the diff: the first and last row where the frame on screen
// and the frame being painted disagree, or (-1, -1) when they are identical.
//
// A span rather than a per-row list because the rows between two changed
// rows are almost always changed too — a streaming block reflows all of
// itself — and rewriting a contiguous run costs one cursor move, while
// skipping the gaps costs one per island and buys back a few dozen bytes.
func changedRows(prev, next []string) (int, int) {
	first, last := -1, -1
	for i := 0; i < max(len(prev), len(next)); i++ {
		var was, now string
		if i < len(prev) {
			was = prev[i]
		}
		if i < len(next) {
			now = next[i]
		}
		if was != now {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	// Rows past the end of the old frame have never been printed at all, so
	// a blank one there is not "unchanged" — there is nothing on screen to
	// be unchanged from, and the region has to physically grow to reach it.
	// Without this a frame that grows by blank lines grows only in the
	// bookkeeping, and every later cursor move is off by that many rows.
	if len(next) > len(prev) {
		if first == -1 {
			first = len(prev)
		} else {
			first = min(first, len(prev))
		}
		last = len(next) - 1
	}
	return first, last
}

// moveTo walks the cursor to a row of the region, extending the region
// downward if the row is past its bottom.
//
// The extension is the part that cannot be done with a cursor move. Rows
// below the region have never been printed, and if the region already ends
// on the last row of the screen they do not exist: CUD stops at the bottom
// margin, so the frame would silently land a row short and every move after
// it would be off by one. A newline scrolls when it has to, which both
// creates the row and takes the region's top into scrollback.
func (w *inlineWriter) moveTo(b *bytes.Buffer, target int) {
	if target <= w.row {
		up(b, w.row-target)
		w.row = target
		return
	}
	bottom := max(w.painted-1, 0)
	if target <= bottom {
		down(b, target-w.row)
		w.row = target
		return
	}
	down(b, bottom-w.row)
	b.WriteString(strings.Repeat("\r\n", target-bottom))
	w.row = target
}

// Finish leaves the terminal clean on exit: the last `chrome` rows — the
// composer, the status line, the working indicator — are erased, so what
// stays behind in scrollback is the conversation and nothing else.
func (w *inlineWriter) Finish(chrome int) {
	if w.painted == 0 {
		return
	}
	var b strings.Builder
	body := max(w.painted-chrome, 0)
	up(&b, w.row-body)
	down(&b, body-w.row)
	// Erase from here to the end of the screen.
	b.WriteString("\r\x1b[J\x1b[?25h")
	w.write(b.String())
	w.painted, w.row = 0, 0
	w.prev = w.prev[:0]
}

// Reset forgets the region. The next Paint writes from the cursor's current
// position without trying to move up to a top that may no longer be there,
// and rewrites every row rather than trusting a baseline that something else
// has since painted over.
func (w *inlineWriter) Reset() {
	w.painted, w.row = 0, 0
	w.prev = w.prev[:0]
}

// Invalidate drops the diff baseline while KEEPING the region's position, so
// the next Paint rewrites every row where the region already is.
//
// The difference from Reset is the whole point. Reset says "the region moved
// out from under us"; Invalidate says "the region is where I left it, but I
// no longer trust what is printed in it" — which is what a render that
// failed halfway through a frame leaves behind. Reset there would write the
// recovery frame BELOW the damaged one instead of over it.
func (w *inlineWriter) Invalidate() { w.prev = w.prev[:0] }

func up(b io.StringWriter, n int) {
	if n > 0 {
		b.WriteString("\x1b[" + itoa(n) + "A")
	}
}

func down(b io.StringWriter, n int) {
	if n > 0 {
		b.WriteString("\x1b[" + itoa(n) + "B")
	}
}
