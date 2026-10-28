package serve

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/notshekhar/pi/internal/rpc"
)

// The workspace (files, git, terminals) over serve's socket: a client calls
// workspace.<name> as an ordinary request; what the host emits on its own
// arrives as a workspace.event { channel, payload } notification.

const (
	workspacePrefix = "workspace."
	workspaceEvent  = "workspace.event"
)

type workspaceRequest struct {
	id     any
	method string
	params map[string]any
}

func parseWorkspaceRequest(frame []byte) *workspaceRequest {
	// Cheap reject first: nearly every frame is a session call.
	if !bytes.Contains(frame, []byte(`"`+workspacePrefix)) {
		return nil
	}
	var msg struct {
		ID     any            `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.ID == nil || len(msg.Method) <= len(workspacePrefix) ||
		msg.Method[:len(workspacePrefix)] != workspacePrefix {
		return nil
	}
	if msg.Params == nil {
		msg.Params = map[string]any{}
	}
	return &workspaceRequest{id: msg.ID, method: msg.Method, params: msg.Params}
}

type workspace struct {
	rpc      *rpc.Server
	notify   func(channel string, payload any)
	handlers map[string]func(params map[string]any) (any, error)
	closers  []func()
}

func newWorkspace(server *rpc.Server, notify func(string, any)) *workspace {
	w := &workspace{rpc: server, notify: notify, handlers: map[string]func(map[string]any) (any, error){}}
	registerWorkspaceHandlers(w)
	return w
}

func (w *workspace) call(method string, params map[string]any, canUseTerminal bool) (any, error) {
	name := method[len(workspacePrefix):]
	h, ok := w.handlers[name]
	if !ok {
		return nil, fmt.Errorf("Method not found: %s", method)
	}
	if len(name) > 4 && name[:4] == "pty." && !canUseTerminal {
		return nil, fmt.Errorf("The terminal is turned off for other devices on that computer. Turn \"terminal for other devices\" back on in its /settings (or start loop serve without --no-terminal), then reconnect.")
	}
	return h(params)
}

func (w *workspace) dispose() {
	for _, c := range w.closers {
		c()
	}
}

// registerWorkspaceHandlers fills the host table (workspace_handlers.go).
var registerWorkspaceHandlers = func(*workspace) {}
