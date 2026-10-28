package ui

// Port of loop's packages/tui/src/utils.ts.

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ---- width ----

// couldBeEmoji is a fast pre-filter before the emoji check proper.
func couldBeEmoji(segment string) bool {
	cp, _ := utf8.DecodeRuneInString(segment)
	return (cp >= 0x1f000 && cp <= 0x1fbff) ||
		(cp >= 0x2300 && cp <= 0x23ff) ||
		(cp >= 0x2600 && cp <= 0x27bf) ||
		(cp >= 0x2b50 && cp <= 0x2b55) ||
		strings.ContainsRune(segment, 0xfe0f) ||
		utf8.RuneCountInString(segment) > 2
}

// isRGIEmoji approximates \p{RGI_Emoji} for a cluster that passed
// couldBeEmoji: an emoji-presentation base, or a sequence built on one
// (VS16, ZWJ, skin tone, keycap, tag sequence).
func isRGIEmoji(segment string) bool {
	first, _ := utf8.DecodeRuneInString(segment)
	if strings.ContainsRune(segment, 0xfe0f) || strings.ContainsRune(segment, 0x200d) {
		return true
	}
	for _, r := range segment {
		if r >= 0x1f3fb && r <= 0x1f3ff { // skin tone
			return true
		}
	}
	return eastAsianWidth(first) == 2 && first >= 0x1f000
}

func isDefaultIgnorable(r rune) bool {
	return unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) ||
		unicode.Is(unicode.Variation_Selector, r) ||
		r == 0x00ad || r == 0x034f || r == 0x061c || (r >= 0x115f && r <= 0x1160) ||
		(r >= 0x17b4 && r <= 0x17b5) || (r >= 0x180b && r <= 0x180f) ||
		(r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) ||
		(r >= 0x2060 && r <= 0x206f) || r == 0x3164 || r == 0xfeff || r == 0xffa0 ||
		(r >= 0x1bca0 && r <= 0x1bca3) || (r >= 0x1d173 && r <= 0x1d17a) ||
		(r >= 0xe0000 && r <= 0xe0fff)
}

// zeroWidthRune is a member of loop's zeroWidthRegex class (which leaves out
// spacing marks: a lone matra occupies a cell).
func zeroWidthRune(r rune) bool {
	return isDefaultIgnorable(r) || unicode.IsControl(r) || unicode.Is(unicode.Mn, r) ||
		unicode.Is(unicode.Me, r) || (r >= 0xd800 && r <= 0xdfff)
}

// zeroCellRune is loop's zeroCellRegex: the per-codepoint sum skips these.
func zeroCellRune(r rune) bool {
	return zeroWidthRune(r) || unicode.Is(unicode.Cf, r)
}

// terminalSpacingMark is loop's terminalSpacingMarkRegex: marks terminals give
// a cell even without a base.
func terminalSpacingMark(r rune) bool {
	if r == 0x1734 || r == 0x302e || r == 0x302f {
		return false
	}
	if unicode.Is(unicode.Mc, r) {
		return true
	}
	switch {
	case r == 0x065f, r == 0x0f7f, r == 0x102b, r == 0x102c, r == 0x1031,
		r >= 0x1033 && r <= 0x1035, r == 0x1038, r >= 0x103a && r <= 0x103e:
		return true
	}
	return false
}

func allRunes(s string, f func(rune) bool) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !f(r) {
			return false
		}
	}
	return true
}

// Width calibration. The wcwidth-style defaults match most terminals, but
// Devanagari-style cluster layout is unstandardized; the TUI probes the real
// terminal at startup and overrides these when it disagrees.
var (
	spacingMarkCellWidth  = 1
	shapedClusterTerminal = false
	clampClusterCells     = false
)

// WidthCalibration is what the startup probe measured.
type WidthCalibration struct {
	// SpacingMarkWidth is the cells a spacing combining mark occupies (0 or 1).
	SpacingMarkWidth int
	// ShapedClusters: the terminal shapes whole clusters into one cell.
	ShapedClusters bool
	// ClampClusters: the terminal caps a cluster at one cell pair.
	ClampClusters bool
}

// SetWidthCalibration installs a probe result; it reports whether anything
// changed (callers must then discard anything laid out with old widths).
func SetWidthCalibration(c WidthCalibration) bool {
	if c.SpacingMarkWidth == spacingMarkCellWidth && c.ShapedClusters == shapedClusterTerminal &&
		c.ClampClusters == clampClusterCells {
		return false
	}
	spacingMarkCellWidth = c.SpacingMarkWidth
	shapedClusterTerminal = c.ShapedClusters
	clampClusterCells = c.ClampClusters
	widthCache = map[string]int{}
	widthCacheOrder = nil
	return true
}

var cjkBreak = regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}\p{Bopomofo}]`)

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// graphemeWidth is the terminal width of one grapheme cluster.
func graphemeWidth(segment string) int {
	if segment == "\t" {
		return 3
	}
	if segment == "" {
		return 0
	}
	if allRunes(segment, terminalSpacingMark) {
		return utf8.RuneCountInString(segment) * spacingMarkCellWidth
	}
	if allRunes(segment, zeroWidthRune) {
		return 0
	}
	if couldBeEmoji(segment) && isRGIEmoji(segment) {
		return 2
	}
	first, _ := utf8.DecodeRuneInString(segment)
	if first >= 0x1f1e6 && first <= 0x1f1ff {
		return 2
	}
	if shapedClusterTerminal {
		return eastAsianWidth(first)
	}
	width := 0
	for _, r := range segment {
		if zeroCellRune(r) {
			continue
		}
		if (r >= 0x1160 && r <= 0x11ff) || (r >= 0xd7b0 && r <= 0xd7ff) {
			continue
		}
		if unicode.Is(unicode.Mc, r) {
			width += spacingMarkCellWidth
		} else {
			width += eastAsianWidth(r)
		}
	}
	if clampClusterCells && width > 2 {
		return 2
	}
	return width
}

const widthCacheSize = 512

var (
	widthCache      = map[string]int{}
	widthCacheOrder []string
)

// VisibleWidth is the width of s in terminal columns: escape sequences are
// free, a tab is three cells, wide characters two.
func VisibleWidth(s string) int {
	if s == "" {
		return 0
	}
	if isPrintableASCII(s) {
		return len(s)
	}
	if w, ok := widthCache[s]; ok {
		return w
	}
	clean := s
	if strings.Contains(clean, "\t") {
		clean = strings.ReplaceAll(clean, "\t", "   ")
	}
	if strings.Contains(clean, "\x1b") {
		clean = StripTerminalSequences(clean)
	}
	w := 0
	forEachGrapheme(clean, func(g string) { w += graphemeWidth(g) })
	if len(widthCache) >= widthCacheSize && len(widthCacheOrder) > 0 {
		delete(widthCache, widthCacheOrder[0])
		widthCacheOrder = widthCacheOrder[1:]
	}
	widthCache[s] = w
	widthCacheOrder = append(widthCacheOrder, s)
	return w
}

// StripTerminalSequences removes ANSI, OSC and APC sequences.
func StripTerminalSequences(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if _, n := ExtractAnsiCode(s, i); n > 0 {
			i += n
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// walkText calls ansi for each escape sequence and text for each run of
// plain text between them, in order.
func walkText(s string, ansi func(code string) bool, text func(run string) bool) {
	for i := 0; i < len(s); {
		if code, n := ExtractAnsiCode(s, i); n > 0 {
			if !ansi(code) {
				return
			}
			i += n
			continue
		}
		end := i
		for end < len(s) {
			if _, n := ExtractAnsiCode(s, end); n > 0 {
				break
			}
			end++
		}
		if !text(s[i:end]) {
			return
		}
		i = end
	}
}

// GraphemeCellRange is the cells a grapheme covers.
type GraphemeCellRange struct{ Start, End int }

// GetGraphemeCellRange is the grapheme under column.
func GetGraphemeCellRange(line string, column int) (GraphemeCellRange, bool) {
	col := 0
	var out GraphemeCellRange
	found := false
	walkText(line, func(string) bool { return true }, func(run string) bool {
		forEachGrapheme(run, func(g string) {
			if found {
				return
			}
			w := graphemeWidth(g)
			if w > 0 && column >= col && column < col+w {
				out, found = GraphemeCellRange{col, col + w}, true
			}
			col += w
		})
		return !found
	})
	return out, found
}

var osc8Link = regexp.MustCompile(`^\x1b\]8;[^;]*;([^\x07\x1b]*)(?:\x07|\x1b\\)$`)

// GetOsc8LinkAtColumn is the hyperlink target under column.
func GetOsc8LinkAtColumn(line string, column int) string {
	active, col := "", 0
	result, done := "", false
	walkText(line, func(code string) bool {
		if m := osc8Link.FindStringSubmatch(code); m != nil {
			active = m[1]
		}
		return true
	}, func(run string) bool {
		forEachGrapheme(run, func(g string) {
			if done {
				return
			}
			w := graphemeWidth(g)
			if column >= col && column < col+w {
				result, done = active, true
			}
			col += w
		})
		return !done
	})
	return result
}

// NormalizeTerminalOutput rewrites what terminals disagree about: Thai/Lao
// SARA AM is decomposed, and tabs become three spaces.
func NormalizeTerminalOutput(s string) string {
	if strings.ContainsAny(s, "ำຳ") {
		s = strings.NewReplacer("ำ", "ํา", "ຳ", "ໍາ").Replace(s)
	}
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if code, n := ExtractAnsiCode(s, i); n > 0 {
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

// ExtractAnsiCode returns the escape sequence at pos and its length, or 0.
// CSI ends at one of m G K H J (loop's set); OSC and APC end at BEL or ST.
func ExtractAnsiCode(s string, pos int) (string, int) {
	if pos >= len(s) || s[pos] != 0x1b || pos+1 >= len(s) {
		return "", 0
	}
	switch s[pos+1] {
	case '[':
		for j := pos + 2; j < len(s); j++ {
			switch s[j] {
			case 'm', 'G', 'K', 'H', 'J':
				return s[pos : j+1], j + 1 - pos
			}
		}
	case ']', '_':
		for j := pos + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return s[pos : j+1], j + 1 - pos
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return s[pos : j+2], j + 2 - pos
			}
		}
	}
	return "", 0
}

// ---- OSC 8 ----

type hyperlink struct{ params, url, term string }

// parseOsc8 returns (link, true) for an OSC 8 code; a nil link is a close.
func parseOsc8(code string) (*hyperlink, bool) {
	if !strings.HasPrefix(code, "\x1b]8;") {
		return nil, false
	}
	term := "\x1b\\"
	body := code[4:]
	if strings.HasSuffix(code, "\x07") {
		term = "\x07"
		body = body[:len(body)-1]
	} else if len(body) >= 2 {
		body = body[:len(body)-2]
	}
	i := strings.IndexByte(body, ';')
	if i < 0 {
		return nil, false
	}
	params, url := body[:i], body[i+1:]
	if url == "" {
		return nil, true
	}
	return &hyperlink{params, url, term}, true
}

func (h *hyperlink) open() string { return "\x1b]8;" + h.params + ";" + h.url + h.term }

func osc8Close(term string) string { return "\x1b]8;;" + term }

func activeOsc8Close(prefix string) string {
	if !strings.Contains(prefix, "\x1b]8;") {
		return ""
	}
	var active *hyperlink
	for i := 0; i < len(prefix); {
		if code, n := ExtractAnsiCode(prefix, i); n > 0 {
			if h, ok := parseOsc8(code); ok {
				active = h
			}
			i += n
		} else {
			i++
		}
	}
	if active == nil {
		return ""
	}
	return osc8Close(active.term)
}

// ---- SGR tracking ----

// AnsiCodeTracker follows SGR state (and an open OSC 8 link) so styling can
// be re-opened across line breaks.
type AnsiCodeTracker struct {
	bold, dim, italic, underline, blink, inverse, hidden, strike bool
	fg, bg                                                       string
	link                                                         *hyperlink
}

func (t *AnsiCodeTracker) Process(code string) {
	if h, ok := parseOsc8(code); ok {
		t.link = h
		return
	}
	if !strings.HasSuffix(code, "m") || !strings.HasPrefix(code, "\x1b[") {
		return
	}
	params := code[2 : len(code)-1]
	for _, c := range params {
		if c != ';' && (c < '0' || c > '9') {
			return
		}
	}
	if params == "" || params == "0" {
		t.reset()
		return
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); {
		code, _ := strconv.Atoi(parts[i])
		if code == 38 || code == 48 {
			if i+2 < len(parts) && parts[i+1] == "5" {
				c := parts[i] + ";5;" + parts[i+2]
				if code == 38 {
					t.fg = c
				} else {
					t.bg = c
				}
				i += 3
				continue
			}
			if i+4 < len(parts) && parts[i+1] == "2" {
				c := strings.Join(parts[i:i+5], ";")
				if code == 38 {
					t.fg = c
				} else {
					t.bg = c
				}
				i += 5
				continue
			}
		}
		switch code {
		case 0:
			t.reset()
		case 1:
			t.bold = true
		case 2:
			t.dim = true
		case 3:
			t.italic = true
		case 4:
			t.underline = true
		case 5:
			t.blink = true
		case 7:
			t.inverse = true
		case 8:
			t.hidden = true
		case 9:
			t.strike = true
		case 21:
			t.bold = false
		case 22:
			t.bold, t.dim = false, false
		case 23:
			t.italic = false
		case 24:
			t.underline = false
		case 25:
			t.blink = false
		case 27:
			t.inverse = false
		case 28:
			t.hidden = false
		case 29:
			t.strike = false
		case 39:
			t.fg = ""
		case 49:
			t.bg = ""
		default:
			if (code >= 30 && code <= 37) || (code >= 90 && code <= 97) {
				t.fg = strconv.Itoa(code)
			} else if (code >= 40 && code <= 47) || (code >= 100 && code <= 107) {
				t.bg = strconv.Itoa(code)
			}
		}
		i++
	}
}

// reset is an SGR reset; it does not close an OSC 8 link.
func (t *AnsiCodeTracker) reset() {
	link := t.link
	*t = AnsiCodeTracker{}
	t.link = link
}

// Clear drops all state, link included.
func (t *AnsiCodeTracker) Clear() { *t = AnsiCodeTracker{} }

// ActiveCodes re-opens everything currently active.
func (t *AnsiCodeTracker) ActiveCodes() string {
	var codes []string
	for _, c := range []struct {
		on   bool
		code string
	}{{t.bold, "1"}, {t.dim, "2"}, {t.italic, "3"}, {t.underline, "4"}, {t.blink, "5"},
		{t.inverse, "7"}, {t.hidden, "8"}, {t.strike, "9"}} {
		if c.on {
			codes = append(codes, c.code)
		}
	}
	if t.fg != "" {
		codes = append(codes, t.fg)
	}
	if t.bg != "" {
		codes = append(codes, t.bg)
	}
	out := ""
	if len(codes) > 0 {
		out = "\x1b[" + strings.Join(codes, ";") + "m"
	}
	if t.link != nil {
		out += t.link.open()
	}
	return out
}

// HasActiveCodes reports whether anything is open.
func (t *AnsiCodeTracker) HasActiveCodes() bool {
	return t.bold || t.dim || t.italic || t.underline || t.blink || t.inverse || t.hidden ||
		t.strike || t.fg != "" || t.bg != "" || t.link != nil
}

// LineEndReset closes what would bleed into a line's padding: underline, and
// an open link (re-opened on the next line by ActiveCodes).
func (t *AnsiCodeTracker) LineEndReset() string {
	out := ""
	if t.underline {
		out += "\x1b[24m"
	}
	if t.link != nil {
		out += osc8Close(t.link.term)
	}
	return out
}

func updateTrackerFromText(text string, t *AnsiCodeTracker) {
	for i := 0; i < len(text); {
		if code, n := ExtractAnsiCode(text, i); n > 0 {
			t.Process(code)
			i += n
		} else {
			i++
		}
	}
}

// ---- wrapping ----

// splitIntoTokensWithAnsi splits text into runs of spaces and words (each CJK
// character its own token), escape codes attached to the next visible text.
func splitIntoTokensWithAnsi(text string) []string {
	var tokens []string
	var cur strings.Builder
	pending := ""
	kind := 0 // 0 none, 1 space, 2 word
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		tokens = append(tokens, cur.String())
		cur.Reset()
		kind = 0
	}
	walkText(text, func(code string) bool {
		pending += code
		return true
	}, func(run string) bool {
		forEachGrapheme(run, func(seg string) {
			isSpace := seg == " "
			if !isSpace && cjkBreak.MatchString(seg) {
				flush()
				tokens = append(tokens, pending+seg)
				pending = ""
				return
			}
			k := 2
			if isSpace {
				k = 1
			}
			if cur.Len() > 0 && kind != k {
				flush()
			}
			if pending != "" {
				cur.WriteString(pending)
				pending = ""
			}
			kind = k
			cur.WriteString(seg)
		})
		return true
	})
	if pending != "" {
		if cur.Len() > 0 {
			cur.WriteString(pending)
		} else if len(tokens) > 0 {
			tokens[len(tokens)-1] += pending
		} else {
			cur.WriteString(pending)
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

var lineBreaks = regexp.MustCompile(`\r\n|\r|\n`)

// WrapTextWithAnsi word-wraps text to width, carrying styles across both
// literal newlines and wrap points. Lines are not padded.
func WrapTextWithAnsi(text string, width int) []string {
	if text == "" {
		return []string{""}
	}
	var result []string
	var tracker AnsiCodeTracker
	for _, line := range lineBreaks.Split(text, -1) {
		prefix := ""
		if len(result) > 0 {
			prefix = tracker.ActiveCodes()
		}
		result = append(result, wrapSingleLine(prefix+line, width)...)
		updateTrackerFromText(line, &tracker)
	}
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

func wrapSingleLine(line string, width int) []string {
	if line == "" {
		return []string{""}
	}
	if VisibleWidth(line) <= width {
		return []string{line}
	}
	var wrapped []string
	var tracker AnsiCodeTracker
	cur := ""
	curW := 0
	for _, token := range splitIntoTokensWithAnsi(line) {
		tw := VisibleWidth(token)
		ws := strings.TrimSpace(StripTerminalSequences(token)) == ""
		if tw > width && !ws {
			if cur != "" {
				cur += tracker.LineEndReset()
				wrapped = append(wrapped, cur)
				cur, curW = "", 0
			}
			broken := breakLongWord(token, width, &tracker)
			wrapped = append(wrapped, broken[:len(broken)-1]...)
			cur = broken[len(broken)-1]
			curW = VisibleWidth(cur)
			continue
		}
		if curW+tw > width && curW > 0 {
			l := strings.TrimRight(cur, " ") + tracker.LineEndReset()
			wrapped = append(wrapped, l)
			if ws {
				cur, curW = tracker.ActiveCodes(), 0
			} else {
				cur, curW = tracker.ActiveCodes()+token, tw
			}
		} else {
			cur += token
			curW += tw
		}
		updateTrackerFromText(token, &tracker)
	}
	if cur != "" {
		wrapped = append(wrapped, cur)
	}
	if len(wrapped) == 0 {
		return []string{""}
	}
	for i, l := range wrapped {
		wrapped[i] = strings.TrimRight(l, " ")
	}
	return wrapped
}

// IsWhitespaceChar reports whether ch is whitespace.
func IsWhitespaceChar(ch string) bool {
	r, _ := utf8.DecodeRuneInString(ch)
	return unicode.IsSpace(r)
}

// PunctuationChars is loop's PUNCTUATION_REGEX class.
const PunctuationChars = "(){}[]<>.,;:'\"!?+-=*/\\|&%^$#@~`"

// IsPunctuation reports membership of PunctuationChars.
func IsPunctuation(ch string) bool {
	return len(ch) == 1 && strings.Contains(PunctuationChars, ch)
}

func breakLongWord(word string, width int, t *AnsiCodeTracker) []string {
	var lines []string
	cur := t.ActiveCodes()
	curW := 0
	walkText(word, func(code string) bool {
		cur += code
		t.Process(code)
		return true
	}, func(run string) bool {
		forEachGrapheme(run, func(g string) {
			if g == "" {
				return
			}
			gw := VisibleWidth(g)
			if curW+gw > width {
				cur += t.LineEndReset()
				lines = append(lines, cur)
				cur, curW = t.ActiveCodes(), 0
			}
			cur += g
			curW += gw
		})
		return true
	})
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

// ApplyBackgroundToLine pads line to width and applies bg across it.
func ApplyBackgroundToLine(line string, width int, bg func(string) string) string {
	return bg(line + strings.Repeat(" ", max(0, width-VisibleWidth(line))))
}

// ---- truncation ----

func truncateFragmentToWidth(text string, maxWidth int) (string, int) {
	if maxWidth <= 0 || text == "" {
		return "", 0
	}
	if isPrintableASCII(text) {
		c := text[:min(len(text), maxWidth)]
		return c, len(c)
	}
	var b strings.Builder
	width := 0
	pending := ""
	stop := false
	walkText(text, func(code string) bool {
		pending += code
		return true
	}, func(run string) bool {
		forEachGrapheme(run, func(g string) {
			if stop {
				return
			}
			w := graphemeWidth(g)
			if width+w > maxWidth {
				stop = true
				return
			}
			b.WriteString(pending)
			pending = ""
			b.WriteString(g)
			width += w
		})
		return !stop
	})
	return b.String(), width
}

func finalizeTruncated(prefix string, prefixW int, ellipsis string, ellipsisW, maxWidth int, pad bool) string {
	const reset = "\x1b[0m"
	out := prefix + activeOsc8Close(prefix) + reset
	if ellipsis != "" {
		out += ellipsis + reset
	}
	if pad {
		out += strings.Repeat(" ", max(0, maxWidth-prefixW-ellipsisW))
	}
	return out
}

// TruncateToWidth cuts text to maxWidth cells, appending ellipsis when it
// cuts; pad fills the result out to exactly maxWidth.
func TruncateToWidth(text string, maxWidth int, ellipsis string, pad bool) string {
	if maxWidth <= 0 {
		return ""
	}
	if text == "" {
		if pad {
			return strings.Repeat(" ", maxWidth)
		}
		return ""
	}
	ew := VisibleWidth(ellipsis)
	if ew >= maxWidth {
		tw := VisibleWidth(text)
		if tw <= maxWidth {
			if pad {
				return text + strings.Repeat(" ", maxWidth-tw)
			}
			return text
		}
		ce, cw := truncateFragmentToWidth(ellipsis, maxWidth)
		if cw == 0 {
			if pad {
				return strings.Repeat(" ", maxWidth)
			}
			return ""
		}
		return finalizeTruncated("", 0, ce, cw, maxWidth, pad)
	}
	if isPrintableASCII(text) {
		if len(text) <= maxWidth {
			if pad {
				return text + strings.Repeat(" ", maxWidth-len(text))
			}
			return text
		}
		target := maxWidth - ew
		return finalizeTruncated(text[:target], target, ellipsis, ew, maxWidth, pad)
	}
	target := maxWidth - ew
	var b strings.Builder
	pending := ""
	seen, kept := 0, 0
	keep, overflow := true, false
	walkText(text, func(code string) bool {
		pending += code
		return true
	}, func(run string) bool {
		forEachGrapheme(run, func(g string) {
			if overflow {
				return
			}
			w := graphemeWidth(g)
			if keep && kept+w <= target {
				b.WriteString(pending)
				pending = ""
				b.WriteString(g)
				kept += w
			} else {
				keep = false
				pending = ""
			}
			seen += w
			if seen > maxWidth {
				overflow = true
			}
		})
		return !overflow
	})
	if !overflow {
		if pad {
			return text + strings.Repeat(" ", max(0, maxWidth-seen))
		}
		return text
	}
	return finalizeTruncated(b.String(), kept, ellipsis, ew, maxWidth, pad)
}

// ---- column slicing ----

// SliceByColumn extracts visible columns [start, start+length). strict drops
// a wide character that would straddle the end.
func SliceByColumn(line string, start, length int, strict bool) string {
	s, _ := SliceWithWidth(line, start, length, strict)
	return s
}

// SliceWithWidth is SliceByColumn plus the width of what it returned.
func SliceWithWidth(line string, startCol, length int, strict bool) (string, int) {
	if length <= 0 {
		return "", 0
	}
	endCol := startCol + length
	var b strings.Builder
	pending := ""
	width, col := 0, 0
	walkText(line, func(code string) bool {
		if col >= startCol && col < endCol {
			b.WriteString(code)
		} else if col < startCol {
			pending += code
		}
		return true
	}, func(run string) bool {
		forEachGrapheme(run, func(g string) {
			if col >= endCol {
				return
			}
			w := graphemeWidth(g)
			if col >= startCol && col < endCol && (!strict || col+w <= endCol) {
				if pending != "" {
					b.WriteString(pending)
					pending = ""
				}
				b.WriteString(g)
				width += w
			}
			col += w
		})
		return col < endCol
	})
	return b.String(), width
}

// Segments is ExtractSegments' result.
type Segments struct {
	Before      string
	BeforeWidth int
	After       string
	AfterWidth  int
}

// ExtractSegments cuts line into [0, beforeEnd) and [afterStart,
// afterStart+afterLen); the after part re-opens the styles active where it
// starts.
func ExtractSegments(line string, beforeEnd, afterStart, afterLen int, strictAfter bool) Segments {
	var seg Segments
	var before, after strings.Builder
	pendingBefore := ""
	afterStarted := false
	afterEnd := afterStart + afterLen
	var tracker AnsiCodeTracker
	col := 0
	done := func() bool {
		if afterLen <= 0 {
			return col >= beforeEnd
		}
		return col >= afterEnd
	}
	walkText(line, func(code string) bool {
		tracker.Process(code)
		if col < beforeEnd {
			pendingBefore += code
		} else if col >= afterStart && col < afterEnd && afterStarted {
			after.WriteString(code)
		}
		return true
	}, func(run string) bool {
		forEachGrapheme(run, func(g string) {
			if done() {
				return
			}
			w := graphemeWidth(g)
			if col < beforeEnd && col+w <= beforeEnd {
				if pendingBefore != "" {
					before.WriteString(pendingBefore)
					pendingBefore = ""
				}
				before.WriteString(g)
				seg.BeforeWidth += w
			} else if col >= afterStart && col < afterEnd {
				if !strictAfter || col+w <= afterEnd {
					if !afterStarted {
						after.WriteString(tracker.ActiveCodes())
						afterStarted = true
					}
					after.WriteString(g)
					seg.AfterWidth += w
				}
			}
			col += w
		})
		return !done()
	})
	seg.Before, seg.After = before.String(), after.String()
	return seg
}

// ---- words ----

// WordSegment is one segment of WordSegments: a word-like run or a
// separator, like Intl.Segmenter's word granularity.
type WordSegment struct {
	Segment  string
	WordLike bool
}

// WordSegments splits s like Intl.Segmenter({granularity: "word"}): letters,
// digits and connectors form words; everything else is its own segment.
func WordSegments(s string) []WordSegment {
	var out []WordSegment
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, WordSegment{cur.String(), true})
			cur.Reset()
		}
	}
	forEachGrapheme(s, func(g string) {
		r, _ := utf8.DecodeRuneInString(g)
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || unicode.Is(unicode.Mn, r) {
			cur.WriteString(g)
			return
		}
		flush()
		out = append(out, WordSegment{g, false})
	})
	flush()
	return out
}
