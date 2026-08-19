// Package status is the pane-level answer to "what is the agent doing right
// now": working, blocked on the user, or idle.
//
// One source of truth, fed by the two seams that already know — the working
// indicator and the agent's own blocking prompts — and subscribed to by
// whoever needs semantic state rather than a terminal's byte stream: a
// multiplexer's sidebar, a notifier, a window title. Without it, everything
// watching a pane can see is a process that is producing output, which says
// nothing about whether it is thinking or waiting for an answer.
package status

import (
	"sync"
	"time"
)

// State is what the agent is doing.
type State string

const (
	Idle    State = "idle"
	Working State = "working"
	Blocked State = "blocked"
)

// rank orders the states by urgency. Transitions UP publish immediately;
// transitions down settle first. See publish.
var rank = map[State]int{Idle: 0, Working: 1, Blocked: 2}

// Event is a published transition.
type Event struct {
	State State
	// Label is what the agent is blocked on ("question", "bash approval"),
	// empty for the other states.
	Label string
}

// Listener receives every published transition.
type Listener func(Event)

// Bus holds the current state and publishes transitions.
type Bus struct {
	mu         sync.Mutex
	settle     time.Duration
	working    bool
	modalDepth int
	modalLabel string
	published  Event
	timer      *time.Timer
	listeners  []Listener
}

// DefaultSettle is how long a calmer state waits before it is published.
const DefaultSettle = 250 * time.Millisecond

// New returns a bus that starts idle. settle of 0 publishes everything
// immediately, which is what tests want and nothing else does.
func New(settle time.Duration) *Bus {
	return &Bus{settle: settle, published: Event{State: Idle}}
}

// SetWorking marks the start of a working stretch.
func (b *Bus) SetWorking() { b.set(func() { b.working = true }) }

// SetIdle marks the end of one.
func (b *Bus) SetIdle() { b.set(func() { b.working = false }) }

// ModalOpened reports an agent-driven prompt and returns its closer. Prompts
// nest: an ask flow that opens one question after another is one continuous
// stretch of blocked, not a blink per question.
//
// The closer is idempotent, because the natural way to call it is a defer on
// a path that may also close explicitly.
func (b *Bus) ModalOpened(label string) func() {
	b.set(func() {
		b.modalDepth++
		b.modalLabel = label
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			b.set(func() {
				if b.modalDepth > 0 {
					b.modalDepth--
				}
				if b.modalDepth == 0 {
					b.modalLabel = ""
				}
			})
		})
	}
}

// Current is the last published state.
func (b *Bus) Current() Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.published
}

// On subscribes. Listeners are called off the caller's goroutine only in the
// settled case, so a listener must not block for long.
func (b *Bus) On(l Listener) {
	b.mu.Lock()
	b.listeners = append(b.listeners, l)
	b.mu.Unlock()
}

func (b *Bus) set(mutate func()) {
	b.mu.Lock()
	mutate()
	b.publish()
	b.mu.Unlock()
}

// desired is the state the inputs currently imply. Caller holds the lock.
func (b *Bus) desired() Event {
	switch {
	case b.modalDepth > 0:
		return Event{State: Blocked, Label: b.modalLabel}
	case b.working:
		return Event{State: Working}
	default:
		return Event{State: Idle}
	}
}

// publish decides whether the new state goes out now or waits. Caller holds
// the lock.
//
// Upward transitions are immediate: a question that has appeared on screen is
// news, and news held for a quarter of a second reads as a laggy pane.
// Downward ones settle, because the seams flicker naturally — back-to-back
// queued turns, an ask flow reopening a prompt per question — and every one
// of those blips would otherwise be published as "idle" and then taken back.
func (b *Bus) publish() {
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	next := b.desired()
	if rank[next.State] >= rank[b.published.State] || b.settle <= 0 {
		b.emit(next)
		return
	}
	b.timer = time.AfterFunc(b.settle, func() {
		b.mu.Lock()
		b.timer = nil
		settled := b.desired()
		b.emit(settled)
		b.mu.Unlock()
	})
}

// emit publishes a state if it is actually new. Caller holds the lock.
//
// The listeners are called under it, which is deliberate: it keeps the
// published state and the notification in the same order for every
// subscriber. A listener that does real work must hand it to its own
// goroutine — the herdr reporter queues and returns.
func (b *Bus) emit(e Event) {
	if e.State == b.published.State && e.Label == b.published.Label {
		return
	}
	b.published = e
	for _, l := range b.listeners {
		l(e)
	}
}
