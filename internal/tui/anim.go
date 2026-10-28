package tui

import (
	"math"
	"sync/atomic"
	"time"
)

// The transcript's animation clock — one shared frame counter every animated
// surface reads, plus the brightness curves that turn a tick into a colour.
//
// Ported from loop's anim.ts, which took the model from grok: a running
// block's rail carries a sin² wave travelling DOWN it, while a blocked one
// freezes into a slow whole-block pulse. Same maths, same constants, so the
// motion reads the same.

// FPS is the frame rate. Deliberately 20 rather than grok's 30: the frame
// clock is a second, independent source of repaints on top of model deltas,
// so it runs as slow as it can while still reading as smooth.
const FPS = 20

// AnimInterval is the frame period, for whoever owns the ticker.
const AnimInterval = time.Second / FPS

const (
	// waveSpeed is radians per tick for the running wave. grok uses 0.15 at
	// 30fps; matching its wall-clock speed at 20fps scales by 30/20.
	waveSpeed = 0.15 * (30.0 / FPS)
	// waveRows is rows per full wave cycle down a rail. Larger = lazier.
	waveRows = 32
	// pulseSpeed is radians per tick for the "waiting on you" pulse.
	pulseSpeed = 0.08 * (30.0 / FPS)
)

// FinishFlash is how long a rail holds its finished colour at full strength
// before settling.
const FinishFlash = 400 * time.Millisecond

// tick is monotonic while the clock runs and frozen — not reset — while it
// does not, so a wave resumes from its own phase instead of snapping back to
// the top of the cycle when the next tool starts.
var tick atomic.Int64

// AnimTick is the current frame.
func AnimTick() int64 { return tick.Load() }

// AdvanceAnim steps the clock one frame.
func AdvanceAnim() { tick.Add(1) }

// SetAnimTickForTest pins the frame. Animated rendering is a pure function of
// the tick, so a test can step frames instead of sleeping and hoping.
func SetAnimTickForTest(frame int64) { tick.Store(frame) }

// waveBrightness is the brightness (0..1) for a row of a running rail — a
// sin² wave whose phase advances with the row, so the bright band travels
// down the rail.
//
// sin² rather than a raw sine because it never goes negative and spends more
// of its cycle near the extremes, which reads as a pulse rather than a wash.
func waveBrightness(row int, t int64) float64 {
	phase := (float64(row) / waveRows) * 2 * math.Pi
	s := math.Sin(float64(t)*waveSpeed + phase)
	return s * s
}

// pulseBrightness is the brightness for a whole-element pulse — no spatial
// phase, so everything sharing a tick pulses in unison. This is the "paused
// on you" cue, and the point is that it reads as DIFFERENT motion from the
// running wave.
func pulseBrightness(t int64) float64 {
	s := math.Sin(float64(t) * pulseSpeed)
	return s * s
}

// accentAtBrightness applies a brightness to an accent by blending it toward
// the canvas.
//
// floor keeps the trough visible: at brightness 0 a rail blended all the way
// to the background would vanish and the block would look like it had ended.
func accentAtBrightness(bg, accent string, brightness, floor float64) string {
	return Mix(bg, accent, floor+(1-floor)*brightness)
}
