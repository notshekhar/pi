package tui

import "sync"

// noir's third theme: noir's ink with NO canvas of its own.
//
// night and day wash the terminal's background (OSC 11/10) and paint noir's
// own dark grey behind every row. That is the point of them, and it is wrong
// when your terminal already has a background it means: a true black, a
// transparency, an image. `system` washes nothing and hands the background
// back, then rebuilds its palette to suit whatever is really there.
//
// It keeps a concrete Bg even though nothing paints it: every surface tint
// (selection, a raised panel, a recessed tool body) is MIXED against it, so
// dropping it would drop those surfaces too. It stops being painted; it stays
// the colour the rest is computed from.

// SystemThemeName is the name the theme is selected by.
const SystemThemeName = "system"

// How far the two surface levels and the chrome line sit off the canvas.
// Measured from night AND day, which agree on all three — the lift is a
// relationship in the design, not a per-palette constant, which is why it
// survives being moved onto a background neither palette chose.
const (
	liftRaised = 0.05
	liftSunken = 0.025
	liftLine   = 0.13
)

// The canvas to assume before the terminal has told us its own — the LIGHTEST
// background a dark terminal plausibly has, and the darkest a light one does.
//
// Not a guess at the average: a guess at the worst case, because the ramp's
// failure is one-sided. Solve for a canvas lighter than the real one and the
// greys come out a little brighter than designed, which is harmless. Solve for
// a darker one and they come out too faint to read, which is precisely the bug
// this exists to prevent.
const (
	safeCanvasDark  = "#2e2e2e"
	safeCanvasLight = "#ececec"
)

// What the terminal told us at startup: the canvas to hold the ramp's contrast
// against, and which polarity to wear. Guarded because the probe runs during
// boot and the palette is rebuilt from the render path.
var (
	systemMu     sync.RWMutex
	systemCanvas string // "" until the terminal names its background
	systemLight  bool
)

// SetSystemCanvas records what the terminal reported. Called once, from the
// startup probe window — see DetectBackgroundColor for why it is only once.
func SetSystemCanvas(canvas string, light bool) {
	systemMu.Lock()
	defer systemMu.Unlock()
	systemCanvas, systemLight = canvas, light
}

// SystemCanvas is the background `system` is currently solving for, and
// whether it reads as light. Falls back to the safe canvas for its polarity.
func SystemCanvas() (string, bool) {
	systemMu.RLock()
	defer systemMu.RUnlock()
	if systemCanvas != "" {
		return systemCanvas, systemLight
	}
	if systemLight {
		return safeCanvasLight, true
	}
	return safeCanvasDark, false
}

// SystemPalette builds `system` for the canvas it is really drawn on.
//
// The ink cannot simply be night's. Noir's colours are not those hexes because
// the hexes are special — each is a RATIO against noir's own #141414: text at
// 16.9:1, muted at 5.3:1, dim deliberately faint at 2.9:1, the accent at
// 6.9:1. Drop the same hexes onto a lighter terminal and every one of them
// loses roughly a fifth of its contrast at once. That is not "text is a bit
// dark"; it is the whole palette sinking toward a background it was never
// measured against.
//
// So EVERY colour is checked against the canvas that is actually there — the
// greys, the semantic hues, the syntax set — and any that fell below the ratio
// it holds on noir's own canvas is lifted back to it.
func SystemPalette() Palette {
	ground, light := SystemCanvas()
	base := NightPalette
	// Tinting past the ink toward pure white/black: on a background lighter
	// than the palette's own, the ratio a slot wants can sit beyond its ink.
	pole := "#ffffff"
	if light {
		base, pole = DayPalette, "#000000"
	}
	// held is `slot`, still as legible here as it is on its own canvas.
	held := func(slot string) string {
		return AtLeastContrast(ground, slot, pole, ContrastRatio(slot, base.Bg))
	}
	p := base
	p.Name = SystemThemeName
	p.Light = light
	p.Wash = false
	p.Bg = ground
	p.BgRaised = Mix(ground, pole, liftRaised)
	p.BgSunken = Mix(ground, pole, liftSunken)
	p.Line = Mix(ground, pole, liftLine)
	p.Text = held(base.Text)
	p.Muted = held(base.Muted)
	p.Dim = held(base.Dim)
	p.Accent = held(base.Accent)
	p.AccentLift = held(base.AccentLift)
	p.Success = held(base.Success)
	p.Error = held(base.Error)
	p.Warning = held(base.Warning)
	p.Heading = held(base.Heading)
	p.InlineCode = held(base.InlineCode)
	p.CodeBlock = held(base.CodeBlock)
	p.ThinkingPeak = held(base.ThinkingPeak)
	p.Syntax = Syntax{
		Comment:     held(base.Syntax.Comment),
		Keyword:     held(base.Syntax.Keyword),
		Function:    held(base.Syntax.Function),
		Variable:    held(base.Syntax.Variable),
		String:      held(base.Syntax.String),
		Number:      held(base.Syntax.Number),
		Type:        held(base.Syntax.Type),
		Operator:    held(base.Syntax.Operator),
		Punctuation: held(base.Syntax.Punctuation),
	}
	return p
}
