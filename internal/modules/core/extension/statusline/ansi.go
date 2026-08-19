// Package statusline holds the status-line layouts and colour themes.
//
// Its own package because it is 700-odd lines of pure rendering with no
// dependency on anything else here — given a snapshot it returns rows of text.
// That makes every layout testable by calling it, which matters more than
// usual for code whose output is judged by eye.
package statusline

import (
	"fmt"
	"math"
	"strings"
)

// Self-contained truecolor helpers. The theme layer in `tui` renders through
// named SLOTS, which is right for the app's own chrome and wrong here: these
// layouts are presets with fixed identities — "matrix" is green, not
// whatever-the-theme-calls-success — so they emit 24-bit colour directly.

// RGB is a truecolor value.
type RGB struct{ R, G, B int }

// StripANSI removes colour escapes.
func StripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// Len is the visible width, ignoring colour escapes.
//
// Rune count, not byte count: a layout that measured bytes would think a
// box-drawing character was three columns wide and shed segments that fit.
func Len(s string) int { return len([]rune(StripANSI(s))) }

// FG paints text in a foreground colour.
func FG(c RGB, text string) string {
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%s\x1b[39m", c.R, c.G, c.B, text)
}

// BG paints text on a background colour.
func BG(c RGB, text string) string {
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm%s\x1b[49m", c.R, c.G, c.B, text)
}

// Bold and Dim are the two attributes the layouts use.
func Bold(text string) string { return "\x1b[1m" + text + "\x1b[22m" }
func Dim(text string) string  { return "\x1b[2m" + text + "\x1b[22m" }

func lerp(a, b int, t float64) int { return int(math.Round(float64(a) + float64(b-a)*t)) }

// Mix interpolates between two colours.
func Mix(from, to RGB, t float64) RGB {
	return RGB{lerp(from.R, to.R, t), lerp(from.G, to.G, t), lerp(from.B, to.B, t)}
}

// SampleStops samples a multi-stop gradient at t in [0,1]. Two stops is a
// plain interpolation.
func SampleStops(stops []RGB, t float64) RGB {
	if len(stops) == 0 {
		return RGB{}
	}
	if len(stops) == 1 {
		return stops[0]
	}
	clamped := math.Min(1, math.Max(0, t))
	span := clamped * float64(len(stops)-1)
	i := int(math.Min(float64(len(stops)-2), math.Floor(span)))
	return Mix(stops[i], stops[i+1], span-float64(i))
}

// Hue is HSL→RGB at full saturation and 0.6 lightness — bright, saturated
// rainbow stops.
func Hue(h float64) RGB {
	const s, l = 1.0, 0.6
	c := (1 - math.Abs(2*l-1)) * s
	hp := math.Mod(math.Mod(h, 360)+360, 360) / 60
	x := c * (1 - math.Abs(math.Mod(hp, 2)-1))
	var r, g, b float64
	switch {
	case hp < 1:
		r, g, b = c, x, 0
	case hp < 2:
		r, g, b = x, c, 0
	case hp < 3:
		r, g, b = 0, c, x
	case hp < 4:
		r, g, b = 0, x, c
	case hp < 5:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	m := l - c/2
	return RGB{
		int(math.Round((r + m) * 255)),
		int(math.Round((g + m) * 255)),
		int(math.Round((b + m) * 255)),
	}
}

// Colours is the named palette the layouts read declaratively.
var Colours = struct {
	Text, Muted, Faint, Cyan, Green, Yellow, Orange, Red, Magenta, Blue RGB
}{
	Text:    RGB{205, 205, 205},
	Muted:   RGB{130, 130, 130},
	Faint:   RGB{95, 95, 95},
	Cyan:    RGB{56, 199, 222},
	Green:   RGB{80, 200, 120},
	Yellow:  RGB{224, 196, 64},
	Orange:  RGB{224, 153, 86},
	Red:     RGB{224, 72, 72},
	Magenta: RGB{214, 96, 184},
	Blue:    RGB{92, 148, 255},
}

// Heat is green → yellow → red, sampled by a 0..1 ratio: the usage heatmap.
func Heat(ratio float64) RGB {
	return SampleStops([]RGB{Colours.Green, Colours.Yellow, Colours.Red},
		math.Min(1, math.Max(0, ratio)))
}

// partialBlocks are the eighth-width blocks, for a bar's last cell.
const partialBlocks = " ▏▎▍▌▋▊▉"

// BarCells is a fixed-width progress bar's glyphs, uncoloured — callers paint
// the two halves as they like.
//
// The last partial cell uses a fractional block, so a short bar still reads
// smoothly instead of jumping a whole cell at a time.
func BarCells(ratio float64, width int) (filled, empty string) {
	r := math.Min(1, math.Max(0, ratio))
	exact := r * float64(width)
	full := int(math.Floor(exact))
	used := full
	filled = strings.Repeat("█", full)

	blocks := []rune(partialBlocks)
	if index := int(math.Round((exact - float64(full)) * 8)); full < width && index > 0 && index < len(blocks) {
		filled += string(blocks[index])
		used++
	}
	if n := width - used; n > 0 {
		empty = strings.Repeat("░", n)
	}
	return filled, empty
}
