package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Detecting the terminal's own background, so a light terminal does not get a
// dark theme painted over it.
//
// Two sources, in order of trust:
//
//  1. An OSC 11 query, which asks the terminal directly and reads the reply.
//     Authoritative — it is the colour of the surface being drawn on — but it
//     needs raw mode and a timeout, because a terminal that does not answer
//     must cost a few milliseconds, not the startup.
//  2. COLORFGBG, set by a few terminals. Cheap and synchronous, but it names
//     an ANSI palette INDEX rather than a colour, so it can only ever answer
//     "light or dark" — never what `system` needs, which is the canvas itself.
//
// Neither is guaranteed, so both fail closed to "unknown" and the caller
// keeps its default rather than guessing.
//
// The query is asked ONCE, here, and the answer is remembered. Every reply a
// terminal sends arrives as INPUT, so asking later — when the key decoder
// exists — races the user's typing for stdin, and an OSC 11 reply ends in BEL,
// which is ctrl+g. loop learned that the expensive way: replies landing in the
// keyboard sent prompts nobody typed.

// Background is what the terminal is sitting on.
type Background int

const (
	BackgroundUnknown Background = iota
	BackgroundDark
	BackgroundLight
)

// bgQueryTimeout bounds the OSC 11 wait. Long enough for a local terminal to
// answer, short enough that a terminal which never will is not noticed.
const bgQueryTimeout = 120 * time.Millisecond

// DetectBackground reports the terminal's background, or unknown.
//
// Same rules as ProbeWidths: raw mode already on, and before the key decoder
// exists, because it reads its answer from stdin.
func DetectBackground() Background {
	bg, _ := DetectBackgroundColor()
	return bg
}

// DetectBackgroundColor reports the terminal's background and, when it named a
// colour rather than merely reading as dark, that colour as #rrggbb.
//
// The colour is what noir's `system` theme rebuilds its whole ramp against, so
// it is worth the query even when COLORFGBG could have answered the cheaper
// half of the question.
func DetectBackgroundColor() (Background, string) {
	if hex, ok := backgroundColorFromOSC(); ok {
		if Luminance(hex) > 0.5 {
			return BackgroundLight, hex
		}
		return BackgroundDark, hex
	}
	return backgroundFromEnv(), ""
}

// backgroundFromEnv reads COLORFGBG, formatted "fg;bg" with ANSI indices.
func backgroundFromEnv() Background {
	v := os.Getenv("COLORFGBG")
	if v == "" {
		return BackgroundUnknown
	}
	parts := strings.Split(v, ";")
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return BackgroundUnknown
	}
	// 0-6 and 8 are the dark half of the base palette; 7 and 9-15 are light.
	if n == 7 || n >= 9 {
		return BackgroundLight
	}
	return BackgroundDark
}

// backgroundColorFromOSC asks the terminal with OSC 11 and parses the reply.
//
// The reply looks like `ESC ] 11 ; rgb:RRRR/GGGG/BBBB ESC \`. Anything else —
// including silence — means we were told nothing.
func backgroundColorFromOSC() (string, bool) {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return "", false
	}
	reply, ok := askTerminal("\x1b]11;?\x07", bgQueryTimeout)
	if !ok {
		return "", false
	}
	return parseOSCColor(reply)
}

// parseOSCBackground reads an OSC 11 reply as a polarity.
func parseOSCBackground(s string) Background {
	hex, ok := parseOSCColor(s)
	if !ok {
		return BackgroundUnknown
	}
	if Luminance(hex) > 0.5 {
		return BackgroundLight
	}
	return BackgroundDark
}

// parseOSCColor pulls the colour out of an OSC 11 reply, as #rrggbb.
func parseOSCColor(s string) (string, bool) {
	i := strings.Index(s, "rgb:")
	if i < 0 {
		return "", false
	}
	parts := strings.Split(strings.TrimRight(s[i+4:], "\x07\x1b\\ \n"), "/")
	if len(parts) < 3 {
		return "", false
	}
	var channels [3]int
	for i := 0; i < 3; i++ {
		hex := parts[i]
		if hex == "" {
			return "", false
		}
		if len(hex) > 4 {
			hex = hex[:4]
		}
		v, err := strconv.ParseUint(hex, 16, 32)
		if err != nil {
			return "", false
		}
		// Replies come in 4, 8, 12 or 16 bits per channel; normalise by the
		// width actually sent rather than assuming 16, then scale to 8-bit.
		full := float64(uint64(1)<<(4*len(hex))) - 1
		channels[i] = int(float64(v)/full*255 + 0.5)
	}
	return fmt.Sprintf("#%02x%02x%02x", channels[0], channels[1], channels[2]), true
}
