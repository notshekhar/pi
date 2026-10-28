package rpc

import (
	"errors"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/core/session"
)

// LiveSession is loop's: a session whose turns run OUTSIDE this server — in
// the TUI that started /rc. A client's send is handed to the TUI (which runs
// it as if typed there), and the TUI feeds the turn's events back in
// (PublishLive / SetLiveRunning), so every client and the TUI's own screen
// render the one stream.
type LiveSession interface {
	// Session is the object the TUI appends to; history is read from it.
	Session() *session.Session
	// Send runs input as a chat turn there (queued if one is running).
	Send(input string)
	Cancel()
}

// LiveProvider is the TUI's live sessions by id.
type LiveProvider interface {
	Get(sessionID string) LiveSession
}

func (s *Server) liveGet(id string) LiveSession {
	if s.host.Live == nil || id == "" {
		return nil
	}
	return s.host.Live.Get(id)
}

// liveCtx is the context for a live session, made on first use from the
// TUI's own Session and re-pointed at it if the server had opened the same
// session from the store (that copy would not see the TUI's appends) — only
// when it is a different object, and never mid-turn.
func (s *Server) liveCtx(id string) *activeSession {
	live := s.liveGet(id)
	if live == nil {
		return s.lookup(id)
	}
	sess := live.Session()
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.sessions[id]; c != nil {
		c.mu.Lock()
		if c.session != sess && !c.running {
			c.session = sess
			c.modelID = orStr(sess.LastModel(), sess.Info.Model)
			c.transcript = transcriptOf(sess, false)
			trackSaved(c)
		}
		c.mu.Unlock()
		return c
	}
	c := newActive(sess, orStr(sess.LastModel(), sess.Info.Model))
	c.tracker.SeedFromSession(sess)
	s.sessions[id] = c
	return c
}

var errorBearing = map[string]bool{"error": true, "tool-error": true}

// PublishLive stamps a live turn's event into its session's stream as if
// this server had run the turn.
func (s *Server) PublishLive(id, typ string, data any) {
	c := s.liveCtx(id)
	if c == nil {
		return
	}
	if errorBearing[typ] {
		if err, ok := data.(error); ok {
			data = plainErr(err)
		} else if str, ok := data.(string); ok {
			data = map[string]any{"name": "Error", "message": str}
		}
	}
	c.mu.Lock()
	s.broadcast(id, c, typ, data)
	c.mu.Unlock()
}

// SetLiveRunning is a live session's turn starting or ending.
func (s *Server) SetLiveRunning(id string, running bool) {
	if c := s.liveCtx(id); c != nil {
		c.mu.Lock()
		s.setRunning(id, c, running)
		c.mu.Unlock()
	}
}

// OpenSession is the Session this server holds open for id, if any, so a
// TUI opening the same conversation uses this object rather than a second
// copy that would append to its own branch.
func (s *Server) OpenSession(id string) *session.Session {
	if c := s.lookup(id); c != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.session
	}
	return nil
}

// IsRunning reports a turn this server is running itself.
func (s *Server) IsRunning(id string) bool {
	c := s.lookup(id)
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// CancelOwnTurn stops a turn this server is running itself.
func (s *Server) CancelOwnTurn(id string) {
	if c := s.lookup(id); c != nil {
		c.mu.Lock()
		if c.running && c.cancel != nil {
			c.cancel()
		}
		c.mu.Unlock()
	}
}

type watchTransport struct{ fn func(part map[string]any) }

func (w watchTransport) Send(msg any) {
	if n, ok := msg.(Notification); ok && n.Method == "session.event" {
		if p, ok := n.Params.(map[string]any); ok {
			if part, ok := p["part"].(map[string]any); ok {
				w.fn(part)
			}
		}
	}
}

// Watch is a session's events in order — how the TUI draws a turn a phone
// started. fromTurnStart first replays what the running turn already
// streamed. Returns an unsubscribe.
func (s *Server) Watch(id string, fn func(part map[string]any), fromTurnStart bool) func() {
	c := s.lookup(id)
	if c == nil {
		return func() {}
	}
	t := &watchTransport{fn: fn}
	c.mu.Lock()
	if fromTurnStart && c.running {
		start := -1
		for i := len(c.ring) - 1; i >= 0; i-- {
			p := c.ring[i].part
			if p["type"] == "session-running" {
				if d, ok := p["data"].(map[string]any); ok && d["running"] == true {
					start = i
					break
				}
			}
		}
		for _, e := range c.ring[start+1:] {
			fn(e.part)
		}
	}
	c.subscribers[t] = true
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.subscribers, t)
		c.mu.Unlock()
	}
}

// Attach connects a transport fed by framed messages (a WebSocket): feed
// takes one JSON message; close detaches it everywhere.
func (s *Server) Attach(t Transport) (feed func(msg string), closeFn func()) {
	s.mu.Lock()
	s.transports[t] = true
	s.mu.Unlock()
	feed = func(msg string) {
		if line := strings.TrimSpace(msg); line != "" {
			go s.handleLine(line, t)
		}
	}
	return feed, func() { s.Disconnect(t) }
}

// localOnly refuses an artifact.* call over a network-reachable server.
func localOnly(method string) error {
	return fmt.Errorf("%s is not available over loop serve — artifacts stay on the local machine", method)
}

func plainErr(err error) map[string]any {
	out := map[string]any{"name": "Error", "message": err.Error()}
	if cause := errors.Unwrap(err); cause != nil {
		out["cause"] = plainErr(cause)
	}
	return out
}
