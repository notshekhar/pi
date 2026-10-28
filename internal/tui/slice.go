package tui

import (
	"regexp"
	"strings"
)

// Column slicing over styled lines, ported from loop's utils.ts
// (sliceWithWidth / extractSegments / getGraphemeCellRange). These are what
// the alt-screen renderer composites with: an overlay painted over the frame,
// a selection highlight, a scrollbar cell — each cuts a styled row at a
// column and has to keep the styles on both sides of the cut intact.

// segmentReset closes SGR state and any open OSC 8 link, so a composited
// segment never inherits a link or a colour from the row beneath it.
const segmentReset = "\x1b[0m\x1b]8;;\x07"

// textRuns calls fn for every grapheme in line with its starting column, and
// ansi for every escape sequence, in order. Returning false from either stops
// the walk.
func walkLine(line string, ansi func(code string) bool, grapheme func(g string, w int) bool) {
	for i := 0; i < len(line); {
		if code, n := extractAnsiCode(line, i); n > 0 {
			if !ansi(code) {
				return
			}
			i += n
			continue
		}
		end := i
		for end < len(line) {
			if _, n := extractAnsiCode(line, end); n > 0 {
				break
			}
			end++
		}
		stop := false
		forEachGrapheme(line[i:end], func(g string) {
			if stop {
				return
			}
			if !grapheme(g, graphemeWidth(g)) {
				stop = true
			}
		})
		if stop {
			return
		}
		i = end
	}
}

// sliceWithWidth returns the part of line covering columns
// [startCol, startCol+length), with the styles that apply to it. strict drops
// a wide grapheme that would straddle the end instead of overrunning it.
func sliceWithWidth(line string, startCol, length int, strict bool) (string, int) {
	if length <= 0 {
		return "", 0
	}
	endCol := startCol + length
	var b strings.Builder
	var pending strings.Builder
	width, col := 0, 0
	walkLine(line,
		func(code string) bool {
			if col >= startCol && col < endCol {
				b.WriteString(code)
			} else if col < startCol {
				pending.WriteString(code)
			}
			return true
		},
		func(g string, w int) bool {
			inRange := col >= startCol && col < endCol
			fits := !strict || col+w <= endCol
			if inRange && fits {
				if pending.Len() > 0 {
					b.WriteString(pending.String())
					pending.Reset()
				}
				b.WriteString(g)
				width += w
			}
			col += w
			return col < endCol
		})
	return b.String(), width
}

// sliceByColumn is sliceWithWidth without the width.
func sliceByColumn(line string, startCol, length int, strict bool) string {
	s, _ := sliceWithWidth(line, startCol, length, strict)
	return s
}

// lineSegments is what extractSegments cuts a row into: the part before an
// overlay and the part after it.
type lineSegments struct {
	before      string
	beforeWidth int
	after       string
	afterWidth  int
}

// extractSegments cuts line into [0, beforeEnd) and [afterStart,
// afterStart+afterLen). The after part re-opens the styles that were active
// where it starts, so text to the right of an overlay keeps its colour.
func extractSegments(line string, beforeEnd, afterStart, afterLen int, strictAfter bool) lineSegments {
	var seg lineSegments
	var before, after, pendingBefore strings.Builder
	var tracker ansiTracker
	afterStarted := false
	afterEnd := afterStart + afterLen
	col := 0
	done := func() bool {
		if afterLen <= 0 {
			return col >= beforeEnd
		}
		return col >= afterEnd
	}
	walkLine(line,
		func(code string) bool {
			tracker.feed(code)
			if col < beforeEnd {
				pendingBefore.WriteString(code)
			} else if col >= afterStart && col < afterEnd && afterStarted {
				after.WriteString(code)
			}
			return true
		},
		func(g string, w int) bool {
			if col < beforeEnd && col+w <= beforeEnd {
				if pendingBefore.Len() > 0 {
					before.WriteString(pendingBefore.String())
					pendingBefore.Reset()
				}
				before.WriteString(g)
				seg.beforeWidth += w
			} else if col >= afterStart && col < afterEnd {
				if !strictAfter || col+w <= afterEnd {
					if !afterStarted {
						after.WriteString(tracker.activeCodes())
						afterStarted = true
					}
					after.WriteString(g)
					seg.afterWidth += w
				}
			}
			col += w
			return !done()
		})
	seg.before = before.String()
	seg.after = after.String()
	return seg
}

// compositeLine paints overlay over base starting at startCol, overlayWidth
// cells wide, in a row totalWidth cells wide. Port of loop's compositeTuiLine.
func compositeLine(base, overlay string, startCol, overlayWidth, totalWidth int) string {
	afterStart := startCol + overlayWidth
	b := extractSegments(base, startCol, afterStart, totalWidth-afterStart, true)
	ovText, ovWidth := sliceWithWidth(overlay, 0, overlayWidth, true)
	beforePad := max(0, startCol-b.beforeWidth)
	overlayPad := max(0, overlayWidth-ovWidth)
	actualBefore := max(startCol, b.beforeWidth)
	actualOverlay := max(overlayWidth, ovWidth)
	afterTarget := max(0, totalWidth-actualBefore-actualOverlay)
	afterPad := max(0, afterTarget-b.afterWidth)
	var sb strings.Builder
	sb.WriteString(b.before)
	sb.WriteString(strings.Repeat(" ", beforePad))
	sb.WriteString(segmentReset)
	sb.WriteString(ovText)
	sb.WriteString(strings.Repeat(" ", overlayPad))
	sb.WriteString(segmentReset)
	sb.WriteString(b.after)
	sb.WriteString(strings.Repeat(" ", afterPad))
	out := sb.String()
	if visibleWidth(out) <= totalWidth {
		return out
	}
	return sliceByColumn(out, 0, totalWidth, true)
}

// graphemeCellRange is the span of cells the grapheme under column covers,
// so a selection that starts inside a wide character takes all of it.
func graphemeCellRange(line string, column int) (start, end int, ok bool) {
	col := 0
	walkLine(line, func(string) bool { return true }, func(g string, w int) bool {
		if w > 0 && column >= col && column < col+w {
			start, end, ok = col, col+w, true
			return false
		}
		col += w
		return true
	})
	return
}

var osc8Open = regexp.MustCompile(`^\x1b\]8;[^;]*;([^\x07\x1b]*)(?:\x07|\x1b\\)$`)

// osc8LinkAt is the hyperlink target under column, if the row has one there.
func osc8LinkAt(line string, column int) string {
	active, found, col := "", "", 0
	walkLine(line,
		func(code string) bool {
			if m := osc8Open.FindStringSubmatch(code); m != nil {
				active = m[1]
			}
			return true
		},
		func(g string, w int) bool {
			if g == "\t" {
				w = 3
			}
			if column >= col && column < col+w {
				found = active
				return false
			}
			col += w
			return true
		})
	return found
}

// normalizeTerminalOutput expands tabs to three cells — the width
// visibleWidth already charges for them — so the terminal's own tab stops
// can never disagree with the layout.
func normalizeTerminalOutput(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if code, n := extractAnsiCode(s, i); n > 0 {
			b.WriteString(code)
			i += n
			continue
		}
		if s[i] == '\t' {
			b.WriteString("   ")
		} else {
			b.WriteByte(s[i])
		}
		i++
	}
	return b.String()
}
