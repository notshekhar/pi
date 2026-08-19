package tui

import (
	"strings"
	"testing"
	"time"
)

// The spinner is a CLOCK. Its frame must depend only on how long the turn has
// been running — not on how many times the screen happened to repaint, which
// is what made it blur while streaming and jump while scrolling.
func TestLoaderFrameDependsOnTimeNotCallCount(t *testing.T) {
	theme := NewTheme(NightPalette)
	l := &Loader{Word: "thinking", Start: time.Now()}

	// A hundred paints in the same instant must all show the same frame.
	first := frameGlyph(t, l.Tick(theme))
	for i := 0; i < 100; i++ {
		if got := frameGlyph(t, l.Tick(theme)); got != first {
			t.Fatalf("paint %d changed the frame with no time elapsed: %q -> %q", i, first, got)
		}
	}
}

func TestLoaderAdvancesWithTime(t *testing.T) {
	theme := NewTheme(NightPalette)
	// Started in the past, so the frame is a pure function of the offset.
	seen := map[string]bool{}
	for i := 0; i < len(loaderFrames); i++ {
		l := &Loader{Word: "thinking", Start: time.Now().Add(-time.Duration(i) * loaderFramePeriod)}
		seen[frameGlyph(t, l.Tick(theme))] = true
	}
	if len(seen) != len(loaderFrames) {
		t.Errorf("got %d distinct frames across a full cycle, want %d", len(seen), len(loaderFrames))
	}
}

// And it wraps rather than running off the end of the frame list.
func TestLoaderWrapsAroundTheCycle(t *testing.T) {
	theme := NewTheme(NightPalette)
	cycle := loaderFramePeriod * time.Duration(len(loaderFrames))
	// The GLYPH only: the rest of the line carries the elapsed counter, which
	// is supposed to differ a cycle apart.
	a := frameGlyph(t, (&Loader{Word: "thinking", Start: time.Now()}).Tick(theme))
	b := frameGlyph(t, (&Loader{Word: "thinking", Start: time.Now().Add(-cycle)}).Tick(theme))
	if a != b {
		t.Errorf("a full cycle did not return to the same frame: %q vs %q", a, b)
	}
}

// frameGlyph is the spinner character out of a rendered loader line.
func frameGlyph(t *testing.T, line string) string {
	t.Helper()
	for _, f := range loaderFrames {
		if strings.Contains(line, f) {
			return f
		}
	}
	t.Fatalf("no spinner frame in %q", line)
	return ""
}

// The frame budget: a repaint that comes hard on the heels of the last one
// waits out the rest of the interval instead of going straight to the
// terminal.
//
// This is what a token stream hits. Every delta is its own event, so without
// the budget a model emitting 300 tokens a second asks for 300 frames a
// second — none of which is visible, all of which is bytes down the wire and
// time not spent reading stdin.
func TestFrameBudgetHoldsBackABurst(t *testing.T) {
	a := &App{}
	now := time.Now()
	a.paintedAt = now

	if wait := a.frameWait(now.Add(time.Millisecond)); wait <= 0 {
		t.Fatalf("a frame 1ms after the last was not held back: wait=%v", wait)
	}
	if wait := a.frameWait(now.Add(FrameInterval)); wait > 0 {
		t.Fatalf("a frame a full interval later was held back: wait=%v", wait)
	}
	// An idle app paints the moment something happens — the budget is a
	// ceiling on painting, never a delay imposed on a quiet session.
	if wait := a.frameWait(now.Add(time.Second)); wait > 0 {
		t.Fatalf("an idle app was made to wait: wait=%v", wait)
	}
}

// The frame the loop is exiting on is owed unconditionally: Close erases the
// chrome over whatever is on screen, so a frame withheld here is never drawn.
func TestLastFrameIgnoresTheBudget(t *testing.T) {
	a := &App{done: true}
	now := time.Now()
	a.paintedAt = now
	if wait := a.frameWait(now); wait > 0 {
		t.Fatalf("the exit frame was held back: wait=%v", wait)
	}
}
