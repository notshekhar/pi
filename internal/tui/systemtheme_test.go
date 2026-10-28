package tui

import (
	"math"
	"testing"
)

// Save and restore the probe's global answer: these tests set it directly.
func withCanvas(t *testing.T, canvas string, light bool) {
	t.Helper()
	systemMu.RLock()
	oldCanvas, oldLight := systemCanvas, systemLight
	systemMu.RUnlock()
	t.Cleanup(func() { SetSystemCanvas(oldCanvas, oldLight) })
	SetSystemCanvas(canvas, light)
}

func TestContrastRatioEndpoints(t *testing.T) {
	if got := ContrastRatio("#ffffff", "#000000"); math.Abs(got-21) > 0.01 {
		t.Errorf("white on black = %.3f, want 21", got)
	}
	if got := ContrastRatio("#777777", "#777777"); math.Abs(got-1) > 0.001 {
		t.Errorf("a colour against itself = %.3f, want 1", got)
	}
	// Order must not matter — the ratio is symmetric.
	if a, b := ContrastRatio("#141414", "#f5f5f5"), ContrastRatio("#f5f5f5", "#141414"); math.Abs(a-b) > 1e-9 {
		t.Errorf("not symmetric: %.6f vs %.6f", a, b)
	}
}

// The one-sided rule: a slot that already clears its target is returned
// untouched. Pulling it back down would be dimming a screen already right.
func TestAtLeastContrastLeavesWhatAlreadyClears(t *testing.T) {
	got := AtLeastContrast("#000000", NightPalette.Dim, "#ffffff", ContrastRatio(NightPalette.Dim, "#141414"))
	if got != NightPalette.Dim {
		t.Errorf("dim on pure black was lifted to %s, want it left at %s", got, NightPalette.Dim)
	}
}

func TestAtLeastContrastLiftsWhatFellBelow(t *testing.T) {
	target := ContrastRatio(NightPalette.Dim, NightPalette.Bg)
	ground := "#3a3a3a" // lighter than noir's canvas: dim sinks into it
	if ContrastRatio(NightPalette.Dim, ground) >= target {
		t.Fatal("test premise wrong: dim already clears its target on this canvas")
	}
	got := AtLeastContrast(ground, NightPalette.Dim, "#ffffff", target)
	if ContrastRatio(got, ground) < target-0.01 {
		t.Errorf("lifted to %s = %.3f:1, want at least %.3f:1", got, ContrastRatio(got, ground), target)
	}
}

// The whole point of the theme: noir's RATIOS survive being moved onto a
// canvas noir never chose. Every slot must read at least as well as it does on
// noir's own background.
func TestSystemPaletteHoldsNoirsRatiosOnALighterCanvas(t *testing.T) {
	ground := "#2e2e2e"
	withCanvas(t, ground, false)
	p := SystemPalette()

	slots := map[string][2]string{
		"text":       {p.Text, NightPalette.Text},
		"muted":      {p.Muted, NightPalette.Muted},
		"dim":        {p.Dim, NightPalette.Dim},
		"accent":     {p.Accent, NightPalette.Accent},
		"success":    {p.Success, NightPalette.Success},
		"error":      {p.Error, NightPalette.Error},
		"warning":    {p.Warning, NightPalette.Warning},
		"heading":    {p.Heading, NightPalette.Heading},
		"inlineCode": {p.InlineCode, NightPalette.InlineCode},
		"comment":    {p.Syntax.Comment, NightPalette.Syntax.Comment},
		"keyword":    {p.Syntax.Keyword, NightPalette.Syntax.Keyword},
		"string":     {p.Syntax.String, NightPalette.Syntax.String},
	}
	// The target is not always reachable, and that is not a failure. On
	// #2e2e2e the most contrast any colour can have is pure white at 13.58:1,
	// while noir's text holds 16.9:1 against its own near-black. When the
	// canvas cannot deliver the ratio, saturating to the pole is the best
	// available answer — so the rule is "hits its target, or is the pole".
	const pole = "#ffffff"
	ceiling := ContrastRatio(pole, ground)
	for name, pair := range slots {
		got, night := pair[0], pair[1]
		want := ContrastRatio(night, NightPalette.Bg)
		have := ContrastRatio(got, ground)
		if have >= want-0.01 {
			continue
		}
		if want > ceiling && got == pole {
			continue // unreachable on this canvas; pinned to the brightest there is
		}
		t.Errorf("%s: %.3f:1 on %s, but holds %.3f:1 on noir's own canvas (ceiling %.3f:1, got %s)",
			name, have, ground, want, ceiling, got)
	}
}

// A terminal DARKER than noir's canvas is already more readable, so nothing is
// touched. This is what makes the theme safe to run on every background.
func TestSystemPaletteLeavesADarkerCanvasAlone(t *testing.T) {
	withCanvas(t, "#000000", false)
	p := SystemPalette()
	for _, pair := range [][2]string{
		{p.Text, NightPalette.Text},
		{p.Muted, NightPalette.Muted},
		{p.Dim, NightPalette.Dim},
		{p.Accent, NightPalette.Accent},
		{p.Syntax.Keyword, NightPalette.Syntax.Keyword},
	} {
		if pair[0] != pair[1] {
			t.Errorf("on pure black, %s was changed to %s — nothing should be", pair[1], pair[0])
		}
	}
}

// It paints no canvas — that is the feature — but it still carries a concrete
// Bg, because every surface tint is mixed against it.
func TestSystemPaletteNeverWashes(t *testing.T) {
	withCanvas(t, "#0d1117", false)
	p := SystemPalette()
	if p.Wash {
		t.Error("system washes the canvas; the whole point is that it does not")
	}
	if p.Bg != "#0d1117" {
		t.Errorf("Bg = %s, want the terminal's own #0d1117 to mix surfaces against", p.Bg)
	}
	if p.Name != SystemThemeName {
		t.Errorf("Name = %q, want %q", p.Name, SystemThemeName)
	}
}

// Until the terminal answers, the ramp is solved for the LIGHTEST background a
// dark terminal plausibly has — the worst case, not the average, because
// solving for one too dark is the unreadable screen this exists to prevent.
func TestSystemCanvasFallsBackToTheSafeCanvas(t *testing.T) {
	withCanvas(t, "", false)
	if got, light := SystemCanvas(); got != safeCanvasDark || light {
		t.Errorf("unanswered dark = (%s, %v), want (%s, false)", got, light, safeCanvasDark)
	}
	SetSystemCanvas("", true)
	if got, light := SystemCanvas(); got != safeCanvasLight || !light {
		t.Errorf("unanswered light = (%s, %v), want (%s, true)", got, light, safeCanvasLight)
	}
}

func TestSystemPaletteIsSelectable(t *testing.T) {
	th, ok := LoadTheme(SystemThemeName)
	if !ok {
		t.Fatal("LoadTheme cannot find system — /theme system and the settings picker both go through it")
	}
	if th.Hex(SlotBgBase) != "" {
		t.Error("the theme found by name washes")
	}
}

// The reply's channels come in 4, 8, 12 or 16 bits; normalising by the width
// actually sent is what keeps #141414 from reading as near-black-but-not.
func TestParseOSCColorWidths(t *testing.T) {
	for _, tc := range []struct{ reply, want string }{
		{"\x1b]11;rgb:1414/1414/1414\x07", "#141414"},
		{"\x1b]11;rgb:14/14/14\x07", "#141414"},
		{"\x1b]11;rgb:ffff/ffff/ffff\x1b\\", "#ffffff"},
		{"\x1b]11;rgb:0000/0000/0000\x07", "#000000"},
		{"\x1b]11;rgb:0d0d/1111/1717\x07", "#0d1117"},
	} {
		got, ok := parseOSCColor(tc.reply)
		if !ok || got != tc.want {
			t.Errorf("parseOSCColor(%q) = (%q, %v), want (%q, true)", tc.reply, got, ok, tc.want)
		}
	}
	for _, bad := range []string{"", "garbage", "\x1b]11;rgb:1414/1414\x07", "\x1b]11;rgb://\x07"} {
		if _, ok := parseOSCColor(bad); ok {
			t.Errorf("parseOSCColor(%q) reported success on nonsense", bad)
		}
	}
}

// The reply forms loop's parseOsc11BackgroundColor accepts, with the DA1
// answer that ends the probe trailing behind them.
func TestParseOSCColorLoopForms(t *testing.T) {
	for reply, want := range map[string]string{
		"\x1b]11;rgb:1414/1414/1414\x07\x1b[?62;22c": "#141414",
		"\x1b]11;rgba:ffff/0000/8080/ffff\x1b\\":     "#ff0080",
		"\x1b]11;#1e1e2e\x07":                        "#1e1e2e",
		"\x1b]11;#ffff00000000\x07":                  "#ff0000",
	} {
		if got, ok := parseOSCColor(reply); !ok || got != want {
			t.Errorf("%q → %q %v, want %q", reply, got, ok, want)
		}
	}
	if _, ok := parseOSCColor("\x1b[?62;22c"); ok {
		t.Error("a DA1 answer alone is not a colour")
	}
}
