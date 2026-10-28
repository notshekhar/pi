// Package rpc is loop's RPC server (packages/core/src/rpc): JSON-RPC 2.0, one
// message per line, over stdio (`pi rpc`, which the desktop app spawns) or a
// unix socket. Method names, params, results and the `session.event` stream
// are loop's, so loop's desktop, web and mobile clients drive pi unchanged.
package rpc

import "fmt"

// Request is a JSON-RPC request or notification.
type Request struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id,omitempty"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

// Response answers a request.
type Response struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
}

// Notification is a server push.
type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// RPCError is a JSON-RPC error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error codes.
const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
	InternalError  = -32603
)

// ProtocolVersion is loop's client↔host protocol (rpc/protocol.ts).
var ProtocolVersion = [2]int{1, 3}

// MinClientProtocol is the oldest client still served.
var MinClientProtocol = [2]int{1, 0}

// ProtocolCapabilities is what this host offers.
var ProtocolCapabilities = []string{"sessions", "status", "paging", "live-sessions", "pairing", "history-tail", "transcript", "teams"}

func older(a, b [2]int) bool { return a[0] < b[0] || (a[0] == b[0] && a[1] < b[1]) }

func formatProtocol(v [2]int) string { return fmt.Sprintf("%d.%d", v[0], v[1]) }

// negotiate is loop's negotiateProtocol.
func negotiate(client [2]int) ([2]int, error) {
	host := ProtocolVersion
	if client[0] != host[0] {
		if client[0] < host[0] {
			return client, fmt.Errorf("This loop host speaks protocol %s; this app speaks %s. Update the app.", formatProtocol(host), formatProtocol(client))
		}
		return client, fmt.Errorf("This loop host speaks protocol %s; this app speaks %s. Update loop on the host (`loop update`).", formatProtocol(host), formatProtocol(client))
	}
	if older(client, MinClientProtocol) {
		return client, fmt.Errorf("This loop host needs protocol %s or newer; this app speaks %s. Update the app.", formatProtocol(MinClientProtocol), formatProtocol(client))
	}
	return [2]int{client[0], min(client[1], host[1])}, nil
}

// parseProtocol reads a [major, minor] off the wire.
func parseProtocol(v any) ([2]int, bool) {
	arr, ok := v.([]any)
	if !ok || len(arr) != 2 {
		return [2]int{}, false
	}
	var out [2]int
	for i, x := range arr {
		f, ok := x.(float64)
		if !ok || f < 0 || f != float64(int(f)) {
			return [2]int{}, false
		}
		out[i] = int(f)
	}
	return out, true
}

// Methods is every method dispatch handles (server.info), loop's RPC_METHODS.
var Methods = []string{
	"server.info", "hello",
	"session.create", "session.list", "session.projects", "session.history", "session.messages", "session.open",
	"session.send", "session.cancel", "session.compact", "session.rename", "session.attach", "session.detach",
	"session.delete", "session.answer", "session.tree", "session.branch", "session.fork", "session.archive",
	"team.get", "team.stop",
	"agent.list", "agent.get", "agent.save", "agent.delete", "agent.tools",
	"auth.status", "auth.providers", "auth.login", "auth.logout", "auth.flow.start", "auth.flow.poll",
	"auth.flow.answer", "auth.flow.cancel", "auth.custom.list", "auth.custom.discover", "auth.custom.save",
	"auth.custom.remove", "auth.custom.setActive",
	"catalog.list", "cost.session", "cost.lifetime", "cost.stats", "usage.steak",
	"settings.list", "settings.set", "config.reload", "context.report",
	"extension.list", "extension.setEnabled",
	"mcp.list", "mcp.add", "mcp.remove", "mcp.setEnabled", "mcp.reconnect", "mcp.login.start", "mcp.login.poll", "mcp.login.cancel",
	"datasource.list", "datasource.save", "datasource.remove", "datasource.test",
	"git.commitMessage",
	"artifact.list", "artifact.get", "artifact.read", "artifact.export", "artifact.delete",
}
