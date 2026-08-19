package tui

import (
	"strings"
	"time"
)

// The left accent rail — the vertical line every transcript block hangs off.
//
// Ported from loop's rail.ts, which took the column model from grok:
//
//	│A│PL│      Content      │
//	│1│ 2│       flex        │
//
// One column of rail, two of padding, then content. Header and body share the
// same chrome, so a block reads as one object bracketed by a single line
// instead of a title with a differently-indented body hanging under it.
//
// The rail also carries a block's state as MOTION rather than more text:
// running waves, blocked pulses, settled holds its outcome colour.

// Both glyphs come from the BOX-DRAWING block, and that is the whole
// requirement: box-drawing glyphs are designed to tile, so a column of them
// joins into one unbroken line. grok's `❙` is a Dingbat drawn as a SHORT bar
// inside the cell, so a stack of them renders as a dashed ladder, not a rail.
//
// Weight, not glyph family, carries the distinction: heavy for live and open
// blocks, light for settled and folded ones. Both tile.
const (
	railGlyph     = "┃"
	railCollapsed = "│"
)

// RailWidth is the columns the rail and its padding occupy. Content starts
// here.
const RailWidth = 3

// RailMotion is how a rail animates.
type RailMotion int

const (
	MotionNone RailMotion = iota
	MotionWave
	MotionPulse
	MotionStatic
)

// RailSpec is a resolved rail: colour, motion, and weight.
type RailSpec struct {
	// Color is the rail at full brightness (hex).
	Color  string
	Motion RailMotion
	// Collapsed uses the thin glyph — a folded block.
	Collapsed bool
}

// RailColors are the theme's already-resolved hex for each rail role. The
// caller owns slot choice: a tool's accent is not a thinking block's.
type RailColors struct {
	Running, Success, Error, Quiet string
}

// BlockState is the display state a rail is derived from.
type BlockState struct {
	IsPartial   bool
	IsError     bool
	Interrupted bool
	Expanded    bool
	Selected    bool
	// Blocked means the call has stopped to ask the user.
	Blocked bool
	// FinishedAt is when the call stopped running, for the finish flash.
	// Zero on replayed transcripts — those were never seen running.
	FinishedAt time.Time
}

// RailFor decides a block's rail from its display state. One place, so every
// block type animates off the same rules.
func RailFor(state BlockState, colors RailColors, bg string) *RailSpec {
	if state.IsPartial {
		// A call that has stopped to ask the user freezes its wave: the same
		// rail still says "this is the live one", but the motion says paused.
		motion := MotionWave
		if state.Blocked {
			motion = MotionPulse
		}
		return &RailSpec{Color: colors.Running, Motion: motion}
	}
	if state.Interrupted {
		return &RailSpec{Color: colors.Quiet, Motion: MotionStatic, Collapsed: !state.Expanded}
	}

	settled := colors.Success
	if state.IsError {
		settled = colors.Error
	}
	// The brief full-brightness beat as a call lands, before it settles.
	if !state.FinishedAt.IsZero() && time.Since(state.FinishedAt) < FinishFlash {
		return &RailSpec{Color: settled, Motion: MotionStatic}
	}
	// Settled — and it STAYS. A finished call keeps its rail in its outcome
	// colour forever, so a scrolled-back transcript still shows at a glance
	// which calls worked and which didn't.
	//
	// Held down toward the canvas so a long transcript reads as a calm ladder
	// rather than a wall of saturated green — only the LIVE rail is at full
	// strength, which is what makes the running one findable.
	blend := 0.4
	if state.Expanded {
		blend = 0.55
	}
	return &RailSpec{
		Color:     Mix(bg, settled, blend),
		Motion:    MotionStatic,
		Collapsed: !state.Expanded,
	}
}

// WithRail prefixes lines with the rail column and its padding.
//
// A nil spec still pads, so content never shifts sideways when a block stops
// animating — only the line itself appears and disappears.
func WithRail(t *Theme, lines []string, spec *RailSpec, bg string, tick int64) []string {
	out := make([]string, len(lines))
	if spec == nil {
		pad := strings.Repeat(" ", RailWidth)
		for i, l := range lines {
			out[i] = pad + l
		}
		return out
	}

	glyph := railGlyph
	if spec.Collapsed {
		glyph = railCollapsed
	}
	for row, line := range lines {
		color := spec.Color
		switch spec.Motion {
		case MotionWave:
			color = accentAtBrightness(bg, spec.Color, waveBrightness(row, tick), 0.25)
		case MotionPulse:
			color = accentAtBrightness(bg, spec.Color, pulseBrightness(tick), 0.3)
		}
		out[row] = t.FgHex(color, glyph) + "  " + line
	}
	return out
}

// BulletColor is the diamond's colour, synced to the head of its own rail's
// wave — grok animates the bullet off the same curve the rail's top row uses,
// so the glyph and the line it sits on pulse as one mark rather than two
// things that happen to both be moving.
func BulletColor(spec *RailSpec, bg, fallback string, tick int64) string {
	if spec == nil {
		return fallback
	}
	switch spec.Motion {
	case MotionWave:
		return accentAtBrightness(bg, spec.Color, waveBrightness(0, tick), 0.25)
	case MotionPulse:
		return accentAtBrightness(bg, spec.Color, pulseBrightness(tick), 0.3)
	}
	return spec.Color
}
