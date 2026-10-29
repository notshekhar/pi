package main

import (
	"context"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/status"
)

// Naming the session, once, from its opening exchange (loop's
// interactive/session-title.ts).
//
// A session that is only a ULID and a cwd is unidentifiable the moment you
// are not looking at it — which is exactly when something else is showing it
// to you: a terminal tab, a cmux pane's sidebar card, a notification. So the
// first turn earns a title, the title becomes the session's name AND the
// terminal's title, and everything downstream inherits it for free (cmux
// reads the terminal title for the pane's card, `/sessions` lists names).
//
// Deliberately once, and deliberately early: the opening exchange is what the
// session is about, later turns drift, and a name that keeps changing is
// worse than one that is slightly stale. A name set with `/name` is never
// overwritten.

// The tab also says what the agent is DOING, because a title alone cannot: a
// row of panes all reading "Fix the pty test" tells you nothing about which
// one is still thinking and which is waiting on you. Braille spinner while a
// turn runs, a filled marker when a prompt needs you, nothing when idle.
var tabSpinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	tabBlockedGlyph = "◆"
	// Slower than the on-screen spinner: this repaints someone else's chrome.
	tabSpinInterval = 250 * time.Millisecond
)

// defaultTabName is what the tab reads when the live session has no name of
// its own: at launch, and again the moment /new or /clear throws the old one
// away. A constant rather than the folder name: the folder is already on
// screen, and the one thing the tab can say that nothing else does is WHICH
// AGENT this pane is.
func defaultTabName() string { return "Pi Agent" }

// tabTitle keeps the terminal's tab showing the pane's name AND its state.
// The two halves are written by different things (the titler, the status
// bus), so they live here and either can change without erasing the other.
type tabTitle struct {
	mu     sync.Mutex
	set    func(string)
	title  string
	prefix string
	frame  int
	stop   chan struct{}
	closed bool
}

func (tt *tabTitle) repaintLocked() {
	if tt.closed || tt.set == nil {
		return
	}
	if tt.prefix != "" {
		tt.set(tt.prefix + " " + tt.title)
		return
	}
	tt.set(tt.title)
}

// attach follows the status bus. Returns the stop function, which leaves a
// plain, un-spinning title as the last thing written.
func (tt *tabTitle) attach(bus *status.Bus, set func(string), initial string) func() {
	tt.mu.Lock()
	tt.set, tt.title = set, initial
	tt.mu.Unlock()
	apply := func(e status.Event) {
		tt.mu.Lock()
		defer tt.mu.Unlock()
		if e.State == status.Working {
			if tt.stop != nil {
				return // already spinning — don't restart the animation
			}
			tt.prefix = tabSpinner[tt.frame]
			tt.repaintLocked()
			stop := make(chan struct{})
			tt.stop = stop
			go tt.spin(stop)
			return
		}
		tt.stopSpinLocked()
		tt.prefix = ""
		if e.State == status.Blocked {
			tt.prefix = tabBlockedGlyph
		}
		tt.repaintLocked()
	}
	bus.On(apply)
	apply(bus.Current())
	return func() {
		tt.mu.Lock()
		defer tt.mu.Unlock()
		tt.stopSpinLocked()
		tt.prefix = ""
		tt.repaintLocked()
		tt.closed = true
	}
}

func (tt *tabTitle) spin(stop chan struct{}) {
	tick := time.NewTicker(tabSpinInterval)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			tt.mu.Lock()
			if tt.stop == stop {
				tt.frame = (tt.frame + 1) % len(tabSpinner)
				tt.prefix = tabSpinner[tt.frame]
				tt.repaintLocked()
			}
			tt.mu.Unlock()
		}
	}
}

func (tt *tabTitle) stopSpinLocked() {
	if tt.stop != nil {
		close(tt.stop)
		tt.stop = nil
	}
}

// setName renames the tab, keeping whatever state glyph is showing.
func (tt *tabTitle) setName(name string) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.title = name
	tt.repaintLocked()
}

// setTabName names the tab after the session that is live now.
func (t *repl) setTabName(name string) { t.tab.setName(name) }

// tabNameFor is the tab's name for a session: its own, or the standing one.
func tabNameFor(s *slot) string {
	if n := s.run.Session.Name(); n != "" {
		return n
	}
	return defaultTabName()
}

// titleAttempts: sessions already titled in this process — one attempt
// each, ever.
var titleAttempts sync.Map

// maybeTitleSession titles the session if it hasn't been, then shows it.
// Best-effort and detached: every failure is silent, and the session keeps
// the identity it had.
func (t *repl) maybeTitleSession(userInput, assistantText string, aborted bool) {
	sess := t.run.Session
	// An interrupted turn says nothing about the session's subject, and a
	// name the user chose outranks anything a model would pick.
	if sess == nil || aborted || sess.ID == "" || sess.Name() != "" {
		return
	}
	if _, seen := titleAttempts.LoadOrStore(sess.ID, true); seen {
		return
	}
	modelID := t.cfg.FullID()
	if modelID == "" {
		return
	}
	title := agent.GenerateSessionTitle(context.Background(), agent.SessionTitleOptions{
		UserInput:     userInput,
		AssistantText: assistantText,
		ModelID:       modelID,
		Tracker:       t.tracker(),
		SessionPub:    sess.ID,
		CWD:           t.cfg.CWD,
	})
	if title == "" {
		return
	}
	// The session may have been renamed, replaced (/new), or ended while the
	// model was thinking — check again before writing.
	if t.run.Session != sess || sess.Name() != "" {
		return
	}
	if err := sess.SetName(title); err != nil {
		return
	}
	// The tab names what is on screen; a session titled in the background
	// shows its name in the switcher instead.
	if t.isForeground() {
		t.setTabName(title)
	}
	t.refreshSessionsChip()
}

// isForeground reports whether this repl's slot is the one on screen.
func (t *repl) isForeground() bool {
	t.slots.mu.Lock()
	defer t.slots.mu.Unlock()
	return t.slots.fg == t.slot
}

// clipRunes is JS's slice(0, n) over characters.
func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// anyTurning reports whether any live session has a turn running.
func (t *repl) anyTurning() bool {
	for _, s := range t.slots.all() {
		if (&repl{shared: t.shared, slot: s}).isTurning() {
			return true
		}
	}
	return false
}
