package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Terminal cell widths, ported from loop's packages/tui/src/utils.ts.
//
// A rune count is not a width: CJK and emoji take two cells, combining marks
// take none, and a Devanagari cluster like प्रे is several runes wide but is
// laid out per spacing codepoint by terminals that do not shape. Getting this
// wrong drifts the cursor and smears the whole grid, so every layout decision
// in this package goes through visibleWidth.

// widthMode is how the terminal lays out a grapheme cluster. Terminals differ
// and cannot be detected from the environment alone; a CPR probe at startup
// picks the mode (see ProbeWidths).
type widthMode int

const (
	// wmSum is the default: each spacing codepoint gets its own cell.
	wmSum widthMode = iota
	// wmShaped fits a whole cluster in the base codepoint's cells.
	wmShaped
	// wmClamp is wmSum capped at two cells per cluster.
	wmClamp
)

var activeWidthMode = wmSum

// SetWidthMode installs the layout mode measured by the startup probe.
func SetWidthMode(m widthMode) { activeWidthMode = m }

// visibleWidth is the number of terminal cells s occupies, ignoring ANSI
// escape sequences and counting a tab as three cells.
func visibleWidth(s string) int {
	if s == "" {
		return 0
	}
	if ascii, w := asciiWidth(s); ascii {
		return w
	}
	clean := s
	if strings.ContainsRune(clean, 0x1b) {
		clean = stripANSI(clean)
	}
	w := 0
	forEachGrapheme(clean, func(g string) {
		w += graphemeWidth(g)
	})
	return w
}

// asciiWidth is the fast path: printable ASCII is one cell per byte.
func asciiWidth(s string) (bool, int) {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false, 0
		}
	}
	return true, len(s)
}

// graphemeWidth is the cell width of one grapheme cluster.
func graphemeWidth(g string) int {
	if g == "\t" {
		return 3
	}
	if g == "" {
		return 0
	}

	first, _ := utf8.DecodeRuneInString(g)

	// An explicit emoji presentation selector forces the two-cell form even
	// on a base that defaults to text (❤ vs ❤️).
	if strings.ContainsRune(g, 0xfe0f) {
		return 2
	}
	// Regional indicators render full-width even alone mid-stream. Staying
	// conservative here avoids auto-wrap drift while a flag streams in.
	if first >= 0x1f1e6 && first <= 0x1f1ff {
		return 2
	}
	// A ZWJ sequence is ONE emoji however many bases it joins: 👨‍👩‍👧 is three
	// wide codepoints that render in a single pair of cells. Summing them
	// would claim six.
	if strings.ContainsRune(g, 0x200d) && eastAsianWidth(first) == 2 {
		return 2
	}
	if isZeroWidthCluster(g) {
		return 0
	}

	if activeWidthMode == wmShaped {
		return eastAsianWidth(first)
	}

	// wcwidth-style per-codepoint sum. Nonspacing/enclosing marks and format
	// characters take no cell, Hangul V/T jamo compose into the preceding
	// leading jamo's cell, and spacing combining marks (Mc — the Indic matras
	// ा ी ो) take one.
	w := 0
	for _, r := range g {
		if isZeroCell(r) {
			continue
		}
		if (r >= 0x1160 && r <= 0x11ff) || (r >= 0xd7b0 && r <= 0xd7ff) {
			continue
		}
		if unicode.Is(unicode.Mc, r) {
			w++
			continue
		}
		w += eastAsianWidth(r)
	}
	if activeWidthMode == wmClamp && w > 2 {
		return 2
	}
	return w
}

// isZeroWidthCluster reports whether every rune in the cluster is zero-cell.
func isZeroWidthCluster(g string) bool {
	for _, r := range g {
		if !isZeroCell(r) {
			return false
		}
	}
	return true
}

// isZeroCell reports whether r occupies no cell of its own.
func isZeroCell(r rune) bool {
	switch {
	case r == 0x200d: // zero-width joiner
		return true
	case r == 0x200b || r == 0x200c: // zero-width space / non-joiner
		return true
	case r >= 0xfe00 && r <= 0xfe0f: // variation selectors
		return true
	case r >= 0xe0100 && r <= 0xe01ef: // variation selectors supplement
		return true
	case r >= 0xe0020 && r <= 0xe007f: // tag characters (flag sequences)
		return true
	}
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r)
}

// forEachGrapheme splits s into grapheme clusters and calls fn for each.
//
// A cut-down UAX #29: a cluster is a base plus the marks, joiners, and
// selectors that hang off it, plus the CR LF, Hangul jamo, regional
// indicator pair, and ZWJ-sequence cases. Terminals only need the clusters
// to be stable, not the full algorithm's edge cases.
func forEachGrapheme(s string, fn func(string)) {
	i := 0
	for i < len(s) {
		r, sz := utf8.DecodeRuneInString(s[i:])
		start := i
		i += sz

		if r == '\r' && i < len(s) && s[i] == '\n' {
			i++
			fn(s[start:i])
			continue
		}
		// A regional indicator binds to exactly one following indicator, so a
		// run of four is two flags rather than one long cluster.
		if r >= 0x1f1e6 && r <= 0x1f1ff {
			if next, nsz := utf8.DecodeRuneInString(s[i:]); i < len(s) && next >= 0x1f1e6 && next <= 0x1f1ff {
				i += nsz
			}
		}

		for i < len(s) {
			next, nsz := utf8.DecodeRuneInString(s[i:])
			switch {
			case isZeroCell(next) && next != 0x200d:
				i += nsz
			case unicode.Is(unicode.Mc, next):
				i += nsz
			case next == 0x200d:
				// ZWJ pulls the following codepoint into the cluster.
				i += nsz
				if i < len(s) {
					_, jsz := utf8.DecodeRuneInString(s[i:])
					i += jsz
				}
			case next >= 0x1160 && next <= 0x11ff, next >= 0xd7b0 && next <= 0xd7ff:
				// Hangul V/T jamo compose onto the preceding syllable.
				i += nsz
			case next == 0x20e3:
				// Combining enclosing keycap.
				i += nsz
			default:
				fn(s[start:i])
				goto next
			}
		}
		fn(s[start:i])
	next:
	}
}

// eastAsianWidth is 2 for Wide and Fullwidth codepoints, 0 for C0/C1
// controls, 1 otherwise.
func eastAsianWidth(r rune) int {
	if r < 0x20 || (r >= 0x7f && r < 0xa0) {
		return 0
	}
	if r < 0x1100 {
		return 1
	}
	lo, hi := 0, len(wideRanges)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case r < wideRanges[mid][0]:
			hi = mid - 1
		case r > wideRanges[mid][1]:
			lo = mid + 1
		default:
			return 2
		}
	}
	return 1
}

// wideRanges are the Unicode East Asian Wide + Fullwidth blocks, sorted.
var wideRanges = [][2]rune{
	{0x1100, 0x115f}, {0x231a, 0x231b}, {0x2329, 0x232a}, {0x23e9, 0x23ec},
	{0x23f0, 0x23f0}, {0x23f3, 0x23f3}, {0x25fd, 0x25fe}, {0x2614, 0x2615},
	{0x2648, 0x2653}, {0x267f, 0x267f}, {0x2693, 0x2693}, {0x26a1, 0x26a1},
	{0x26aa, 0x26ab}, {0x26bd, 0x26be}, {0x26c4, 0x26c5}, {0x26ce, 0x26ce},
	{0x26d4, 0x26d4}, {0x26ea, 0x26ea}, {0x26f2, 0x26f3}, {0x26f5, 0x26f5},
	{0x26fa, 0x26fa}, {0x26fd, 0x26fd}, {0x2705, 0x2705}, {0x270a, 0x270b},
	{0x2728, 0x2728}, {0x274c, 0x274c}, {0x274e, 0x274e}, {0x2753, 0x2755},
	{0x2757, 0x2757}, {0x2795, 0x2797}, {0x27b0, 0x27b0}, {0x27bf, 0x27bf},
	{0x2b1b, 0x2b1c}, {0x2b50, 0x2b50}, {0x2b55, 0x2b55}, {0x2e80, 0x2e99},
	{0x2e9b, 0x2ef3}, {0x2f00, 0x2fd5}, {0x2ff0, 0x2ffb}, {0x3000, 0x303e},
	{0x3041, 0x3096}, {0x3099, 0x30ff}, {0x3105, 0x312f}, {0x3131, 0x318e},
	{0x3190, 0x31e3}, {0x31f0, 0x321e}, {0x3220, 0x3247}, {0x3250, 0x4dbf},
	{0x4e00, 0xa48c}, {0xa490, 0xa4c6}, {0xa960, 0xa97c}, {0xac00, 0xd7a3},
	{0xf900, 0xfaff}, {0xfe10, 0xfe19}, {0xfe30, 0xfe52}, {0xfe54, 0xfe66},
	{0xfe68, 0xfe6b}, {0xff01, 0xff60}, {0xffe0, 0xffe6},
	{0x16fe0, 0x16fe4}, {0x16ff0, 0x16ff1}, {0x17000, 0x187f7},
	{0x18800, 0x18cd5}, {0x18d00, 0x18d08}, {0x1aff0, 0x1aff3},
	{0x1aff5, 0x1affb}, {0x1affd, 0x1affe}, {0x1b000, 0x1b122},
	{0x1b150, 0x1b152}, {0x1b164, 0x1b167}, {0x1b170, 0x1b2fb},
	{0x1f004, 0x1f004}, {0x1f0cf, 0x1f0cf}, {0x1f18e, 0x1f18e},
	{0x1f191, 0x1f19a}, {0x1f200, 0x1f320}, {0x1f32d, 0x1f335},
	{0x1f337, 0x1f37c}, {0x1f37e, 0x1f393}, {0x1f3a0, 0x1f3ca},
	{0x1f3cf, 0x1f3d3}, {0x1f3e0, 0x1f3f0}, {0x1f3f4, 0x1f3f4},
	{0x1f3f8, 0x1f43e}, {0x1f440, 0x1f440}, {0x1f442, 0x1f4fc},
	{0x1f4ff, 0x1f53d}, {0x1f54b, 0x1f54e}, {0x1f550, 0x1f567},
	{0x1f57a, 0x1f57a}, {0x1f595, 0x1f596}, {0x1f5a4, 0x1f5a4},
	{0x1f5fb, 0x1f64f}, {0x1f680, 0x1f6c5}, {0x1f6cc, 0x1f6cc},
	{0x1f6d0, 0x1f6d2}, {0x1f6d5, 0x1f6d7}, {0x1f6dd, 0x1f6df},
	{0x1f6eb, 0x1f6ec}, {0x1f6f4, 0x1f6fc}, {0x1f7e0, 0x1f7eb},
	{0x1f7f0, 0x1f7f0}, {0x1f90c, 0x1f93a}, {0x1f93c, 0x1f945},
	{0x1f947, 0x1f9ff}, {0x1fa70, 0x1fa74}, {0x1fa78, 0x1fa7c},
	{0x1fa80, 0x1fa86}, {0x1fa90, 0x1faac}, {0x1fab0, 0x1faba},
	{0x1fac0, 0x1fac5}, {0x1fad0, 0x1fad9}, {0x1fae0, 0x1fae7},
	{0x1faf0, 0x1faf6}, {0x20000, 0x2fffd}, {0x30000, 0x3fffd},
}

// displayWidth is visibleWidth under its historical name.
func displayWidth(s string) int { return visibleWidth(s) }
