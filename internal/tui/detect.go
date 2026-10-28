package tui

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"runtime"
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

// loop's system-scheme.ts windows: the colour may take a while on a loaded
// machine (a reply that misses its window is an answer thrown away), the
// scheme report is only asked when the colour did not come back. Both waits
// end early on the DA1 answer that follows them, so a terminal that replies
// costs nothing and one that never will costs at most the window.
const (
	bgQueryTimeout     = 1500 * time.Millisecond
	schemeQueryTimeout = 250 * time.Millisecond
)

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
//
// loop's resolveScheme: the BACKGROUND COLOUR decides (polarity and canvas).
// The colour-scheme report is the fallback for a terminal that will not name
// its background — except on macOS, where it reports the OS appearance, not
// the terminal. Neither: unknown (system keeps the dark set on its safe
// canvas, legible on any dark background).
func DetectBackgroundColor() (Background, string) {
	if hex, ok := backgroundColorFromOSC(); ok {
		if Luminance(hex) > 0.5 {
			return BackgroundLight, hex
		}
		return BackgroundDark, hex
	}
	if runtime.GOOS == "darwin" {
		return BackgroundUnknown, ""
	}
	switch colorSchemeReport() {
	case "light":
		return BackgroundLight, ""
	case "dark":
		return BackgroundDark, ""
	}
	return BackgroundUnknown, ""
}

var da1Reply = regexp.MustCompile(`\x1b\[\?[0-9;]*c`)

// askUntilDA1 sends query then DA1 and collects what comes back until the
// DA1 answer (every terminal sends one) or the deadline.
func askUntilDA1(query string, timeout time.Duration) string {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return ""
	}
	deadline := time.Now().Add(timeout)
	if os.Stdin.SetReadDeadline(deadline) != nil {
		return ""
	}
	defer os.Stdin.SetReadDeadline(time.Time{})
	if _, err := os.Stdout.WriteString(query + "\x1b[c"); err != nil {
		return ""
	}
	var got []byte
	buf := make([]byte, 256)
	for time.Now().Before(deadline) {
		n, err := os.Stdin.Read(buf)
		got = append(got, buf[:n]...)
		if da1Reply.Match(got) || err != nil {
			break
		}
	}
	return string(got)
}

var schemeReply = regexp.MustCompile(`\x1b\[\?997;([12])n`)

// colorSchemeReport is CSI ? 996 n: "dark", "light", or "".
func colorSchemeReport() string {
	m := schemeReply.FindStringSubmatch(askUntilDA1("\x1b[?996n", schemeQueryTimeout))
	switch {
	case m == nil:
		return ""
	case m[1] == "2":
		return "light"
	}
	return "dark"
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
	return parseOSCColor(askUntilDA1("\x1b]11;?\x07", bgQueryTimeout))
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
var osc11Reply = regexp.MustCompile(`(?i)\x1b\]11;([^\x07\x1b]*)(?:\x07|\x1b\\)`)

// parseOSCColor is loop's parseOsc11BackgroundColor, as #rrggbb.
func parseOSCColor(s string) (string, bool) {
	m := osc11Reply.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	v := strings.TrimSpace(m[1])
	channel := func(h string) (int, bool) {
		if h == "" || len(h) > 8 {
			return 0, false
		}
		n, err := strconv.ParseUint(h, 16, 64)
		if err != nil {
			return 0, false
		}
		full := math.Pow(16, float64(len(h))) - 1
		return int(math.Floor(float64(n)/full*255 + 0.5)), true
	}
	var parts []string
	if strings.HasPrefix(v, "#") {
		h := v[1:]
		switch len(h) {
		case 6:
			parts = []string{h[0:2], h[2:4], h[4:6]}
		case 12:
			parts = []string{h[0:4], h[4:8], h[8:12]}
		default:
			return "", false
		}
	} else {
		low := strings.ToLower(v)
		v = v[len(low)-len(strings.TrimPrefix(strings.TrimPrefix(low, "rgba:"), "rgb:")):]
		parts = strings.Split(v, "/")
		if len(parts) < 3 {
			return "", false
		}
	}
	var c [3]int
	for k := 0; k < 3; k++ {
		x, ok := channel(parts[k])
		if !ok {
			return "", false
		}
		c[k] = x
	}
	return fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2]), true
}
