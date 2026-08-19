package tui

import (
	"os"
	"os/exec"
	"strings"
	"time"
)

// The startup masthead, matching loop's: a vertical gradient rule down the
// left edge with the product name and greeting beside it, then an aligned
// label/value block, then the hint lines.
//
// Static by design — it renders once and never animates, so it costs nothing
// once it has scrolled up into the terminal's scrollback.

// bar is the rule glyph. A full block, so a column of them joins into an
// unbroken bar.
const bar = "█"

// barGap is the space between the rule and the text column.
const barGap = "  "

// labelGap separates the label column from the value column.
const labelGap = "  "

// Where the rule's endpoints sit relative to the accent: how far the top is
// lightened and the bottom darkened. A light terminal needs a shallower ramp
// shifted darker, or the top of the rule disappears against the canvas.
var barRamp = map[bool]struct{ top, bottom float64 }{
	false: {0.55, 0.6}, // dark
	true:  {0.26, 0.5}, // light
}

// WelcomeInfo is what the masthead reports.
type WelcomeInfo struct {
	Name    string
	Version string
	Model   string
	Branch  string
	CWD     string
	Session string
	Agent   string
}

// Welcome renders the masthead.
func Welcome(t *Theme, info WelcomeInfo, width int) []string {
	type row struct{ label, value string }
	rows := []row{}
	if info.Version != "" {
		rows = append(rows, row{"version", t.Fg(SlotText, info.Version)})
	}
	if info.Model != "" {
		rows = append(rows, row{"model", t.Fg(SlotText, info.Model)})
	} else {
		rows = append(rows, row{"model", t.Fg(SlotWarning, "no model — run /login or /provider")})
	}
	if info.Branch != "" {
		rows = append(rows, row{"branch", t.Fg(SlotText, info.Branch)})
	}
	rows = append(rows, row{"cwd", t.Fg(SlotText, info.CWD)})
	session := t.Fg(SlotDim, "unsaved")
	if info.Session != "" && info.Session != "unsaved" {
		session = t.Fg(SlotText, info.Session)
	}
	rows = append(rows, row{"session", session})
	if info.Agent != "" {
		rows = append(rows, row{"agent", t.Fg(SlotText, info.Agent)})
	}

	labelWidth := 0
	for _, r := range rows {
		labelWidth = max(labelWidth, len(r.label))
	}

	// Everything that sits beside the rule, in order.
	beside := []string{
		t.Fg(SlotText, t.Bold(ProductName)),
		t.Fg(SlotMuted, "Welcome back, "+info.Name),
		"",
	}
	for _, r := range rows {
		beside = append(beside, t.Fg(SlotDim, padRight(r.label, labelWidth))+labelGap+r.value)
	}
	// The rule must end on the last row with something beside it, or a bar
	// segment hangs under the block.
	for len(beside) > 0 && beside[len(beside)-1] == "" {
		beside = beside[:len(beside)-1]
	}

	colors := barColors(t, len(beside))
	lines := []string{""}
	for i, text := range beside {
		lines = append(lines, " "+t.FgHex(colors[i], bar)+barGap+text)
	}
	lines = append(lines,
		"",
		" "+t.Fg(SlotDim, "type ")+t.Fg(SlotText, t.Bold("/help"))+t.Fg(SlotDim, " for slash commands"),
		" "+t.Fg(SlotDim, "ctrl+e transcript · shift+tab agents · ctrl+c twice to quit"),
		"")

	// Truncate then pad: a deep cwd can exceed the width, and an overflowing
	// line wraps and pushes the whole grid down.
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = padToWidth(fitRow(l, width), width)
	}
	return out
}

// ProductName is what the masthead announces.
var ProductName = "pi-agent"

// barColors ramps the accent from a pale top to a deep bottom.
func barColors(t *Theme, rows int) []string {
	accent := t.Hex(SlotAccent)
	ramp := barRamp[t.Palette.Light]
	top := Mix(accent, "#ffffff", ramp.top)
	bottom := Mix(accent, "#000000", ramp.bottom)

	out := make([]string, rows)
	for i := range out {
		at := 0.0
		if rows > 1 {
			at = float64(i) / float64(rows-1)
		}
		out[i] = Mix(top, bottom, at)
	}
	return out
}

// GitBranch is the current branch, or "" outside a repo or on a detached
// HEAD.
//
// Synchronous with a short timeout on purpose: it runs once during startup,
// and a missing or slow git must never delay the first paint.
func GitBranch(cwd string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = cwd
	done := make(chan string, 1)
	go func() {
		out, err := cmd.Output()
		if err != nil {
			done <- ""
			return
		}
		done <- strings.TrimSpace(string(out))
	}()
	select {
	case branch := <-done:
		if branch == "HEAD" {
			return ""
		}
		return branch
	case <-time.After(500 * time.Millisecond):
		return ""
	}
}

// ShortenPath replaces the home prefix with `~`.
func ShortenPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(p, home) {
		return p
	}
	return "~" + p[len(home):]
}

// UserName is the greeting name.
func UserName() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("USERNAME"); u != "" {
		return u
	}
	return "there"
}
