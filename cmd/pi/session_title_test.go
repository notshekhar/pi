package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/notshekhar/pi/internal/core/status"
)

type titleLog struct {
	mu   sync.Mutex
	seen []string
}

func (l *titleLog) set(s string) { l.mu.Lock(); l.seen = append(l.seen, s); l.mu.Unlock() }
func (l *titleLog) last() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.seen) == 0 {
		return ""
	}
	return l.seen[len(l.seen)-1]
}

func TestTabTitleFollowsTheAgent(t *testing.T) {
	bus := status.New(0)
	var log titleLog
	var tt tabTitle
	stop := tt.attach(bus, log.set, defaultTabName())
	if log.last() != "Pi Agent" {
		t.Fatalf("initial title %q", log.last())
	}
	tt.setName("Fix flaky pty test")
	bus.SetWorking()
	if got := log.last(); !strings.HasSuffix(got, " Fix flaky pty test") || got == "Fix flaky pty test" {
		t.Fatalf("working title %q has no spinner", got)
	}
	// A title arriving mid-turn keeps the spinner.
	tt.setName("Renamed")
	if got := log.last(); !strings.HasSuffix(got, " Renamed") || !strings.ContainsAny(got[:3], strings.Join(tabSpinner, "")) {
		t.Fatalf("rename mid-turn lost the spinner: %q", got)
	}
	time.Sleep(3 * tabSpinInterval / 2)
	bus.SetIdle()
	if log.last() != "Renamed" {
		t.Fatalf("idle title %q", log.last())
	}
	done := bus.ModalOpened("question")
	if log.last() != tabBlockedGlyph+" Renamed" {
		t.Fatalf("blocked title %q", log.last())
	}
	done()
	// Stopping mid-turn leaves a plain name, not an animation frame.
	bus.SetWorking()
	stop()
	if log.last() != "Renamed" {
		t.Fatalf("after stop %q", log.last())
	}
	n := len(log.seen)
	time.Sleep(2 * tabSpinInterval)
	if len(log.seen) != n {
		t.Error("the spinner kept painting after stop")
	}
}
