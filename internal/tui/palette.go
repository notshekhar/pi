package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
)

// The shared colour system every built-in theme is generated from — loop's
// ui/palette.ts and ui/themes.ts, value for value.
//
// A theme has ~65 slots. Themes here declare a Palette (about twenty
// primitives) and ThemeFromPalette derives every slot from it, so changing
// how a slot relates to the palette moves every theme at once. ThemeJSON
// stays the wire format: custom themes and extension themes are authored
// slot by slot and still load.

// Syntax is the nine scopes code highlighting maps onto.
type Syntax struct {
	Comment, Keyword, Function, Variable string
	String, Number, Type                 string
	Operator, Punctuation                string
}

// ToolSurfaces is how tool calls carry their state: Flat keeps every box the
// recessed surface (state lives in the glyph); otherwise explicit fills.
type ToolSurfaces struct {
	Flat                    bool
	Pending, Success, Error string
}

// Palette is loop's Palette.
type Palette struct {
	Name  string
	Light bool
	// Wash paints Bg across the canvas (OSC 11).
	Wash bool

	Bg, BgRaised, BgSunken string
	Text, Muted, Dim       string
	Line                   string
	ToolSurfaces           ToolSurfaces

	Accent, AccentLift      string
	Success, Error, Warning string
	Heading, InlineCode     string
	// CodeBlock is fenced blocks of no known language; "" = Success.
	CodeBlock    string
	Syntax       Syntax
	ThinkingPeak string
}

// ── colour maths ─────────────────────────────────────────────────────────

// rgb parses #rrggbb (or #rgb). Unparseable reads as black: a wrong shade is
// cosmetic, a panic mid-render is not.
func rgb(hex string) (int, int, int) {
	h := strings.TrimPrefix(hex, "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return 0, 0, 0
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return 0, 0, 0
	}
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff)
}

// jsRound is Math.round: halves go up.
func jsRound(v float64) int { return int(math.Floor(v + 0.5)) }

func hexOf(r, g, b float64) string {
	c := func(v float64) int { return jsRound(math.Min(255, math.Max(0, v))) }
	return fmt.Sprintf("#%02x%02x%02x", c(r), c(g), c(b))
}

// Mix is t of the way from a to b.
func Mix(a, b string, t float64) string {
	ar, ag, ab := rgb(a)
	br, bg, bb := rgb(b)
	lerp := func(x, y int) float64 {
		// float64(...) keeps arm64 from fusing this into an FMA (JS parity).
		return float64(x) + float64(float64(y-x)*t)
	}
	return hexOf(lerp(ar, br), lerp(ag, bg), lerp(ab, bb))
}

func channelLuminance(c int) float64 {
	v := float64(c) / 255
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func relLuminance(hex string) float64 {
	r, g, b := rgb(hex)
	return float64(0.2126*channelLuminance(r)) + float64(0.7152*channelLuminance(g)) + float64(0.0722*channelLuminance(b))
}

// ContrastRatio is the WCAG contrast between two colours.
func ContrastRatio(a, b string) float64 {
	hi, lo := relLuminance(a), relLuminance(b)
	if lo > hi {
		hi, lo = lo, hi
	}
	return (hi + 0.05) / (lo + 0.05)
}

// AtLeastContrast is color, lifted toward `toward` only as far as it takes
// to reach target contrast against bg (one-sided: never dimmed).
func AtLeastContrast(bg, color, toward string, target float64) string {
	if ContrastRatio(color, bg) >= target {
		return color
	}
	if ContrastRatio(toward, bg) <= target {
		return toward
	}
	lo, hi := 0.0, 1.0
	for i := 0; i < 12; i++ {
		mid := (lo + hi) / 2
		if ContrastRatio(Mix(color, toward, mid), bg) < target {
			lo = mid
		} else {
			hi = mid
		}
	}
	return Mix(color, toward, hi)
}

// Luminance is Rec.601 luma, for "does this read as light or dark".
func Luminance(hex string) float64 {
	r, g, b := rgb(hex)
	return (float64(0.299*float64(r)) + float64(0.587*float64(g)) + float64(0.114*float64(b))) / 255
}

// ── derivation ──────────────────────────────────────────────────────────

const (
	tintSelection     = 0.16
	tintCustomMessage = 0.07
	tintErrorSurface  = 0.15
)

// ThemeFromPalette is loop's themeFromPalette: the full slot table.
func ThemeFromPalette(p Palette) ThemeJSON {
	tint := func(c string, amt float64) string { return Mix(p.Bg, c, amt) }
	pending, success, failed := p.BgSunken, p.BgSunken, Mix(p.BgRaised, p.Error, tintErrorSurface)
	if !p.ToolSurfaces.Flat {
		pending, success, failed = p.ToolSurfaces.Pending, p.ToolSurfaces.Success, p.ToolSurfaces.Error
	}
	codeBlock := p.CodeBlock
	if codeBlock == "" {
		codeBlock = p.Success
	}
	bgBase := ""
	if p.Wash {
		bgBase = p.Bg
	}
	s := func(v string) ColorValue { return ColorValue{Hex: v} }
	return ThemeJSON{Name: p.Name, Colors: map[string]ColorValue{
		"accent": s(p.Accent), "border": s(p.Line), "borderAccent": s(p.Accent), "borderMuted": s(p.Line),
		"success": s(p.Success), "error": s(p.Error), "warning": s(p.Warning),
		"muted": s(p.Muted), "dim": s(p.Dim), "text": s(p.Text), "thinkingText": s(p.Muted),

		"selectedBg": s(tint(p.Accent, tintSelection)), "userMessageBg": s(p.BgRaised), "userMessageText": s(p.Text),
		"customMessageBg": s(tint(p.Accent, tintCustomMessage)), "customMessageText": s(p.Text),
		"customMessageLabel": s(p.AccentLift),

		"toolPendingBg": s(pending), "toolSuccessBg": s(success), "toolErrorBg": s(failed),
		"toolTitle": s(p.Text), "toolOutput": s(p.Muted), "toolError": s(p.Error),

		"mdHeading": s(p.Heading), "mdLink": s(p.AccentLift), "mdLinkUrl": s(p.Dim), "mdCode": s(p.InlineCode),
		"mdCodeBlock": s(codeBlock), "mdCodeBlockBorder": s(p.Dim), "mdQuote": s(p.Muted), "mdQuoteBorder": s(p.Dim),
		"mdHr": s(p.Dim), "mdListBullet": s(p.Accent),

		"toolDiffAdded": s(p.Success), "toolDiffRemoved": s(p.Error), "toolDiffContext": s(p.Muted),

		"syntaxComment": s(p.Syntax.Comment), "syntaxKeyword": s(p.Syntax.Keyword), "syntaxFunction": s(p.Syntax.Function),
		"syntaxVariable": s(p.Syntax.Variable), "syntaxString": s(p.Syntax.String), "syntaxNumber": s(p.Syntax.Number),
		"syntaxType": s(p.Syntax.Type), "syntaxOperator": s(p.Syntax.Operator), "syntaxPunctuation": s(p.Syntax.Punctuation),

		"thinkingOff": s(p.Line), "thinkingMinimal": s(p.Dim), "thinkingLow": s(Mix(p.Accent, p.Bg, 0.25)),
		"thinkingMedium": s(p.Accent), "thinkingHigh": s(p.AccentLift), "thinkingXhigh": s(p.ThinkingPeak),

		"bashMode": s(p.Success), "inputBorder": s(p.Line), "inputCommand": s(p.Accent), "hookAccent": s(p.Syntax.Variable),

		"bgBase": s(bgBase), "bgRaised": s(p.BgRaised), "accentUser": s(p.Accent), "accentAssistant": s(p.Text),
		"accentThinking": s(p.Muted), "accentTool": s(p.Dim), "timestamp": s(p.Dim), "selectionBorder": s(p.Accent),
		"turnSummary": s(p.Dim),
		// pi's own: the recessed surface itself (loop reads it as toolPendingBg).
		"bgSunken": s(p.BgSunken),
	}}
}

// ── the shipped palettes (loop themes.ts) ───────────────────────────────

var (
	// GrokNight's ink: TokyoNight's accents on a neutral ramp; markdown in
	// loop's own softer set (NIGHT_MARKDOWN).
	NightPalette = Palette{
		Name: "night", Light: false, Wash: true,
		Bg: "#141414", BgRaised: "#242424", BgSunken: "#1c1c1c", Line: "#3c3c41",
		Text: "#e1e1e1", Muted: "#c8c8c8", Dim: "#6c6c6c", ToolSurfaces: ToolSurfaces{Flat: true},
		Accent: "#7aa2f7", AccentLift: "#a2c0eb", Warning: "#e0af68", Error: "#f7768e", Success: "#9ece6a",
		ThinkingPeak: "#bb9af7", Heading: "#d5bb7b", InlineCode: "#d5afd7", CodeBlock: "#a0cba5",
		Syntax: Syntax{Comment: "#6f6f6f", Keyword: "#e6a9c5", Function: "#beb6e8", Variable: "#e2b293",
			String: "#a0cba5", Number: "#87cbd5", Type: "#d5afd7", Operator: "#8a8a8a", Punctuation: "#8a8a8a"},
	}
	// GrokDay: the same hue family, deepened for a light canvas.
	DayPalette = Palette{
		Name: "day", Light: true, Wash: true,
		Bg: "#eeeeee", BgRaised: "#dedede", BgSunken: "#e4e4e4", Line: "#b9b9be",
		Text: "#262626", Muted: "#444444", Dim: "#767676", ToolSurfaces: ToolSurfaces{Flat: true},
		Accent: "#2f64d2", AccentLift: "#446493", Warning: "#a27612", Error: "#cd3048", Success: "#378e23",
		ThinkingPeak: "#7d4bc6", Heading: "#7c5f00", InlineCode: "#7c527e", CodeBlock: "#3f7047",
		Syntax: Syntax{Comment: "#767676", Keyword: "#8c4a6b", Function: "#645a90", Variable: "#885531",
			String: "#3f7047", Number: "#04707c", Type: "#7c527e", Operator: "#6b6b6b", Punctuation: "#6b6b6b"},
	}
)

// ── system: the transcript without a canvas ─────────────────────────────

// SystemThemeName is the theme that paints no canvas of its own and rebuilds
// its ink for the background the terminal reports.
const SystemThemeName = "system"

const (
	liftRaised = 0.05
	liftSunken = 0.025
	liftLine   = 0.13

	safeCanvasDark  = "#2e2e2e"
	safeCanvasLight = "#ececec"
)

var (
	systemMu     sync.RWMutex
	systemCanvas string
	systemLight  bool
)

// SetSystemCanvas records what the terminal reported ("" canvas = it named
// only its polarity). Returns whether it changed.
func SetSystemCanvas(canvas string, light bool) bool {
	systemMu.Lock()
	defer systemMu.Unlock()
	if canvas == systemCanvas && light == systemLight {
		return false
	}
	systemCanvas, systemLight = canvas, light
	return true
}

// SystemCanvas is the background system solves for, and its polarity.
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

// SystemPalette is loop's systemPalette: every colour held at the contrast
// it has on its own palette's canvas, on the canvas really there.
func SystemPalette() Palette {
	ground, light := SystemCanvas()
	base, pole := NightPalette, "#ffffff"
	if light {
		base, pole = DayPalette, "#000000"
	}
	held := func(c string) string { return AtLeastContrast(ground, c, pole, ContrastRatio(c, base.Bg)) }
	codeBlock := base.CodeBlock
	if codeBlock == "" {
		codeBlock = base.Success
	}
	p := base
	p.Name, p.Wash, p.Bg = SystemThemeName, false, ground
	p.BgRaised, p.BgSunken, p.Line = Mix(ground, pole, liftRaised), Mix(ground, pole, liftSunken), Mix(ground, pole, liftLine)
	p.Text, p.Muted, p.Dim = held(base.Text), held(base.Muted), held(base.Dim)
	p.Accent, p.AccentLift = held(base.Accent), held(base.AccentLift)
	p.Success, p.Error, p.Warning = held(base.Success), held(base.Error), held(base.Warning)
	p.Heading, p.InlineCode, p.CodeBlock = held(base.Heading), held(base.InlineCode), held(codeBlock)
	p.ThinkingPeak = held(base.ThinkingPeak)
	p.Syntax = Syntax{Comment: held(base.Syntax.Comment), Keyword: held(base.Syntax.Keyword),
		Function: held(base.Syntax.Function), Variable: held(base.Syntax.Variable), String: held(base.Syntax.String),
		Number: held(base.Syntax.Number), Type: held(base.Syntax.Type), Operator: held(base.Syntax.Operator),
		Punctuation: held(base.Syntax.Punctuation)}
	return p
}
