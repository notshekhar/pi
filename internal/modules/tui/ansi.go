package tui

import (
	"strconv"
	"strings"
)

// ANSI-aware text layout, ported from loop's packages/tui/src/utils.ts.
//
// Every helper here treats escape sequences as zero-width and keeps them
// attached to the text they style, so wrapping a coloured line does not leak
// its colour onto the next one or drop it halfway through.

// extractAnsiCode returns the escape sequence starting at pos, if any.
// It covers CSI styling/cursor codes, OSC hyperlinks and title sets, and the
// two-byte escapes terminals emit around them.
func extractAnsiCode(s string, pos int) (code string, length int) {
	if pos >= len(s) || s[pos] != 0x1b {
		return "", 0
	}
	if pos+1 >= len(s) {
		return "", 0
	}
	switch s[pos+1] {
	case '[': // CSI ... final byte
		j := pos + 2
		for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
			j++
		}
		if j < len(s) {
			return s[pos : j+1], j + 1 - pos
		}
		return "", 0

	case ']': // OSC ... BEL | ESC \
		j := pos + 2
		for j < len(s) {
			if s[j] == 0x07 {
				return s[pos : j+1], j + 1 - pos
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return s[pos : j+2], j + 2 - pos
			}
			j++
		}
		return "", 0

	case '_', 'P', '^': // APC / DCS / PM ... ESC \
		j := pos + 2
		for j < len(s) {
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return s[pos : j+2], j + 2 - pos
			}
			j++
		}
		return "", 0
	}
	return s[pos : pos+2], 2
}

// stripANSI removes every escape sequence; widths are computed on the rest.
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if _, n := extractAnsiCode(s, i); n > 0 {
			i += n
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// ansiTracker follows SGR state across a line so a wrap can re-open the
// styles that were active at the break.
type ansiTracker struct {
	fg, bg                                    string
	bold, dim, italic, underline, strike, rev bool
}

func (t *ansiTracker) feed(s string) {
	for i := 0; i < len(s); {
		code, n := extractAnsiCode(s, i)
		if n == 0 {
			i++
			continue
		}
		i += n
		if !strings.HasPrefix(code, "\x1b[") || !strings.HasSuffix(code, "m") {
			continue
		}
		t.apply(code[2 : len(code)-1])
	}
}

// apply folds one SGR parameter string into the tracked state. Extended
// colours (38/48;5;n and 38/48;2;r;g;b) are consumed whole so their
// arguments are never mistaken for standalone attributes.
func (t *ansiTracker) apply(params string) {
	if params == "" {
		params = "0"
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			continue
		}
		switch {
		case n == 0:
			*t = ansiTracker{}
		case n == 1:
			t.bold = true
		case n == 2:
			t.dim = true
		case n == 3:
			t.italic = true
		case n == 4:
			t.underline = true
		case n == 7:
			t.rev = true
		case n == 9:
			t.strike = true
		case n == 22:
			t.bold, t.dim = false, false
		case n == 23:
			t.italic = false
		case n == 24:
			t.underline = false
		case n == 27:
			t.rev = false
		case n == 29:
			t.strike = false
		case n == 38 || n == 48:
			seq, consumed := extendedColor(parts, i)
			if consumed == 0 {
				continue
			}
			if n == 38 {
				t.fg = seq
			} else {
				t.bg = seq
			}
			i += consumed
		case n == 39:
			t.fg = ""
		case n == 49:
			t.bg = ""
		case n >= 30 && n <= 37, n >= 90 && n <= 97:
			t.fg = "\x1b[" + strconv.Itoa(n) + "m"
		case n >= 40 && n <= 47, n >= 100 && n <= 107:
			t.bg = "\x1b[" + strconv.Itoa(n) + "m"
		}
	}
}

// extendedColor rebuilds a 256-colour or truecolor sequence starting at
// parts[i], returning it and how many extra parts it consumed.
func extendedColor(parts []string, i int) (string, int) {
	if i+1 >= len(parts) {
		return "", 0
	}
	switch parts[i+1] {
	case "5":
		if i+2 >= len(parts) {
			return "", 0
		}
		return "\x1b[" + strings.Join(parts[i:i+3], ";") + "m", 2
	case "2":
		if i+4 >= len(parts) {
			return "", 0
		}
		return "\x1b[" + strings.Join(parts[i:i+5], ";") + "m", 4
	}
	return "", 0
}

// activeCodes re-opens every style the tracker currently holds.
func (t *ansiTracker) activeCodes() string {
	var b strings.Builder
	if t.bold {
		b.WriteString("\x1b[1m")
	}
	if t.dim {
		b.WriteString("\x1b[2m")
	}
	if t.italic {
		b.WriteString("\x1b[3m")
	}
	if t.underline {
		b.WriteString("\x1b[4m")
	}
	if t.rev {
		b.WriteString("\x1b[7m")
	}
	if t.strike {
		b.WriteString("\x1b[9m")
	}
	b.WriteString(t.fg)
	b.WriteString(t.bg)
	return b.String()
}

// lineEndReset closes the decorations that would otherwise bleed into the
// terminal's padding at the end of a wrapped line. Underline and strike do
// bleed; a background is deliberately left open so a washed row stays washed
// to the edge.
func (t *ansiTracker) lineEndReset() string {
	var b strings.Builder
	if t.underline {
		b.WriteString("\x1b[24m")
	}
	if t.strike {
		b.WriteString("\x1b[29m")
	}
	return b.String()
}

// wrapTextWithAnsi word-wraps text to width, carrying ANSI state across both
// literal newlines and wrap points.
func wrapTextWithAnsi(text string, width int) []string {
	if width <= 0 {
		width = 1
	}
	if text == "" {
		return []string{""}
	}
	var out []string
	var tracker ansiTracker
	for _, line := range strings.Split(text, "\n") {
		prefix := ""
		if len(out) > 0 {
			prefix = tracker.activeCodes()
		}
		out = append(out, wrapSingleLine(prefix+line, width)...)
		tracker.feed(line)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func wrapSingleLine(line string, width int) []string {
	if line == "" {
		return []string{""}
	}
	if visibleWidth(line) <= width {
		return []string{line}
	}

	var wrapped []string
	var tracker ansiTracker
	cur := ""
	curWidth := 0

	for _, token := range splitIntoTokensWithAnsi(line) {
		tokenWidth := visibleWidth(token)
		isSpace := strings.TrimSpace(token) == ""

		// A single token wider than the line has to be broken mid-word.
		if tokenWidth > width && !isSpace {
			if cur != "" {
				wrapped = append(wrapped, cur+tracker.lineEndReset())
				cur, curWidth = "", 0
			}
			broken := breakLongWord(token, width, &tracker)
			wrapped = append(wrapped, broken[:len(broken)-1]...)
			cur = broken[len(broken)-1]
			curWidth = visibleWidth(cur)
			continue
		}

		if curWidth+tokenWidth > width && curWidth > 0 {
			wrapped = append(wrapped, strings.TrimRight(cur, " \t")+tracker.lineEndReset())
			if isSpace {
				// Never open a line with the whitespace that caused the wrap.
				cur, curWidth = tracker.activeCodes(), 0
			} else {
				cur, curWidth = tracker.activeCodes()+token, tokenWidth
			}
		} else {
			cur += token
			curWidth += tokenWidth
		}
		tracker.feed(token)
	}
	if cur != "" || len(wrapped) == 0 {
		wrapped = append(wrapped, cur)
	}
	return wrapped
}

// splitIntoTokensWithAnsi cuts a line into word and whitespace runs, keeping
// each escape sequence glued to the token it opens.
func splitIntoTokensWithAnsi(line string) []string {
	var tokens []string
	var cur strings.Builder
	curIsSpace := false
	started := false

	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
		started = false
	}

	for i := 0; i < len(line); {
		if code, n := extractAnsiCode(line, i); n > 0 {
			cur.WriteString(code)
			i += n
			continue
		}
		isSpace := line[i] == ' ' || line[i] == '\t'
		if started && isSpace != curIsSpace {
			flush()
		}
		curIsSpace = isSpace
		started = true
		cur.WriteByte(line[i])
		i++
	}
	flush()
	return tokens
}

// breakLongWord splits a token wider than the line into width-sized pieces.
// The last element is the unfinished remainder, for the caller to continue.
func breakLongWord(token string, width int, tracker *ansiTracker) []string {
	var out []string
	cur := tracker.activeCodes()
	curWidth := 0

	flushPiece := func() {
		out = append(out, cur+tracker.lineEndReset())
		cur = tracker.activeCodes()
		curWidth = 0
	}

	for i := 0; i < len(token); {
		if code, n := extractAnsiCode(token, i); n > 0 {
			tracker.feed(code)
			cur += code
			i += n
			continue
		}
		end := i
		for end < len(token) {
			if _, n := extractAnsiCode(token, end); n > 0 {
				break
			}
			end++
		}
		forEachGrapheme(token[i:end], func(g string) {
			w := graphemeWidth(g)
			if curWidth+w > width && curWidth > 0 {
				flushPiece()
			}
			cur += g
			curWidth += w
		})
		i = end
	}
	return append(out, cur)
}

// truncateToWidth cuts styled text to maxWidth cells, appending ellipsis when
// it had to cut. Escape sequences are carried through so the kept prefix
// keeps its styling.
func truncateToWidth(text string, maxWidth int, ellipsis string) string {
	if maxWidth <= 0 || text == "" {
		return ""
	}
	if visibleWidth(text) <= maxWidth {
		return text
	}
	ellipsisWidth := visibleWidth(ellipsis)
	if ellipsisWidth >= maxWidth {
		ellipsis, ellipsisWidth = "", 0
	}
	target := maxWidth - ellipsisWidth

	var b strings.Builder
	pending := ""
	width := 0
	for i := 0; i < len(text); {
		if code, n := extractAnsiCode(text, i); n > 0 {
			// Hold escapes until a visible cell earns them, so a truncation
			// point does not trail a dangling colour change.
			pending += code
			i += n
			continue
		}
		end := i
		for end < len(text) {
			if _, n := extractAnsiCode(text, end); n > 0 {
				break
			}
			end++
		}
		stop := false
		forEachGrapheme(text[i:end], func(g string) {
			if stop {
				return
			}
			w := graphemeWidth(g)
			if width+w > target {
				stop = true
				return
			}
			b.WriteString(pending)
			pending = ""
			b.WriteString(g)
			width += w
		})
		if stop {
			break
		}
		i = end
	}
	return b.String() + "\x1b[0m" + ellipsis
}

// fitRow keeps a one-line row inside width.
//
// An overflowing line wraps in the terminal and pushes the whole grid down by
// a row, which desynchronises every absolute cursor move that follows — the
// same crash guard loop's noir rows carry.
func fitRow(line string, width int) string {
	if visibleWidth(line) > width {
		return truncateToWidth(line, max(width-1, 0), "") + "…"
	}
	return line
}

// padToWidth appends spaces until the line fills width cells.
func padToWidth(line string, width int) string {
	if n := width - visibleWidth(line); n > 0 {
		return line + strings.Repeat(" ", n)
	}
	return line
}

// applyBackgroundToLine pads a line to the full width and washes it, so the
// canvas colour reaches the right edge rather than stopping at the text.
func applyBackgroundToLine(line string, width int, bg func(string) string) string {
	return bg(padToWidth(line, width))
}

// wrapBlock word-wraps every newline-separated line to width.
func wrapBlock(s string, width int) []string { return wrapTextWithAnsi(s, width) }

// wrapLine word-wraps a single line to width.
func wrapLine(line string, width int) []string { return wrapSingleLine(line, width) }
