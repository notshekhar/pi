// Package herdr mirrors the agent-status bus onto herdr's socket API when
// pi-agent is running inside a herdr pane (https://herdr.dev).
//
// herdr is an agent multiplexer whose sidebar shows every pane as working,
// blocked or idle. Without a report it can only see this process as a
// terminal producing bytes, which is exactly the thing that does not
// distinguish "thinking" from "waiting for you" — the distinction the sidebar
// exists to show. This speaks pane.report_agent / pane.report_agent_session /
// pane.release_agent as a "custom" source, which is the documented path for
// an agent herdr has no built-in manifest for.
//
// Two properties matter more than the protocol:
//
//   - It is HARD-GATED on the environment herdr injects into every pane.
//     Outside a pane nothing subscribes and no socket is ever opened.
//   - It is fire-and-forget with a hard timeout, on a queue, off the caller's
//     goroutine. A herdr server that is dead, wedged or mid-restart must
//     never add a millisecond of latency to the TUI, and must never leave the
//     app holding a broken socket. A report that cannot be delivered is
//     dropped; the next transition supersedes it anyway.
package herdr

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"os"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/modules/core/status"
)

const (
	source = "custom:pi-agent"
	agent  = "pi-agent"
)

// Session is the conversation identity carried on every report, so herdr can
// link a pane to the session file it is writing.
type Session struct {
	ID   string
	Path string
}

// Options configure the reporter.
type Options struct {
	// Session is read at SEND time, never cached: the id changes on /new,
	// /resume and fork, and a cached one would report the pane as still
	// being the session it was launched with.
	Session func() Session
	// Disabled is the `herdr` setting turned off.
	Disabled bool
	// Env defaults to os.Getenv.
	Env func(string) string
	// Dial defaults to a unix-socket connection with Timeout. A test seam:
	// the interesting behaviour here is what happens when the far end is
	// slow, gone, or restarted.
	Dial    func(socket string) (net.Conn, error)
	Timeout time.Duration
}

// Reporter is the live subscription. The zero value is inert, which is what
// Attach returns outside a herdr pane.
type Reporter struct {
	active  bool
	paneID  string
	socket  string
	timeout time.Duration
	dial    func(socket string) (net.Conn, error)
	session func() Session

	queue chan map[string]any
	done  chan struct{}

	mu        sync.Mutex
	seq       int64
	announced string
	released  bool
}

// Attach subscribes to the bus and returns the reporter. It never fails: a
// pane that is not a herdr pane simply gets an inert one.
func Attach(bus *status.Bus, opts Options) *Reporter {
	env := opts.Env
	if env == nil {
		env = os.Getenv
	}
	socket, paneID := env("HERDR_SOCKET_PATH"), env("HERDR_PANE_ID")
	if opts.Disabled || env("HERDR_ENV") != "1" || socket == "" || paneID == "" {
		return &Reporter{}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}
	dial := opts.Dial
	if dial == nil {
		dial = func(path string) (net.Conn, error) {
			return net.DialTimeout("unix", path, timeout)
		}
	}
	r := &Reporter{
		active: true, paneID: paneID, socket: socket, timeout: timeout,
		dial: dial, session: opts.Session,
		// Deep enough that a burst of transitions never reaches the drop
		// path, shallow enough that a wedged server cannot make this a
		// memory leak.
		queue: make(chan map[string]any, 64),
		done:  make(chan struct{}),
		seq:   time.Now().UnixMilli() * 1000,
	}
	go r.drain()

	bus.On(r.report)
	// Announce at once, so the pane reads "pi-agent · idle" from launch
	// rather than staying unlabelled until the first turn.
	r.report(bus.Current())
	return r
}

// Active reports whether this process is inside a herdr pane with reporting
// on.
func (r *Reporter) Active() bool { return r.active }

// report is the bus listener. It must return promptly — the bus holds its
// lock across listeners — so it only enqueues.
func (r *Reporter) report(e status.Event) {
	if !r.active {
		return
	}
	r.mu.Lock()
	if r.released {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	r.announceSession()
	params := r.params()
	params["state"] = string(e.State)
	if e.State == status.Blocked && e.Label != "" {
		params["message"] = e.Label
	}
	r.enqueue("pane.report_agent", params)
}

// announceSession re-announces identity when the session id changes, which
// covers create, /new, /resume and fork through the one getter.
func (r *Reporter) announceSession() {
	if r.session == nil {
		return
	}
	s := r.session()
	if s.ID == "" {
		return
	}
	r.mu.Lock()
	changed := s.ID != r.announced
	if changed {
		r.announced = s.ID
	}
	r.mu.Unlock()
	if changed {
		r.enqueue("pane.report_agent_session", r.params())
	}
}

// Release hands the pane back to herdr's own detection on exit, and waits a
// bounded time for the queue to clear.
//
// Bounded because this runs on the way out: a herdr server that has already
// gone away must not be able to hold the process open.
func (r *Reporter) Release() {
	if !r.active {
		return
	}
	r.mu.Lock()
	if r.released {
		r.mu.Unlock()
		return
	}
	r.released = true
	r.mu.Unlock()

	// Through the queue, so it lands after any report still in flight.
	r.enqueue("pane.release_agent", r.params())
	close(r.queue)
	select {
	case <-r.done:
	case <-time.After(2 * r.timeout):
	}
}

// params builds the fields every request carries.
func (r *Reporter) params() map[string]any {
	r.mu.Lock()
	r.seq++
	seq := r.seq
	r.mu.Unlock()

	p := map[string]any{
		"pane_id": r.paneID,
		"source":  source,
		"agent":   agent,
		"seq":     seq,
	}
	if r.session != nil {
		if s := r.session(); s.ID != "" {
			p["agent_session_id"] = s.ID
			if s.Path != "" {
				p["agent_session_path"] = s.Path
			}
		}
	}
	return p
}

func (r *Reporter) enqueue(method string, params map[string]any) {
	req := map[string]any{
		"id":     fmt.Sprintf("%s:%s:%d:%d", source, method, time.Now().UnixNano(), rand.Int63()),
		"method": method,
		"params": params,
	}
	select {
	case r.queue <- req:
	default:
		// The server is not keeping up. Dropping is correct: these are
		// snapshots of a state that has already moved on, and blocking here
		// would block whoever published the transition.
	}
}

// drain sends one request at a time, in order. Order is the point: two state
// reports that overtake each other leave the sidebar showing the older one.
func (r *Reporter) drain() {
	defer close(r.done)
	for req := range r.queue {
		r.send(req)
	}
}

// send is one connection per request: connect, write a line, wait briefly for
// the ack, close. No persistent connection, so a herdr restart costs one
// dropped report instead of a dead socket and a reconnect state machine.
func (r *Reporter) send(req map[string]any) {
	line, err := json.Marshal(req)
	if err != nil {
		return
	}
	conn, err := r.dial(r.socket)
	if err != nil {
		return
	}
	defer conn.Close()
	deadline := time.Now().Add(r.timeout)
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return
	}
	// The ack is read but not parsed: nothing here acts on the answer, and
	// reading it is only how the write is known to have been taken.
	buf := make([]byte, 256)
	_, _ = conn.Read(buf)
}
