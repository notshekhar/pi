package rpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/config"
)

// The MCP OAuth login as a pollable flow (loop's rpc/mcp-flows.ts): start
// returns a flow id at once, the browser URL arrives as an event, and the
// client polls until the login ends.

type mcpLoginEvent struct {
	Type    string `json:"type"` // auth | progress | done | error
	URL     string `json:"url,omitempty"`
	Message string `json:"message,omitempty"`
}

type mcpLogin struct {
	id, server string
	events     []mcpLoginEvent
	status     string // running | done | error | cancelled
	finishedAt time.Time
	cancel     context.CancelFunc
}

var (
	loginsMu sync.Mutex
	logins   = map[string]*mcpLogin{}
)

// loginRetention keeps a finished login briefly, so a slow poll still sees
// how it ended.
const loginRetention = time.Minute

func sweepLoginsLocked() {
	for id, l := range logins {
		if !l.finishedAt.IsZero() && time.Since(l.finishedAt) > loginRetention {
			delete(logins, id)
		}
	}
}

func finishLogin(l *mcpLogin, status string, ev mcpLoginEvent) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	if l.status != "running" {
		return
	}
	l.status, l.finishedAt = status, time.Now()
	l.events = append(l.events, ev)
}

func (s *Server) startMCPLogin(name, cwd string) (any, error) {
	if name == "" {
		return nil, errors.New("server name required")
	}
	if _, ok := config.LoadMCPServers(cwd)[name]; !ok {
		return nil, fmt.Errorf("unknown MCP server: %s (check the name)", name)
	}
	if s.host.MCP == nil {
		return nil, errors.New("MCP is not available in this process")
	}
	loginsMu.Lock()
	sweepLoginsLocked()
	// A second login for the same server would race the first over the same
	// callback port and the same token file.
	for _, l := range logins {
		if l.server == name && l.status == "running" {
			loginsMu.Unlock()
			return map[string]any{"flowId": l.id, "server": name}, nil
		}
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	ctx, cancel := context.WithCancel(context.Background())
	l := &mcpLogin{id: hex.EncodeToString(b), server: name, status: "running", cancel: cancel,
		events: []mcpLoginEvent{{Type: "progress", Message: "Authorizing " + name + "…"}}}
	logins[l.id] = l
	loginsMu.Unlock()

	go func() {
		defer cancel()
		tools, err := s.host.MCP.Login(ctx, cwd, name, func(url string) {
			loginsMu.Lock()
			l.events = append(l.events, mcpLoginEvent{Type: "auth", URL: url})
			loginsMu.Unlock()
		})
		if err != nil {
			finishLogin(l, "error", mcpLoginEvent{Type: "error", Message: err.Error()})
			return
		}
		finishLogin(l, "done", mcpLoginEvent{Type: "done", Message: fmt.Sprintf("%s authorized — %d tools.", name, tools)})
	}()
	return map[string]any{"flowId": l.id, "server": name}, nil
}

func pollMCPLogin(id string, cursor int) (any, error) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	l, ok := logins[id]
	if !ok {
		return nil, fmt.Errorf("unknown MCP login: %s", id)
	}
	from := max(0, min(cursor, len(l.events)))
	return map[string]any{"status": l.status, "cursor": len(l.events), "events": append([]mcpLoginEvent{}, l.events[from:]...)}, nil
}

func cancelMCPLogin(id string) {
	loginsMu.Lock()
	l, ok := logins[id]
	loginsMu.Unlock()
	if !ok {
		return
	}
	finishLogin(l, "cancelled", mcpLoginEvent{Type: "error", Message: "Authorization cancelled."})
	l.cancel()
}
