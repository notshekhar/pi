package statusline

// Colour themes recolour the ALREADY-RENDERED rows, whatever the active
// layout produced: the existing colour is stripped and the plain text is
// repainted. That is what lets the two axes compose — a layout decides the
// structure, a theme decides the colour, and neither knows about the other.

// themeKind is how a theme paints.
type themeKind int

const (
	// themeOff leaves the native colours alone.
	themeOff themeKind = iota
	// themeSolid is one flat colour.
	themeSolid
	// themeGradient interpolates left to right across two or more stops.
	themeGradient
	// themeRainbow sweeps the hue per character.
	themeRainbow
)

// Theme is one colour preset.
type Theme struct {
	ID          string
	Description string
	kind        themeKind
	colour      RGB
	stops       []RGB
}

// Themes is every colour preset, in menu order.
var Themes = []Theme{
	{ID: "default", Description: "the native colours (agent orange, model cyan, cost green)", kind: themeOff},
	{ID: "mono", Description: "no colour — plain monochrome text", kind: themeSolid, colour: RGB{170, 170, 170}},
	{ID: "matrix", Description: "all green, terminal-hacker vibes", kind: themeSolid, colour: RGB{0, 255, 102}},
	{ID: "ocean", Description: "blue → cyan gradient", kind: themeGradient,
		stops: []RGB{{36, 92, 255}, {0, 224, 224}}},
	{ID: "sunset", Description: "orange → magenta gradient", kind: themeGradient,
		stops: []RGB{{255, 153, 51}, {224, 32, 160}}},
	{ID: "synthwave", Description: "magenta → cyan gradient (retro neon)", kind: themeGradient,
		stops: []RGB{{255, 41, 184}, {41, 224, 255}}},
	{ID: "fire", Description: "red → yellow gradient", kind: themeGradient,
		stops: []RGB{{255, 40, 0}, {255, 214, 0}}},
	{ID: "rainbow", Description: "every character a different hue", kind: themeRainbow},
	{ID: "heat", Description: "green → yellow → red, the context-bar heatmap", kind: themeGradient,
		stops: []RGB{{0, 200, 80}, {220, 200, 0}, {220, 40, 20}}},
	{ID: "neon", Description: "yellow → magenta → cyan", kind: themeGradient,
		stops: []RGB{{255, 255, 0}, {255, 0, 255}, {0, 184, 220}}},
	{ID: "gold", Description: "solid gold", kind: themeSolid, colour: RGB{255, 255, 0}},
	{ID: "cyber", Description: "solid cyan", kind: themeSolid, colour: RGB{0, 184, 220}},
}

// DefaultTheme leaves the native colours alone.
const DefaultTheme = "default"

// GetTheme finds a theme by id, falling back to the default.
func GetTheme(id string) Theme {
	for _, t := range Themes {
		if t.ID == id {
			return t
		}
	}
	return Themes[0]
}

// Apply recolours one rendered row.
//
// Width is preserved because colour escapes occupy no columns — which is the
// property that lets this run AFTER the layout has already fitted its content
// to the terminal.
func (t Theme) Apply(line string) string {
	if t.kind == themeOff {
		return line
	}
	plain := StripANSI(line)
	if plain == "" {
		return line
	}
	if t.kind == themeSolid {
		return FG(t.colour, plain)
	}

	chars := []rune(plain)
	n := float64(max(1, len(chars)-1))
	var out string
	for i, r := range chars {
		at := float64(i) / n
		colour := SampleStops(t.stops, at)
		if t.kind == themeRainbow {
			colour = Hue(at * 320)
		}
		out += FG(colour, string(r))
	}
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
