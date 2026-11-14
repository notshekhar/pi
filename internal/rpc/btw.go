package rpc

import (
	"errors"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/core/session"
)

// turnChoices is the per-turn choices a client may send with a prompt
// (loop's turnChoices). Thinking: anything unknown falls back to the
// thinkingLevel setting. Agent: the RPC form of `/<agent> <message>` — this
// turn runs under that agent's prompt and the session is untouched. An
// unknown name is refused rather than silently falling back to the default
// persona, which would look like the agent had simply ignored its
// instructions.
func turnChoices(p map[string]any) (turnRequest, error) {
	thinking := pstr(p, "thinking")
	switch thinking {
	case "off", "minimal", "low", "medium", "high", "xhigh":
	default:
		thinking = ""
	}
	agentName := strings.TrimSpace(pstr(p, "agent"))
	if agentName != "" && !agentExists(agentName) {
		return turnRequest{}, fmt.Errorf("Unknown agent: %s", agentName)
	}
	return turnRequest{thinking: thinking, agent: agentName}, nil
}

// btwField is a session-list row's `btw`: the session a side thread was
// asked from, so a client can offer the way back. Nil for any other row.
func btwField(row session.Row) map[string]any {
	name := ""
	if row.Name != nil {
		name = *row.Name
	}
	parentID, ok := session.BtwParentOf(name, row.ParentSession)
	if !ok {
		return nil
	}
	return map[string]any{"parentId": parentID}
}

// askBtw is session.btw: the question runs in a fresh session linked to
// this one, which is never touched — so it works while a turn is running
// there. The reply
// streams on the NEW session, and the asker is subscribed to it, as
// session.create's caller is.
func (s *Server) askBtw(p map[string]any, t Transport) (any, error) {
	question := strings.TrimSpace(pstr(p, "question"))
	if question == "" {
		return nil, errors.New("session.btw needs a question")
	}
	req, err := turnChoices(p)
	if err != nil {
		return nil, err
	}
	source, err := s.ensureCtx(pstr(p, "sessionId"))
	if err != nil {
		return nil, err
	}
	source.mu.Lock()
	src, sourceModel := source.session, source.modelID
	source.mu.Unlock()
	thread, meta, err := session.CreateBtwThread(src, question)
	if err != nil {
		return nil, err
	}
	req.input, req.modelID, req.btw = question, orStr(pstr(p, "model"), sourceModel), &meta
	c := newActive(thread, req.modelID)
	c.tracker.SeedFromSession(thread)
	c.subscribers[t] = true
	s.mu.Lock()
	s.sessions[thread.ID] = c
	s.mu.Unlock()
	s.announce(SessionStatus{SessionID: thread.ID, Change: "created"})
	c.mu.Lock()
	err = s.startTurn(thread.ID, c, req)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return map[string]any{"sessionId": thread.ID, "cwd": thread.Info.CWD, "name": thread.Name()}, nil
}
