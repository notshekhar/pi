package main

import (
	"strings"

	"github.com/notshekhar/pi/internal/tui"
)

// The input queue, ported from loop's turn-runner.
//
// While a turn runs, EVERY input queues, FIFO: chat, /commands and !shell
// alike. Commands mutate session and model state and would race the running
// turn, so they wait their turn rather than preempting it. The queue drains
// one item at a time, after each item finishes, whatever its type.
//
// The exception is the commands that LEAVE this session: they never touch the
// running turn, so making them wait would be waiting for nothing.

// leavingCommands run immediately even mid-turn.
var leavingCommands = map[string]bool{"new": true, "clear": true, "resume": true, "sessions": true}

// leavesSession reports whether a line is one of the leaving commands.
func leavesSession(line string) bool {
	if !strings.HasPrefix(line, "/") {
		return false
	}
	name, _, _ := strings.Cut(strings.TrimPrefix(expandAlias(line), "/"), " ")
	return leavingCommands[strings.TrimSpace(name)] && lookupCommand("/"+name) != nil
}

// enqueue queues line if a turn is running. It reports whether it did.
func (t *repl) enqueue(line string) bool {
	if leavesSession(line) {
		return false
	}
	t.mu.Lock()
	if !t.turning {
		t.mu.Unlock()
		return false
	}
	t.queue = append(t.queue, line)
	snapshot := append([]string{}, t.queue...)
	t.mu.Unlock()
	t.do(func() { t.app.SetQueue(snapshot) })
	return true
}

// drainNext resubmits the oldest queued input, if nothing is running. Called
// after every item finishes, so the queue keeps draining.
//
// The item goes back through the app's event channel rather than being run
// here, so it is handled on the pump goroutine like anything typed — and so
// the busy check there sees whatever the previous item left running.
func (t *repl) drainNext() {
	t.mu.Lock()
	if t.turning || len(t.queue) == 0 {
		t.mu.Unlock()
		return
	}
	next := t.queue[0]
	t.queue = t.queue[1:]
	snapshot := append([]string{}, t.queue...)
	t.mu.Unlock()
	t.do(func() { t.app.SetQueue(snapshot) })
	go func() { t.app.Events <- lineEvent(next) }()
}

// clearQueue drops everything queued (ctrl+c mid-turn).
func (t *repl) clearQueue() {
	t.mu.Lock()
	t.queue = nil
	t.mu.Unlock()
	t.do(func() { t.app.SetQueue(nil) })
}

// lineEvent is the event the composer would have sent for line.
func lineEvent(line string) tui.Event {
	switch {
	case strings.HasPrefix(line, "!"):
		return tui.Event{Kind: tui.EvBash, Text: line[1:]}
	case strings.HasPrefix(line, "/"):
		return tui.Event{Kind: tui.EvSlash, Text: line}
	default:
		return tui.Event{Kind: tui.EvSubmit, Text: line}
	}
}
