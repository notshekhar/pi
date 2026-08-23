package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The theme layer, ported from loop's palette.ts / theme.ts / noir-mode.ts.
//
// Nothing in this package hardcodes a colour. A renderer names a SLOT and the
// theme resolves it, so a palette swap moves every mark on screen at once and
// the day/night pair stay in step by construction.

// Slot is a named colour role. Renderers ask for these, never for hex.
type Slot string

const (
	SlotText      Slot = "text"
	SlotMuted     Slot = "muted"
	SlotDim       Slot = "dim"
	SlotAccent    Slot = "accent"
	SlotBorder    Slot = "border"
	SlotSuccess   Slot = "success"
	SlotError     Slot = "error"
	SlotWarning   Slot = "warning"
	SlotSelected  Slot = "selectedBg"
	SlotSelection Slot = "selectionBorder"
	SlotBgBase    Slot = "bgBase"
	SlotBgRaised  Slot = "bgRaised"
	SlotBgSunken  Slot = "bgSunken"

	SlotThinkingText Slot = "thinkingText"
	SlotThinkingPeak Slot = "thinkingPeak"

	SlotToolTitle       Slot = "toolTitle"
	SlotToolOutput      Slot = "toolOutput"
	SlotToolError       Slot = "toolError"
	SlotToolDiffAdded   Slot = "toolDiffAdded"
	SlotToolDiffRemoved Slot = "toolDiffRemoved"
	SlotToolDiffContext Slot = "toolDiffContext"

	SlotMdHeading         Slot = "mdHeading"
	SlotMdLink            Slot = "mdLink"
	SlotMdLinkURL         Slot = "mdLinkUrl"
	SlotMdCode            Slot = "mdCode"
	SlotMdCodeBlock       Slot = "mdCodeBlock"
	SlotMdCodeBlockBorder Slot = "mdCodeBlockBorder"
	SlotMdQuote           Slot = "mdQuote"
	SlotMdQuoteBorder     Slot = "mdQuoteBorder"
	SlotMdHr              Slot = "mdHr"
	SlotMdListBullet      Slot = "mdListBullet"

	SlotSyntaxComment     Slot = "syntaxComment"
	SlotSyntaxKeyword     Slot = "syntaxKeyword"
	SlotSyntaxFunction    Slot = "syntaxFunction"
	SlotSyntaxVariable    Slot = "syntaxVariable"
	SlotSyntaxString      Slot = "syntaxString"
	SlotSyntaxNumber      Slot = "syntaxNumber"
	SlotSyntaxType        Slot = "syntaxType"
	SlotSyntaxOperator    Slot = "syntaxOperator"
	SlotSyntaxPunctuation Slot = "syntaxPunctuation"
)

// Syntax is a code palette: one colour per token class.
type Syntax struct {
	Comment, Keyword, Function, Variable string
	String, Number, Type                 string
	Operator, Punctuation                string
}

// Palette is the small set of primitives every slot is derived from.
// Writing a new theme means filling this in, not enumerating forty slots.
type Palette struct {
	Name  string
	Light bool
	// Wash paints Bg across the canvas (OSC 11). A mode that does not wash
	// still needs a concrete Bg: every tint below is computed against it.
	Wash bool

	Bg       string // the canvas
	BgRaised string // lifted off it: user messages, panels
	BgSunken string // recessed: tool output
	Line     string // borders, rules, composer edges

	Text  string
	Muted string
	Dim   string

	Accent     string
	AccentLift string
	Success    string
	Error      string
	Warning    string

	// Markdown's own two colours. Everything else in a message reuses a
	// colour the palette already has; a heading and an inline `code` span
	// need hues of their own or a message renders in one flat blue.
	Heading    string
	InlineCode string
	CodeBlock  string

	ThinkingPeak string
	Syntax       Syntax
}

// Noir's ink: every colour sits at one lightness and a shared chroma, with
// hues spread far enough apart to stay apart. Heading and error carry extra
// chroma because both have a job beyond looking pleasant — a heading leads its
// section, an error has to alert.
var NightPalette = Palette{
	Name: "night", Light: false, Wash: true,
	Bg: "#141414", BgRaised: "#1f1f21", BgSunken: "#1a1a1c", Line: "#33333a",
	Text: "#f5f5f5", Muted: "#8a8a8a", Dim: "#5f5f5f",
	Accent: "#77a0dc", AccentLift: "#a2c0eb",
	Success: "#a0cba5", Error: "#f5a5a7", Warning: "#dcb77f",
	Heading: "#d5bb7b", InlineCode: "#d5afd7", CodeBlock: "#a0cba5",
	ThinkingPeak: "#beb6e8",
	Syntax: Syntax{
		Comment: "#6f6f6f", Keyword: "#e6a9c5", Function: "#beb6e8",
		Variable: "#e2b293", String: "#a0cba5", Number: "#87cbd5",
		Type: "#d5afd7", Operator: "#8a8a8a", Punctuation: "#8a8a8a",
	},
}

var DayPalette = Palette{
	Name: "day", Light: true, Wash: true,
	Bg: "#fcfcfc", BgRaised: "#f1f1f3", BgSunken: "#f7f7f8", Line: "#d6d6dd",
	Text: "#27272a", Muted: "#71717b", Dim: "#8b8b95",
	Accent: "#3463a6", AccentLift: "#446493",
	Success: "#3f7047", Error: "#9a444a", Warning: "#835b06",
	Heading: "#7c5f00", InlineCode: "#7c527e", CodeBlock: "#3f7047",
	ThinkingPeak: "#645a90",
	Syntax: Syntax{
		Comment: "#767676", Keyword: "#8c4a6b", Function: "#645a90",
		Variable: "#885531", String: "#3f7047", Number: "#04707c",
		Type: "#7c527e", Operator: "#6b6b6b", Punctuation: "#6b6b6b",
	},
}

// Theme resolves slots to colours and carries the text-decoration helpers.
type Theme struct {
	Palette Palette
	slots   map[Slot]string
	// TrueColor emits 24-bit SGR. Off falls back to the 256-colour cube,
	// which every terminal since the nineties understands.
	TrueColor bool
}

// tint lays amount of color over the canvas — how every surface is built.
func (p Palette) tint(color string, amount float64) string {
	return Mix(p.Bg, color, amount)
}

// NewTheme derives the full slot table from a palette.
func NewTheme(p Palette) *Theme {
	t := &Theme{Palette: p, TrueColor: true}
	t.slots = map[Slot]string{
		SlotText:      p.Text,
		SlotMuted:     p.Muted,
		SlotDim:       p.Dim,
		SlotAccent:    p.Accent,
		SlotBorder:    p.Line,
		SlotSuccess:   p.Success,
		SlotError:     p.Error,
		SlotWarning:   p.Warning,
		SlotSelected:  p.tint(p.Accent, 0.16),
		SlotSelection: p.Accent,
		SlotBgBase:    p.Bg,
		SlotBgRaised:  p.BgRaised,
		SlotBgSunken:  p.BgSunken,

		SlotThinkingText: p.Muted,
		SlotThinkingPeak: p.ThinkingPeak,

		SlotToolTitle:       p.Text,
		SlotToolOutput:      p.Muted,
		SlotToolError:       p.Error,
		SlotToolDiffAdded:   p.Success,
		SlotToolDiffRemoved: p.Error,
		SlotToolDiffContext: p.Muted,

		// Headings carry colour, not just weight: a terminal transcript has
		// no type scale to separate sections with, so an uncoloured heading
		// reads as just another bold line.
		SlotMdHeading:         p.Heading,
		SlotMdLink:            p.AccentLift,
		SlotMdLinkURL:         p.Dim,
		SlotMdCode:            p.InlineCode,
		SlotMdCodeBlock:       p.CodeBlock,
		SlotMdCodeBlockBorder: p.Dim,
		SlotMdQuote:           p.Muted,
		SlotMdQuoteBorder:     p.Dim,
		SlotMdHr:              p.Dim,
		SlotMdListBullet:      p.Accent,

		SlotSyntaxComment:     p.Syntax.Comment,
		SlotSyntaxKeyword:     p.Syntax.Keyword,
		SlotSyntaxFunction:    p.Syntax.Function,
		SlotSyntaxVariable:    p.Syntax.Variable,
		SlotSyntaxString:      p.Syntax.String,
		SlotSyntaxNumber:      p.Syntax.Number,
		SlotSyntaxType:        p.Syntax.Type,
		SlotSyntaxOperator:    p.Syntax.Operator,
		SlotSyntaxPunctuation: p.Syntax.Punctuation,
	}
	return t
}

// Hex is the raw colour behind a slot, for the uses that must blend rather
// than emit (the rail's wave, the bullet's pulse).
func (t *Theme) Hex(s Slot) string { return t.slots[s] }

// Fg paints text in a slot's colour.
func (t *Theme) Fg(s Slot, text string) string {
	c, ok := t.slots[s]
	if !ok || text == "" {
		return text
	}
	return t.FgHex(c, text)
}

// Bg fills text's cells with a slot's colour.
func (t *Theme) Bg(s Slot, text string) string {
	c, ok := t.slots[s]
	if !ok {
		return text
	}
	return t.BgHex(c, text)
}

// FgHex paints text in an explicit colour.
func (t *Theme) FgHex(hex, text string) string {
	return t.sgr(38, hex) + text + "\x1b[39m"
}

// BgHex fills text's cells with an explicit colour.
func (t *Theme) BgHex(hex, text string) string {
	return t.sgr(48, hex) + text + "\x1b[49m"
}

// sgr builds the colour-setting escape for a layer (38 fg / 48 bg).
func (t *Theme) sgr(layer int, hex string) string {
	r, g, b := rgb(hex)
	if t.TrueColor {
		return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", layer, r, g, b)
	}
	return fmt.Sprintf("\x1b[%d;5;%dm", layer, cube256(r, g, b))
}

// Text decorations. Each closes only its own attribute, so nesting a bold
// inside an italic does not drop the italic on the way out.
// seriesSlots are the swatches a categorical chart hands out, in order.
//
// Named slots rather than a fixed palette so a chart restyles with the theme.
// The order matters: consecutive entries have to stay distinguishable side by
// side, which is why the two markdown colours lead and the syntax ones are
// interleaved rather than grouped.
var seriesSlots = []Slot{
	SlotMdHeading,
	SlotMdLink,
	SlotSyntaxType,
	SlotSyntaxNumber,
	SlotSuccess,
	SlotError,
	SlotSyntaxVariable,
}

// Series colours the i-th category of a chart, wrapping when it runs out.
func (t *Theme) Series(i int, text string) string {
	return t.Fg(seriesSlots[((i%len(seriesSlots))+len(seriesSlots))%len(seriesSlots)], text)
}

// SeriesLength is how many distinct swatches Series hands out before it
// repeats.
func SeriesLength() int { return len(seriesSlots) }

// heatSteps are how far along the success colour each activity level sits,
// measured out from the canvas.
var heatSteps = []float64{0.3, 0.55, 0.78, 1}

// Heat is a step on the activity ramp, 0 (nothing) through len(heatSteps).
//
// Built by walking the success colour out of the canvas rather than pinning
// GitHub's greens, so the wall reads as one colour deepening on whatever
// background it lands on.
func (t *Theme) Heat(level int, text string) string {
	if level <= 0 {
		return t.Fg(SlotDim, text)
	}
	floor := "#000000"
	if t.Palette.Light {
		floor = "#ffffff"
	}
	step := heatSteps[min(level, len(heatSteps))-1]
	return t.FgHex(Mix(floor, t.Hex(SlotSuccess), step), text)
}

func (t *Theme) Bold(s string) string      { return "\x1b[1m" + s + "\x1b[22m" }
func (t *Theme) Dim(s string) string       { return "\x1b[2m" + s + "\x1b[22m" }
func (t *Theme) Italic(s string) string    { return "\x1b[3m" + s + "\x1b[23m" }
func (t *Theme) Underline(s string) string { return "\x1b[4m" + s + "\x1b[24m" }
func (t *Theme) Strike(s string) string    { return "\x1b[9m" + s + "\x1b[29m" }

// StylePrefix is the opening half of a style function, recovered by running
// it over a sentinel.
//
// Inline markdown needs it: a codespan closes with its own reset, and without
// re-opening the surrounding style the rest of a heading would fall back to
// body text mid-line.
func StylePrefix(style func(string) string) string {
	const sentinel = "\x00"
	styled := style(sentinel)
	if i := strings.Index(styled, sentinel); i >= 0 {
		return styled[:i]
	}
	return ""
}

// Mix blends t of the way from a to b.
func Mix(a, b string, ratio float64) string {
	ar, ag, ab := rgb(a)
	br, bg, bb := rgb(b)
	blend := func(x, y int) int { return int(float64(x) + (float64(y)-float64(x))*ratio + 0.5) }
	return fmt.Sprintf("#%02x%02x%02x", blend(ar, br), blend(ag, bg), blend(ab, bb))
}

// Luminance is the relative brightness of a colour, for deciding whether it
// reads as light or dark.
func Luminance(hex string) float64 {
	r, g, b := rgb(hex)
	return (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 255
}

// rgb parses #rgb or #rrggbb. An unparseable colour reads as black rather
// than erroring: a wrong shade is a cosmetic bug, a panic mid-render is not.
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

// cube256 maps a colour onto the xterm 256 palette: the 6×6×6 cube, or the
// grey ramp when the channels are close enough to be grey.
func cube256(r, g, b int) int {
	if abs(r-g) < 8 && abs(g-b) < 8 {
		if r < 8 {
			return 16
		}
		if r > 248 {
			return 231
		}
		return 232 + (r-8)*24/247
	}
	q := func(v int) int { return (v * 5) / 255 }
	return 16 + 36*q(r) + 6*q(g) + q(b)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// SetCanvas washes the terminal background (OSC 11) and sets the foreground
// (OSC 10), so the transcript sits on the theme's own canvas rather than
// whatever the terminal happened to have. The returned func restores both.
func SetCanvas(p Palette) func() {
	if !p.Wash {
		// Hand the canvas back rather than doing nothing. Switching from a
		// washing theme to this one has to UNDO the previous wash, or the
		// terminal keeps night's grey behind a theme whose entire point is
		// that your own background shows through. Doing it here rather than at
		// the call sites means no theme switch can forget it.
		ReleaseCanvas()
		return func() {}
	}
	fmt.Print("\x1b]11;" + p.Bg + "\x07\x1b]10;" + p.Text + "\x07")
	return ReleaseCanvas
}

// ReleaseCanvas hands background and foreground back to the terminal
// (OSC 111/110). Safe to call when nothing was ever washed.
func ReleaseCanvas() {
	fmt.Print("\x1b]111\x07\x1b]110\x07")
}

// WCAG relative luminance, and the contrast ratio between two colours.
//
// Deliberately distinct from Luminance above, which is the cheap Rec.601 luma
// used to decide "does this read as light or dark". Contrast needs the real
// thing: a ramp is only legible relative to what it sits ON, and that is what
// lets a palette be rebuilt for a canvas it did not choose (see SystemPalette).
func channelLuminance(c int) float64 {
	v := float64(c) / 255
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func relLuminance(hex string) float64 {
	r, g, b := rgb(hex)
	return 0.2126*channelLuminance(r) + 0.7152*channelLuminance(g) + 0.0722*channelLuminance(b)
}

// ContrastRatio is the WCAG contrast between two colours, 1:1 to 21:1.
func ContrastRatio(a, b string) float64 {
	hi, lo := relLuminance(a), relLuminance(b)
	if lo > hi {
		hi, lo = lo, hi
	}
	return (hi + 0.05) / (lo + 0.05)
}

// AtLeastContrast returns color, guaranteed to reach target contrast against
// bg — lifted toward `toward` only as far as it takes, and left alone when it
// already clears it.
//
// This is how a palette keeps its RELATIONSHIPS on a canvas it does not own.
// Noir's dim grey is not #5f5f5f because that hex is special: it is 2.9:1
// against noir's own background, faint on purpose but still readable. Move the
// same hex onto a lighter terminal and the intent is gone (2.4:1). Asking for
// the ratio reproduces the design on the canvas that is actually there.
//
// One-sided on purpose. A slot that already clears its target here is not
// touched: a terminal darker than the palette's own canvas makes everything
// MORE readable, and trading that back for exactness would be dimming a screen
// that was already right. Only what fell below is raised.
//
// Mixing the colour toward white (or black) tints rather than rotates, so a
// hue stays the hue it was — a lifted red is still red, just no longer sunk
// into the background. Mixing up from bg instead would return a grey and throw
// the hue away.
func AtLeastContrast(bg, color, toward string, target float64) string {
	if ContrastRatio(color, bg) >= target {
		return color
	}
	// Even the pole cannot reach it (a mid-grey canvas): the pole is the best
	// available, and returning it beats returning something dimmer.
	if ContrastRatio(toward, bg) <= target {
		return toward
	}
	lo, hi := 0.0, 1.0
	// Monotonic in t, so a fixed bisection is exact enough for 8-bit channels.
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
