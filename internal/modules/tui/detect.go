package tui

import (
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
//  1. COLORFGBG, set by a few terminals. Cheap and synchronous.
//  2. An OSC 11 query, which asks the terminal directly and reads the reply.
//     Authoritative, but it needs raw mode and a timeout — a terminal that
//     does not answer must cost a few milliseconds, not the startup.
//
// Neither is guaranteed, so both fail closed to "unknown" and the caller
// keeps its default rather than guessing.

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
	if bg := backgroundFromEnv(); bg != BackgroundUnknown {
		return bg
	}
	return backgroundFromOSC()
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

// backgroundFromOSC asks the terminal with OSC 11 and parses the reply.
//
// The reply looks like `ESC ] 11 ; rgb:RRRR/GGGG/BBBB ESC \`. Anything else —
// including silence — is unknown.
func backgroundFromOSC() Background {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return BackgroundUnknown
	}
	reply, ok := askTerminal("\x1b]11;?\x07", bgQueryTimeout)
	if !ok {
		return BackgroundUnknown
	}
	return parseOSCBackground(reply)
}

// parseOSCBackground pulls the luminance out of an OSC 11 reply.
func parseOSCBackground(s string) Background {
	i := strings.Index(s, "rgb:")
	if i < 0 {
		return BackgroundUnknown
	}
	parts := strings.Split(strings.TrimRight(s[i+4:], "\x07\x1b\\ \n"), "/")
	if len(parts) < 3 {
		return BackgroundUnknown
	}
	var channels [3]float64
	for i := 0; i < 3; i++ {
		hex := parts[i]
		if len(hex) > 4 {
			hex = hex[:4]
		}
		v, err := strconv.ParseUint(hex, 16, 32)
		if err != nil {
			return BackgroundUnknown
		}
		// Replies come in 4, 8, 12 or 16 bits per channel; normalise by the
		// width actually sent rather than assuming 16.
		full := float64(uint64(1)<<(4*len(hex))) - 1
		channels[i] = float64(v) / full
	}
	lum := 0.299*channels[0] + 0.587*channels[1] + 0.114*channels[2]
	if lum > 0.5 {
		return BackgroundLight
	}
	return BackgroundDark
}
