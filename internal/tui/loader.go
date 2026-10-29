package tui

import (
	"time"
)

// Loader is the working line under the transcript (loop's working
// indicator): a braille spinner in the accent colour, then what is happening
// — "Generating", "Compacting", "Summarizing branch" — with the interrupt
// hint.
//
// Braille cells rather than a spinner of ASCII slashes: they are a single
// cell wide in every font that has them, and the eight dots give a smooth
// rotation.
var loaderFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// loaderFramePeriod is how long one frame is held: loop's Loader interval.
const loaderFramePeriod = 80 * time.Millisecond

// DefaultLoaderMessage is what a running turn says (loop's "Generating").
const DefaultLoaderMessage = "Generating"

// Loader tracks the frame clock and the message of one working stretch.
type Loader struct {
	Message string
	Start   time.Time
}

// NewLoader starts the clock.
func NewLoader(message string) *Loader {
	if message == "" {
		message = DefaultLoaderMessage
	}
	return &Loader{Message: message, Start: time.Now()}
}

// Tick returns the styled line for the current moment.
//
// The frame is derived from ELAPSED TIME, not from a counter incremented on
// every call: the app repaints on every keystroke and every mutation, and a
// counter would make the spinner's speed the render rate — blurring while a
// response streams, jumping a frame per keypress. A spinner is a clock.
func (l *Loader) Tick(t *Theme) string {
	frame := loaderFrames[int(time.Since(l.Start)/loaderFramePeriod)%len(loaderFrames)]
	return t.Fg(SlotAccent, frame) + " " + t.Fg(SlotDim, l.Message+" (Esc to interrupt)")
}
