package tui

import (
	"os"
	"strings"
	"sync"
)

// The terminal's tab title, OSC 0 — the tab's name in every terminal worth
// the word, cmux included (it renders it on the pane's sidebar card).
//
// Written by the render loop, between frames, never from the caller's
// goroutine: a title written while a frame is half out would land inside one
// of its escape sequences. Only the latest title matters, so a spinner
// ticking faster than frames go out costs nothing.

type tabTitle struct {
	mu      sync.Mutex
	pending *string
}

// SetTitle names the terminal tab. Safe from any goroutine.
func (a *App) SetTitle(title string) {
	a.title.mu.Lock()
	a.title.pending = &title
	a.title.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// flushTitle writes the newest title, if one is owed.
func (a *App) flushTitle() {
	a.title.mu.Lock()
	t := a.title.pending
	a.title.pending = nil
	a.title.mu.Unlock()
	if t != nil {
		os.Stdout.WriteString(titleSequence(*t))
	}
}

// titleSequence is OSC 0 with the title made safe to carry: a control byte
// inside it would end the sequence early and print the rest as text.
func titleSequence(title string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, title)
	return "\x1b]0;" + clean + "\x07"
}
