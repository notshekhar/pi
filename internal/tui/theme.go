package tui

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/core/home"
)

// The theme engine — loop's ui/theme.ts. Nothing else in pi names a colour:
// a renderer asks for a SLOT and the active theme resolves it.
//
// Built-in themes are palettes (palette.go); custom ones load from
// ~/.loop/agent/themes/<name>.json in loop's shape ({name, vars, colors}),
// and extensions may contribute partial ones. Missing slots fall back to
// night's, so a theme written before a slot existed still renders.

// Slot is a named colour role.
type Slot string

// Every slot loop's ThemeColors has (plus bgSunken, the recessed surface).
const (
	SlotAccent             Slot = "accent"
	SlotBorder             Slot = "border"
	SlotBorderAccent       Slot = "borderAccent"
	SlotBorderMuted        Slot = "borderMuted"
	SlotSuccess            Slot = "success"
	SlotError              Slot = "error"
	SlotWarning            Slot = "warning"
	SlotMuted              Slot = "muted"
	SlotDim                Slot = "dim"
	SlotText               Slot = "text"
	SlotThinkingText       Slot = "thinkingText"
	SlotSelected           Slot = "selectedBg"
	SlotUserMessageBg      Slot = "userMessageBg"
	SlotUserMessageText    Slot = "userMessageText"
	SlotCustomMessageBg    Slot = "customMessageBg"
	SlotCustomMessageText  Slot = "customMessageText"
	SlotCustomMessageLabel Slot = "customMessageLabel"
	SlotToolPendingBg      Slot = "toolPendingBg"
	SlotToolSuccessBg      Slot = "toolSuccessBg"
	SlotToolErrorBg        Slot = "toolErrorBg"
	SlotToolTitle          Slot = "toolTitle"
	SlotToolOutput         Slot = "toolOutput"
	SlotToolError          Slot = "toolError"
	SlotMdHeading          Slot = "mdHeading"
	SlotMdLink             Slot = "mdLink"
	SlotMdLinkURL          Slot = "mdLinkUrl"
	SlotMdCode             Slot = "mdCode"
	SlotMdCodeBlock        Slot = "mdCodeBlock"
	SlotMdCodeBlockBorder  Slot = "mdCodeBlockBorder"
	SlotMdQuote            Slot = "mdQuote"
	SlotMdQuoteBorder      Slot = "mdQuoteBorder"
	SlotMdHr               Slot = "mdHr"
	SlotMdListBullet       Slot = "mdListBullet"
	SlotToolDiffAdded      Slot = "toolDiffAdded"
	SlotToolDiffRemoved    Slot = "toolDiffRemoved"
	SlotToolDiffContext    Slot = "toolDiffContext"
	SlotSyntaxComment      Slot = "syntaxComment"
	SlotSyntaxKeyword      Slot = "syntaxKeyword"
	SlotSyntaxFunction     Slot = "syntaxFunction"
	SlotSyntaxVariable     Slot = "syntaxVariable"
	SlotSyntaxString       Slot = "syntaxString"
	SlotSyntaxNumber       Slot = "syntaxNumber"
	SlotSyntaxType         Slot = "syntaxType"
	SlotSyntaxOperator     Slot = "syntaxOperator"
	SlotSyntaxPunctuation  Slot = "syntaxPunctuation"
	SlotThinkingOff        Slot = "thinkingOff"
	SlotThinkingMinimal    Slot = "thinkingMinimal"
	SlotThinkingLow        Slot = "thinkingLow"
	SlotThinkingMedium     Slot = "thinkingMedium"
	SlotThinkingHigh       Slot = "thinkingHigh"
	SlotThinkingXhigh      Slot = "thinkingXhigh"
	SlotBashMode           Slot = "bashMode"
	SlotInputBorder        Slot = "inputBorder"
	SlotInputCommand       Slot = "inputCommand"
	SlotHookAccent         Slot = "hookAccent"
	SlotBgBase             Slot = "bgBase"
	SlotBgRaised           Slot = "bgRaised"
	SlotAccentUser         Slot = "accentUser"
	SlotAccentAssistant    Slot = "accentAssistant"
	SlotAccentThinking     Slot = "accentThinking"
	SlotAccentTool         Slot = "accentTool"
	SlotTimestamp          Slot = "timestamp"
	SlotSelection          Slot = "selectionBorder"
	SlotTurnSummary        Slot = "turnSummary"

	// SlotBgSunken is the recessed surface (tool bodies); themes without it
	// fall back to toolPendingBg, which loop derives from the same value.
	SlotBgSunken Slot = "bgSunken"
	// SlotThinkingPeak is the top of the effort ladder (thinkingXhigh).
	SlotThinkingPeak Slot = "thinkingXhigh"
)

// bgSlots are painted as backgrounds (loop's BG_KEYS, plus bgSunken).
var bgSlots = map[string]bool{"selectedBg": true, "userMessageBg": true, "customMessageBg": true,
	"toolPendingBg": true, "toolSuccessBg": true, "toolErrorBg": true, "bgBase": true, "bgRaised": true, "bgSunken": true}

// ColorValue is a slot value: a hex, a 256-colour index, "" (terminal
// default), or a var name.
type ColorValue struct {
	Hex   string
	Index int
	IsNum bool
}

// UnmarshalJSON reads a string or a number.
func (c *ColorValue) UnmarshalJSON(b []byte) error {
	var n float64
	if json.Unmarshal(b, &n) == nil {
		*c = ColorValue{Index: int(n), IsNum: true}
		return nil
	}
	return json.Unmarshal(b, &c.Hex)
}

// MarshalJSON writes it back the same way.
func (c ColorValue) MarshalJSON() ([]byte, error) {
	if c.IsNum {
		return json.Marshal(c.Index)
	}
	return json.Marshal(c.Hex)
}

// ThemeJSON is loop's ThemeJson.
type ThemeJSON struct {
	Name   string                `json:"name"`
	Vars   map[string]ColorValue `json:"vars,omitempty"`
	Colors map[string]ColorValue `json:"colors"`
}

func resolveVar(v ColorValue, vars map[string]ColorValue, seen map[string]bool) (ColorValue, error) {
	if v.IsNum || v.Hex == "" || strings.HasPrefix(v.Hex, "#") {
		return v, nil
	}
	if seen[v.Hex] {
		return v, fmt.Errorf("Circular variable reference: %s", v.Hex)
	}
	next, ok := vars[v.Hex]
	if !ok {
		return v, fmt.Errorf("Variable reference not found: %s", v.Hex)
	}
	seen[v.Hex] = true
	return resolveVar(next, vars, seen)
}

// ── colour modes (loop's fgAnsi / bgAnsi / rgbTo256) ─────────────────────

var (
	cubeValues = []int{0, 95, 135, 175, 215, 255}
	grayValues = func() []int {
		out := make([]int, 24)
		for i := range out {
			out[i] = 8 + i*10
		}
		return out
	}()
)

func findClosest(values []int, target int) int {
	best, idx := math.MaxInt, 0
	for i, v := range values {
		if d := abs(target - v); d < best {
			best, idx = d, i
		}
	}
	return idx
}

func colorDistance(r1, g1, b1, r2, g2, b2 int) float64 {
	dr, dg, db := float64(r1-r2), float64(g1-g2), float64(b1-b2)
	return float64(float64(dr*dr)*0.299) + float64(float64(dg*dg)*0.587) + float64(float64(db*db)*0.114)
}

// rgbTo256 is loop's: the 6×6×6 cube, or the grey ramp when it is closer
// and the colour is (nearly) grey.
func rgbTo256(r, g, b int) int {
	ri, gi, bi := findClosest(cubeValues, r), findClosest(cubeValues, g), findClosest(cubeValues, b)
	cube := 16 + 36*ri + 6*gi + bi
	cubeDist := colorDistance(r, g, b, cubeValues[ri], cubeValues[gi], cubeValues[bi])
	gray := jsRound(float64(0.299*float64(r)) + float64(0.587*float64(g)) + float64(0.114*float64(b)))
	gIdx := findClosest(grayValues, gray)
	gv := grayValues[gIdx]
	grayDist := colorDistance(r, g, b, gv, gv, gv)
	spread := max(r, g, b) - min(r, g, b)
	if spread < 10 && grayDist < cubeDist {
		return 232 + gIdx
	}
	return cube
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func ansiFor(layer int, c ColorValue, trueColor bool) string {
	if !c.IsNum && c.Hex == "" {
		return fmt.Sprintf("\x1b[%dm", layer+1) // 39 / 49: terminal default
	}
	if c.IsNum {
		return fmt.Sprintf("\x1b[%d;5;%dm", layer, c.Index)
	}
	r, g, b := rgb(c.Hex)
	if trueColor {
		return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", layer, r, g, b)
	}
	return fmt.Sprintf("\x1b[%d;5;%dm", layer, rgbTo256(r, g, b))
}

var trueColorRE = regexp.MustCompile(`(?i)truecolor|24bit`)

// DetectTrueColor is loop's detectColorMode: COLORTERM says truecolor/24bit.
func DetectTrueColor() bool { return trueColorRE.MatchString(os.Getenv("COLORTERM")) }

// ── the theme ───────────────────────────────────────────────────────────

// Theme is loop's Theme: a ThemeJSON resolved to escapes.
type Theme struct {
	Name string
	// TrueColor emits 24-bit SGR; off maps onto the 256-colour palette.
	TrueColor bool
	raw       map[string]ColorValue
	fg, bg    map[string]string
	derived   sync.Map
}

// NewThemeJSON resolves a theme in the terminal's colour mode.
func NewThemeJSON(j ThemeJSON) (*Theme, error) {
	t := &Theme{Name: j.Name, TrueColor: DetectTrueColor(), raw: map[string]ColorValue{}}
	for k, v := range j.Colors {
		r, err := resolveVar(v, j.Vars, map[string]bool{})
		if err != nil {
			return nil, err
		}
		t.raw[k] = r
	}
	t.build()
	return t, nil
}

// NewTheme is the theme a palette derives.
func NewTheme(p Palette) *Theme {
	t, _ := NewThemeJSON(ThemeFromPalette(p))
	return t
}

func (t *Theme) build() {
	t.fg, t.bg = map[string]string{}, map[string]string{}
	t.derived = sync.Map{}
	if _, ok := t.raw["bgSunken"]; !ok {
		if v, ok := t.raw["toolPendingBg"]; ok {
			t.raw["bgSunken"] = v
		}
	}
	for k, v := range t.raw {
		if bgSlots[k] {
			t.bg[k] = ansiFor(48, v, t.TrueColor)
		} else {
			t.fg[k] = ansiFor(38, v, t.TrueColor)
		}
	}
}

// SetTrueColor switches colour depth (rebuilding every escape).
func (t *Theme) SetTrueColor(on bool) {
	if t.TrueColor != on {
		t.TrueColor = on
		t.build()
	}
}

// Raw is a slot's resolved value.
func (t *Theme) Raw(s Slot) (ColorValue, bool) {
	v, ok := t.raw[string(s)]
	return v, ok
}

// Hex is a slot's colour as hex, for blending; "" when it is an index or
// the terminal default.
func (t *Theme) Hex(s Slot) string {
	if v, ok := t.raw[string(s)]; ok && !v.IsNum && strings.HasPrefix(v.Hex, "#") {
		return v.Hex
	}
	return ""
}

// Fg paints text in a slot's colour. A background slot asked for as a
// foreground is painted in that colour too (a fill used as ink).
func (t *Theme) Fg(s Slot, text string) string {
	if a, ok := t.fg[string(s)]; ok {
		return a + text + "\x1b[39m"
	}
	if v, ok := t.raw[string(s)]; ok {
		return ansiFor(38, v, t.TrueColor) + text + "\x1b[39m"
	}
	return text
}

// Bg fills text's cells with a slot's colour.
func (t *Theme) Bg(s Slot, text string) string {
	if a, ok := t.bg[string(s)]; ok {
		return a + text + "\x1b[49m"
	}
	if v, ok := t.raw[string(s)]; ok {
		return ansiFor(48, v, t.TrueColor) + text + "\x1b[49m"
	}
	return text
}

// FgHex paints text in a computed colour (loop's fgHex).
func (t *Theme) FgHex(hex, text string) string {
	return ansiFor(38, ColorValue{Hex: hex}, t.TrueColor) + text + "\x1b[39m"
}

// BgHex fills cells with a computed colour (loop's bgHex).
func (t *Theme) BgHex(hex, text string) string {
	return ansiFor(48, ColorValue{Hex: hex}, t.TrueColor) + text + "\x1b[49m"
}

// VerticalRule is loop's verticalRule: Terminal.app paints a space's
// background (gapless); elsewhere the glyph.
func (t *Theme) VerticalRule(hex, glyph string) string {
	if os.Getenv("TERM_PROGRAM") == "Apple_Terminal" {
		return t.BgHex(hex, " ")
	}
	return t.FgHex(hex, glyph)
}

// VerticalRuleSlot is VerticalRule for a slot.
func (t *Theme) VerticalRuleSlot(s Slot, glyph string) string {
	if h := t.Hex(s); h != "" {
		return t.VerticalRule(h, glyph)
	}
	return t.Fg(s, glyph)
}

var seriesSlots = []Slot{SlotMdHeading, SlotMdLink, SlotSyntaxType, SlotSyntaxNumber, SlotSuccess, SlotError, SlotSyntaxVariable}

// Series is the i-th categorical swatch, cycling.
func (t *Theme) Series(i int, text string) string {
	n := len(seriesSlots)
	return t.Fg(seriesSlots[((i%n)+n)%n], text)
}

// SeriesLength is how many swatches Series has.
func SeriesLength() int { return len(seriesSlots) }

var heatSteps = []float64{0.3, 0.55, 0.78, 1}

// Heat is a step on the activity ramp: success walked out of the canvas.
func (t *Theme) Heat(level int, text string) string {
	if level <= 0 {
		return t.Fg(SlotThinkingOff, text)
	}
	if a, ok := t.derived.Load(level); ok {
		return a.(string) + text + "\x1b[39m"
	}
	var a string
	if h := t.Hex(SlotSuccess); h != "" {
		floor := "#000000"
		if t.IsLight() {
			floor = "#ffffff"
		}
		a = ansiFor(38, ColorValue{Hex: Mix(floor, h, heatSteps[min(level, len(heatSteps))-1])}, t.TrueColor)
	} else {
		a = t.fg["success"]
	}
	t.derived.Store(level, a)
	return a + text + "\x1b[39m"
}

// CanvasBg is loop's canvasBg: what rails and bullets blend toward — the
// terminal's own background under system, else bgBase (or the palette's).
func (t *Theme) CanvasBg() string {
	if t.Name == SystemThemeName {
		c, _ := SystemCanvas()
		return c
	}
	if h := t.Hex(SlotBgBase); h != "" {
		return h
	}
	if t.IsLight() {
		return DayPalette.Bg
	}
	return NightPalette.Bg
}

// IsLight: the theme paints dark text, i.e. it is for a light terminal.
func (t *Theme) IsLight() bool {
	h := t.Hex(SlotText)
	return h != "" && Luminance(h) < 0.5
}

func (t *Theme) Bold(s string) string      { return "\x1b[1m" + s + "\x1b[22m" }
func (t *Theme) Dim(s string) string       { return "\x1b[2m" + s + "\x1b[22m" }
func (t *Theme) Italic(s string) string    { return "\x1b[3m" + s + "\x1b[23m" }
func (t *Theme) Underline(s string) string { return "\x1b[4m" + s + "\x1b[24m" }
func (t *Theme) Strike(s string) string    { return "\x1b[9m" + s + "\x1b[29m" }

// StylePrefix is the opening half of a style function (inline markdown
// re-opens the surrounding style after a codespan's own reset).
func StylePrefix(style func(string) string) string {
	const sentinel = "\x00"
	styled := style(sentinel)
	if i := strings.Index(styled, sentinel); i >= 0 {
		return styled[:i]
	}
	return ""
}

// ── the registry (built-ins, extensions, the user's files) ──────────────

var nightJSON = ThemeFromPalette(NightPalette)

// ExtensionThemes is what enabled extensions contribute (partial themes).
var ExtensionThemes func() []ThemeJSON

// BuiltinThemes is loop's builtinThemes: night, day, system (rebuilt for
// whatever the terminal reported).
func BuiltinThemes() []ThemeJSON {
	return []ThemeJSON{ThemeFromPalette(NightPalette), ThemeFromPalette(DayPalette), ThemeFromPalette(SystemPalette())}
}

// AvailableThemes is the builtins then what extensions contribute.
func AvailableThemes() []ThemeJSON {
	all := BuiltinThemes()
	if ExtensionThemes != nil {
		all = append(all, ExtensionThemes()...)
	}
	byName := map[string]int{}
	var out []ThemeJSON
	for _, t := range all {
		if i, ok := byName[t.Name]; ok {
			out[i] = t
			continue
		}
		byName[t.Name] = len(out)
		out = append(out, t)
	}
	return out
}

// ThemesDir is ~/.loop/agent/themes.
func ThemesDir() string { return home.AgentDir("themes") }

func customThemeNames() []string {
	entries, err := os.ReadDir(ThemesDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Strings(out)
	return out
}

// ThemeNames is every pickable name: shipped, contributed, the user's files.
func ThemeNames() []string {
	var out []string
	for _, t := range AvailableThemes() {
		out = append(out, t.Name)
	}
	return append(out, customThemeNames()...)
}

// withFallback fills missing slots (and vars) from night.
func withFallback(j ThemeJSON) ThemeJSON {
	out := ThemeJSON{Name: j.Name, Vars: map[string]ColorValue{}, Colors: map[string]ColorValue{}}
	for k, v := range nightJSON.Vars {
		out.Vars[k] = v
	}
	for k, v := range j.Vars {
		out.Vars[k] = v
	}
	for k, v := range nightJSON.Colors {
		out.Colors[k] = v
	}
	for k, v := range j.Colors {
		out.Colors[k] = v
	}
	return out
}

func loadThemeJSON(name string) (ThemeJSON, error) {
	for _, t := range AvailableThemes() {
		if t.Name == name {
			return withFallback(t), nil
		}
	}
	raw, err := os.ReadFile(filepath.Join(ThemesDir(), name+".json"))
	if err != nil {
		return ThemeJSON{}, err
	}
	var j ThemeJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return ThemeJSON{}, err
	}
	if j.Name == "" {
		j.Name = name
	}
	return withFallback(j), nil
}

// LoadTheme is loop's initTheme(name) without the fallback: ok false for an
// unknown or broken theme.
func LoadTheme(name string) (*Theme, bool) {
	j, err := loadThemeJSON(name)
	if err != nil {
		return nil, false
	}
	t, err := NewThemeJSON(j)
	if err != nil {
		return nil, false
	}
	return t, true
}

// InitTheme is loop's initTheme: the named theme, else night.
func InitTheme(name string) *Theme {
	if t, ok := LoadTheme(name); ok {
		return t
	}
	return NewTheme(NightPalette)
}

// ConfiguredThemeName is loop's configuredThemeName: `theme`, then the old
// per-mode keys, with dark/light mapped to night/day.
func ConfiguredThemeName(theme string, uiThemes map[string]string) string {
	saved := theme
	if saved == "" {
		saved = uiThemes["noir"]
	}
	if saved == "" {
		saved = uiThemes["grok"]
	}
	switch saved {
	case "dark":
		return "night"
	case "light":
		return "day"
	case "":
		return "night"
	}
	return saved
}

// ── canvas wash (loop's canvas-wash.ts) ─────────────────────────────────

var washApplied bool

// ApplyCanvasWash sets the terminal's default background (OSC 11) and
// foreground (OSC 10) to the theme's bgBase/text, or hands them back when the
// theme washes nothing.
func ApplyCanvasWash(t *Theme) {
	if bg := t.Hex(SlotBgBase); bg != "" {
		fmt.Print("\x1b]11;" + bg + "\x07")
		if fg := t.Hex(SlotText); fg != "" {
			fmt.Print("\x1b]10;" + fg + "\x07")
		}
		washApplied = true
	} else if washApplied {
		ReleaseCanvas()
	}
}

// ReleaseCanvas restores the terminal's own colours (safe to call twice).
func ReleaseCanvas() {
	if !washApplied {
		return
	}
	fmt.Print("\x1b]111\x07\x1b]110\x07")
	washApplied = false
}
