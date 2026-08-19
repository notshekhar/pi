package tui

import (
	"time"
)

// Loader is the animated line under the transcript while a turn runs.
//
// Braille cells rather than a spinner of ASCII slashes: they are a single
// cell wide in every font that has them, and the eight dots give a smooth
// rotation at a frame rate slow enough not to compete with the rails.
var loaderFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var loaderWords = []string{
	"thinking", "hmm", "pondering", "cooking", "reading",
	"planning", "typing", "brewing", "forging", "scheming",
}

// Loader tracks the frame clock for a running turn.
type Loader struct {
	Word  string
	Start time.Time
}

// NewLoader starts the clock with a word picked from the start time.
func NewLoader() *Loader {
	now := time.Now()
	return &Loader{
		Word:  loaderWords[now.UnixNano()%int64(len(loaderWords))],
		Start: now,
	}
}

// Tick returns the styled line for the current moment.
//
// The frame is derived from ELAPSED TIME, not from a counter incremented on
// every call. It used to be a counter, and the effect was that the spinner's
// speed was the RENDER rate rather than a rate at all: the app repaints on
// every keystroke and on every mutation, so the spinner blurred while a
// response streamed in, jumped a frame per keypress while scrolling, and ran
// at its intended speed only when nothing else was happening. A spinner is a
// clock — it has to be driven by one.
func (l *Loader) Tick(t *Theme) string {
	elapsed := time.Since(l.Start)
	frame := loaderFrames[int(elapsed/loaderFramePeriod)%len(loaderFrames)]
	return t.Fg(SlotWarning, frame) + " " +
		t.Fg(SlotDim, l.Word+" · "+fmtDuration(elapsed.Round(time.Second))+" · esc to interrupt")
}

// loaderFramePeriod is how long one braille frame is held.
//
// Two frame clocks slower than the render tick: at 20fps the ten dots cycle
// twice a second, which reads as a blur rather than as rotation. This is the
// speed the counter was NEVER actually delivering, since it advanced once per
// paint.
const loaderFramePeriod = AnimInterval * 2
