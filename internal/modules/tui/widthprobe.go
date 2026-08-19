package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Measuring how the terminal lays out grapheme clusters, instead of guessing.
//
// Terminals genuinely disagree. A Devanagari cluster like प्रे is laid out
// per spacing codepoint by some and clamped to one or two cells by others,
// and no environment variable says which. Guessing wrong drifts the cursor
// and smears the grid, so the mode is MEASURED: print a canary, ask where the
// cursor ended up with a Cursor Position Report, and compare.
//
// Three canaries rather than one, because the disagreements are independent —
// a terminal that shapes Devanagari may still not clamp a long emoji ZWJ
// sequence. The strictest observation wins.

// probeTimeout bounds each CPR wait. A terminal that does not answer must
// cost a few milliseconds, not the startup.
const probeTimeout = 150 * time.Millisecond

// canary is one probe: what to print and what each mode predicts.
type canary struct {
	text string
	// sum is the per-codepoint width; shaped is the base codepoint's width.
	sum, shaped int
}

// canaries cover the three cases the modes disagree about.
var canaries = []canary{
	// Devanagari: three spacing codepoints under wmSum, one cluster shaped.
	{text: "प्रे", sum: 2, shaped: 1},
	// A ZWJ family: three wide bases summed, one emoji shaped or clamped.
	{text: "👨‍👩‍👧", sum: 6, shaped: 2},
	// A Thai cluster with a spacing mark.
	{text: "ก็", sum: 1, shaped: 1},
}

// ProbeWidths measures the terminal and installs the matching mode.
//
// MUST run with the terminal already in raw mode and BEFORE the key decoder
// starts: it reads the reply from stdin, and a decoder running alongside it
// would race for those bytes — swallowing the answer, or injecting the escape
// sequence into the draft as keystrokes.
//
// Silently does nothing when stdin or stdout is not a terminal, or when the
// terminal declines to answer — the default per-codepoint mode is the safest
// of the three, because it over-estimates rather than under-estimates and an
// over-estimate leaves a gap where an under-estimate corrupts the line.
func ProbeWidths() {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return
	}
	mode := wmSum
	for _, c := range canaries {
		got, ok := measure(c.text)
		if !ok {
			return // no answer: keep the safe default
		}
		switch {
		case got == c.shaped && c.shaped != c.sum:
			mode = wmShaped
		case got > c.shaped && got < c.sum:
			// Neither prediction: the terminal caps a cluster at two cells.
			mode = wmClamp
		}
	}
	SetWidthMode(mode)
}

// measure prints text and reports how many columns the cursor advanced.
func measure(text string) (int, bool) {
	// Carriage return first so the measurement starts from a known column,
	// and the line is cleared afterwards so the canary never shows.
	defer fmt.Fprint(os.Stdout, "\r\x1b[K")

	reply, ok := askTerminal("\r"+text+"\x1b[6n", probeTimeout)
	if !ok {
		return 0, false
	}
	col, ok := parseCPR(reply)
	if !ok {
		return 0, false
	}
	// CPR columns are 1-indexed, so the advance is one less.
	return col - 1, true
}

// parseCPR reads `ESC [ row ; col R`.
func parseCPR(s string) (int, bool) {
	i := strings.LastIndex(s, "\x1b[")
	if i < 0 || !strings.HasSuffix(strings.TrimSpace(s), "R") {
		return 0, false
	}
	body := strings.TrimSuffix(strings.TrimSpace(s[i+2:]), "R")
	_, colStr, ok := strings.Cut(body, ";")
	if !ok {
		return 0, false
	}
	col, err := strconv.Atoi(colStr)
	if err != nil {
		return 0, false
	}
	return col, true
}
