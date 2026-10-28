package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/tools"
	"github.com/notshekhar/pi/internal/core/transcript"
)

// EventRingSize is how many events each session keeps for reconnect replay.
const EventRingSize = 2048

// Transport is one connected client.
type Transport interface{ Send(msg any) }

// AskAnswer is loop's AskAnswer.
type AskAnswer struct {
	Answers  []string `json:"answers"`
	Custom   bool     `json:"custom,omitempty"`
	Declined bool     `json:"declined,omitempty"`
	Note     string   `json:"note,omitempty"`
}

// AskOption / AskQuestion are loop's ask-bridge shapes.
type AskOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type AskQuestion struct {
	Question    string      `json:"question"`
	Header      string      `json:"header"`
	Options     []AskOption `json:"options"`
	MultiSelect bool        `json:"multiSelect,omitempty"`
}

// Host is what the server needs from the program hosting it — the pieces
// that live above core (tools, extensions, MCP, the command registry).
type Host struct {
	// NewRun builds a run for a session on a model, with ask wired to the
	// clients watching it.
	NewRun func(s *session.Session, modelID string, ask tools.Asker) (*agent.Run, error)
	// HasCommand reports a slash command name (agent.save refuses one).
	HasCommand func(name string) bool
	// Reload re-reads prompts, skills, agents and extensions for cwd and
	// returns how many commands there now are.
	Reload func(cwd string) int
	// Extensions lists and toggles extensions.
	Extensions ExtensionHost
	// MCP manages tool servers.
	MCP MCPHost
	// CWD is the process's working directory (defaults for create/report).
	CWD string
	// Version is pi's release, reported by hello.
	Version string
	// Remote marks a network-reachable server (serve): artifact.* refused.
	Remote bool
	// Live is the TUI's sessions under /rc; their turns run there.
	Live LiveProvider
}

type ringEntry struct {
	seq  int64
	part map[string]any
}

// activeSession is a session this server has open (loop's ActiveSession).
type activeSession struct {
	mu          sync.Mutex
	session     *session.Session
	tracker     *agent.CostTracker
	cancel      context.CancelFunc
	modelID     string
	running     bool
	subscribers map[Transport]bool
	seq         int64
	ring        []ringEntry
	savedSeq    int64
	hasSaved    bool
	transcript  *transcript.Transcript
}

// Server is one RPC server.
type Server struct {
	host Host

	mu          sync.Mutex
	sessions    map[string]*activeSession
	transports  map[Transport]bool
	pendingAsks map[string]chan []AskAnswer
	nextAsk     int
	handlers    map[string]handler
}

type handler func(params map[string]any, t Transport) (any, error)

// NewServer builds a server around a host.
func NewServer(host Host) *Server {
	if host.Version == "" {
		host.Version = "dev"
	}
	s := &Server{host: host, sessions: map[string]*activeSession{}, transports: map[Transport]bool{},
		pendingAsks: map[string]chan []AskAnswer{}}
	s.handlers = map[string]handler{}
	for _, group := range []map[string]handler{
		s.sessionHandlers(), s.teamHandlers(), s.agentHandlers(), s.authHandlers(), s.catalogHandlers(),
		s.settingsHandlers(), s.extensionHandlers(), s.datasourceHandlers(), s.mcpHandlers(), s.artifactHandlers(),
	} {
		for name, h := range group {
			s.handlers[name] = h
		}
	}
	return s
}

// lineTransport writes newline-delimited JSON to a writer.
type lineTransport struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lineTransport) Send(msg any) {
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(raw, '\n'))
}

// Serve reads requests from r and writes responses and notifications to w
// until r ends.
func (s *Server) Serve(r io.Reader, w io.Writer) {
	t := &lineTransport{w: w}
	s.mu.Lock()
	s.transports[t] = true
	s.mu.Unlock()
	defer s.Disconnect(t)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	var wg sync.WaitGroup
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handleLine(line, t)
		}()
	}
	wg.Wait()
}

// Disconnect drops a transport everywhere.
func (s *Server) Disconnect(t Transport) {
	s.mu.Lock()
	delete(s.transports, t)
	ctxs := make([]*activeSession, 0, len(s.sessions))
	for _, c := range s.sessions {
		ctxs = append(ctxs, c)
	}
	s.mu.Unlock()
	for _, c := range ctxs {
		c.mu.Lock()
		delete(c.subscribers, t)
		c.mu.Unlock()
	}
}

// Dispose aborts every turn (the process is leaving).
func (s *Server) Dispose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.sessions {
		c.mu.Lock()
		if c.cancel != nil {
			c.cancel()
		}
		c.subscribers = map[Transport]bool{}
		c.mu.Unlock()
	}
}

func (s *Server) handleLine(line string, t Transport) {
	var req Request
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		t.Send(Response{JSONRPC: "2.0", ID: nil, Error: &RPCError{Code: ParseError, Message: "Parse error"}})
		return
	}
	result, err := s.dispatch(req, t)
	if req.ID == nil {
		return
	}
	if err != nil {
		t.Send(Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: InternalError, Message: err.Error()}})
		return
	}
	t.Send(resultResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
}

// resultResponse always carries `result` (null included), as loop's does.
type resultResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result"`
}

// Call runs one method in-process.
func (s *Server) Call(method string, params map[string]any) (any, error) {
	return s.dispatch(Request{JSONRPC: "2.0", ID: 0, Method: method, Params: params}, nopTransport{})
}

type nopTransport struct{}

func (nopTransport) Send(any) {}

func (s *Server) dispatch(req Request, t Transport) (result any, err error) {
	if s.host.Remote && strings.HasPrefix(req.Method, "artifact.") {
		return nil, localOnly(req.Method)
	}
	h, ok := s.handlers[req.Method]
	if !ok {
		return nil, fmt.Errorf("Method not found: %s", req.Method)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	params := req.Params
	if params == nil {
		params = map[string]any{}
	}
	return h(params, t)
}

// SessionStatus is loop's SessionStatusNotice.
type SessionStatus struct {
	SessionID  string `json:"sessionId"`
	Change     string `json:"change"`
	Running    *bool  `json:"running,omitempty"`
	TeamID     string `json:"teamId,omitempty"`
	TeamChange string `json:"teamChange,omitempty"`
}

// announce tells every client a session changed, after the reply that
// caused it.
func (s *Server) announce(n SessionStatus) {
	go func() {
		time.Sleep(time.Millisecond)
		s.mu.Lock()
		ts := make([]Transport, 0, len(s.transports))
		for t := range s.transports {
			ts = append(ts, t)
		}
		s.mu.Unlock()
		msg := Notification{JSONRPC: "2.0", Method: "session.status", Params: n}
		for _, t := range ts {
			t.Send(msg)
		}
	}()
}

// broadcast stamps a seq, remembers the event for replay, folds it into the
// transcript and fans it out. Called with c.mu held.
func (s *Server) broadcast(id string, c *activeSession, typ string, data any) {
	part := map[string]any{"type": typ}
	if data != nil {
		raw, err := json.Marshal(data)
		if err == nil {
			var v any
			if json.Unmarshal(raw, &v) == nil {
				part["data"] = v
			}
		}
	}
	c.seq++
	seq := c.seq
	c.ring = append(c.ring, ringEntry{seq: seq, part: part})
	if len(c.ring) > EventRingSize {
		c.ring = c.ring[1:]
	}
	c.transcript.Apply(transcript.Event{Type: typ, Data: part["data"]}, time.Now().UnixMilli())
	msg := Notification{JSONRPC: "2.0", Method: "session.event", Params: map[string]any{"sessionId": id, "seq": seq, "part": part}}
	for t := range c.subscribers {
		t.Send(msg)
	}
}

// setRunning moves the running flag and tells everyone. Called with c.mu held.
func (s *Server) setRunning(id string, c *activeSession, running bool) {
	if c.running == running {
		return
	}
	c.running = running
	s.broadcast(id, c, "session-running", map[string]any{"running": running})
	r := running
	s.announce(SessionStatus{SessionID: id, Change: "running", Running: &r})
}

func transcriptOf(sess *session.Session, running bool) *transcript.Transcript {
	sess.RLock()
	defer sess.RUnlock()
	branch := sess.CurrentBranch()
	return transcript.FromEntries(branch, running, transcript.LatestTodos(branch))
}

func newActive(sess *session.Session, modelID string) *activeSession {
	c := &activeSession{session: sess, tracker: agent.NewCostTracker(), modelID: modelID,
		subscribers: map[Transport]bool{}, transcript: transcriptOf(sess, false)}
	trackSaved(c)
	return c
}

// trackSaved keeps savedSeq current: what session.history is good to mid-turn.
func trackSaved(c *activeSession) {
	c.session.OnAppend = func() {
		c.savedSeq, c.hasSaved = c.seq, true
	}
}

func (s *Server) lookup(id string) *activeSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

// requireSession is a session this server has open.
func (s *Server) requireSession(id string) (*activeSession, error) {
	if s.liveGet(id) != nil {
		return s.liveCtx(id), nil
	}
	if c := s.lookup(id); c != nil {
		return c, nil
	}
	return nil, fmt.Errorf("Unknown sessionId: %s", id)
}

// ensureCtx opens a session on first touch, reloading a cached copy that
// another process has written to since.
func (s *Server) ensureCtx(id string) (*activeSession, error) {
	// A session the TUI has open is read from the TUI's own object.
	if s.liveGet(id) != nil {
		return s.liveCtx(id), nil
	}
	if c := s.lookup(id); c != nil {
		c.mu.Lock()
		behind := !c.running && c.session.IsBehindDisk()
		c.mu.Unlock()
		if behind {
			if fresh, err := session.Load(id); err == nil {
				c.mu.Lock()
				if !c.running {
					c.session = fresh
					c.modelID = fresh.LastModel()
					c.transcript = transcriptOf(fresh, false)
					trackSaved(c)
				}
				c.mu.Unlock()
				s.announce(SessionStatus{SessionID: id, Change: "updated"})
			}
		}
		return c, nil
	}
	sess, err := session.Load(id)
	if err != nil {
		return nil, err
	}
	c := newActive(sess, sess.LastModel())
	// Seeded, so cost.session reports what the conversation actually cost.
	c.tracker.SeedFromSession(sess)
	s.mu.Lock()
	if existing := s.sessions[id]; existing != nil {
		s.mu.Unlock()
		return existing, nil
	}
	s.sessions[id] = c
	s.mu.Unlock()
	return c, nil
}

// askOverRPC puts questions to the clients watching a session; with nobody
// there, or on abort, every question is declined.
func (s *Server) askOverRPC(ctx context.Context, id string, questions []AskQuestion) []AskAnswer {
	declined := func() []AskAnswer {
		out := make([]AskAnswer, len(questions))
		for i := range out {
			out[i] = AskAnswer{Answers: []string{}, Declined: true}
		}
		return out
	}
	c := s.lookup(id)
	if c == nil {
		return declined()
	}
	c.mu.Lock()
	if len(c.subscribers) == 0 {
		c.mu.Unlock()
		return declined()
	}
	s.mu.Lock()
	s.nextAsk++
	askID := fmt.Sprintf("ask-%d", s.nextAsk)
	ch := make(chan []AskAnswer, 1)
	s.pendingAsks[askID] = ch
	s.mu.Unlock()
	s.broadcast(id, c, "ask", map[string]any{"askId": askID, "questions": questions})
	c.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pendingAsks, askID)
		s.mu.Unlock()
	}()
	select {
	case answers := <-ch:
		return answers
	case <-ctx.Done():
		return declined()
	}
}

// startTurn runs a turn this server owns; its events stream to subscribers.
func (s *Server) startTurn(id string, c *activeSession, input, modelID, thinking, agentName string) error {
	run, err := s.host.NewRun(c.session, modelID, func(ctx context.Context, question string, options []string) string {
		q := AskQuestion{Question: question, Options: make([]AskOption, 0, len(options))}
		for _, o := range options {
			q.Options = append(q.Options, AskOption{Label: o})
		}
		answers := s.askOverRPC(ctx, id, []AskQuestion{q})
		if len(answers) == 0 || answers[0].Declined || len(answers[0].Answers) == 0 {
			return ""
		}
		return answers[0].Answers[0]
	})
	if err != nil {
		return err
	}
	if thinking != "" {
		run.Config.Reasoning = thinkingEffort(thinking)
	}
	if agentName != "" {
		if err := applyAgent(run, agentName); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	s.setRunning(id, c, true)
	tracker := c.tracker
	go func() {
		defer cancel()
		emit := func(event string, data any) {
			c.mu.Lock()
			s.broadcast(id, c, event, data)
			c.mu.Unlock()
		}
		_, err := run.RunTurn(ctx, agent.TurnOptions{Input: input, Tracker: tracker, Emit: emit})
		c.mu.Lock()
		defer c.mu.Unlock()
		if err != nil && !errors.Is(err, context.Canceled) && c.transcript != nil {
			// RunTurn already emitted the error; nothing more to say.
		}
		s.setRunning(id, c, false)
	}()
	return nil
}
