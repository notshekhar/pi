package agent

import (
	"context"
	"sync"
	"time"
)

// stallWatch aborts a subagent whose provider goes silent without erroring
// (a dead socket, a server-side hang) — otherwise the parent turn could only
// wait for ever. It is armed only while the PROVIDER owes the next part: a
// tool call in flight disarms it, since a long build is not a stall.
type stallWatch struct {
	mu      sync.Mutex
	after   time.Duration
	cancel  context.CancelFunc
	timer   *time.Timer
	pending int
	stalled bool
}

// newStallWatch returns a context the watch cancels on a stall. A zero
// duration never trips.
func newStallWatch(parent context.Context, after time.Duration) (*stallWatch, context.Context) {
	ctx, cancel := context.WithCancel(parent)
	w := &stallWatch{after: after, cancel: cancel}
	w.rearm()
	return w, ctx
}

// toolStarted and toolDone bracket a tool call; every part re-arms.
func (w *stallWatch) toolStarted() { w.adjust(1) }
func (w *stallWatch) toolDone()    { w.adjust(-1) }
func (w *stallWatch) part()        { w.adjust(0) }

func (w *stallWatch) adjust(delta int) {
	w.mu.Lock()
	w.pending = max(0, w.pending+delta)
	w.mu.Unlock()
	w.rearm()
}

func (w *stallWatch) rearm() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	if w.after > 0 && w.pending == 0 {
		w.timer = time.AfterFunc(w.after, w.trip)
	}
}

func (w *stallWatch) trip() {
	w.mu.Lock()
	w.stalled = true
	w.mu.Unlock()
	w.cancel()
}

// Stalled reports whether the watch cancelled the run.
func (w *stallWatch) Stalled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stalled
}

// stop releases the timer and the context.
func (w *stallWatch) stop() {
	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.mu.Unlock()
	w.cancel()
}
