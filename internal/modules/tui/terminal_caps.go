package tui

import (
	"os"
	"strings"
	"sync"
)

// What the host terminal can actually do.
//
// Every capability here degrades to something readable rather than to
// nothing: a terminal without OSC 8 gets the URL in parentheses, one without
// truecolor gets the 256-colour cube. Guessing wrong costs polish, never
// legibility.

// Caps is the detected capability set.
type Caps struct {
	// Hyperlinks is OSC 8 support — clickable link text.
	Hyperlinks bool
	// TrueColor is 24-bit SGR support.
	TrueColor bool
}

var (
	capsOnce sync.Once
	caps     Caps
)

// TerminalCaps probes the environment once and caches the result.
func TerminalCaps() Caps {
	capsOnce.Do(func() { caps = detectCaps() })
	return caps
}

func detectCaps() Caps {
	if os.Getenv("NO_COLOR") != "" {
		return Caps{}
	}
	term := os.Getenv("TERM")
	program := os.Getenv("TERM_PROGRAM")
	colorterm := strings.ToLower(os.Getenv("COLORTERM"))

	c := Caps{
		TrueColor: colorterm == "truecolor" || colorterm == "24bit" ||
			strings.Contains(term, "256color") || program != "",
	}

	// OSC 8 is widely supported but silently prints its payload as text on
	// terminals that lack it, so this allowlists rather than guesses.
	switch program {
	case "iTerm.app", "WezTerm", "ghostty", "Hyper", "vscode", "Apple_Terminal":
		c.Hyperlinks = program != "Apple_Terminal"
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("GHOSTTY_RESOURCES_DIR") != "" {
		c.Hyperlinks = true
	}
	if strings.Contains(term, "kitty") || strings.Contains(term, "wezterm") {
		c.Hyperlinks = true
	}
	return c
}

// Hyperlink wraps text in an OSC 8 clickable link. The URL is not printed
// inline — the link text carries it.
func Hyperlink(text, url string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}
