// Package mcp speaks the Model Context Protocol, so tools living in another
// process can be offered to the model alongside the built-in ones.
//
// MCP is JSON-RPC 2.0 over a transport — a subprocess's stdio, or HTTP. This
// package implements the client half of the handshake, tool discovery, and
// tool invocation; it does not implement prompts, resources, or sampling,
// because a coding agent has its own answers to all three.
//
// The bridge into the agent (see bridge.go) is the part worth being careful
// about: a discovered tool is a tool like any other, which means it goes
// through the SAME permissions layer. A server that could bypass the policy
// would make the policy decorative.
package mcp

import (
	"encoding/json"
	"fmt"
)

// ProtocolVersion is the revision this client speaks. Servers that support a
// newer one are expected to accept it or negotiate down.
const ProtocolVersion = "2024-11-05"

// request is an outgoing JSON-RPC call.
type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// notification is an outgoing message with no reply.
type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// response is an incoming JSON-RPC reply.
//
// ID is a pointer so a notification from the server — which has none — is
// distinguishable from a reply to request zero.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	Method  string          `json:"method,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("%s (code %d): %s", e.Message, e.Code, e.Data)
	}
	return fmt.Sprintf("%s (code %d)", e.Message, e.Code)
}

// initializeParams opens the session.
type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      clientInfo     `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult is what the server reports about itself.
type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      ServerInfo     `json:"serverInfo"`
	Instructions    string         `json:"instructions,omitempty"`
}

// ServerInfo names the server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolDef is a tool the server offers.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type listToolsResult struct {
	Tools      []ToolDef `json:"tools"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// CallToolResult is a tool's output.
type CallToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Content is one piece of a result. Only text is rendered; other kinds are
// reported by type so the model knows something came back that this client
// cannot show it, rather than seeing an empty result.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// Text flattens a result's content into what the model sees.
func (r CallToolResult) Text() string {
	var parts []string
	for _, c := range r.Content {
		switch c.Type {
		case "text":
			parts = append(parts, c.Text)
		case "":
			// A malformed part with no type; its text is still worth having.
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		default:
			kind := c.Type
			if c.MimeType != "" {
				kind += " (" + c.MimeType + ")"
			}
			parts = append(parts, "["+kind+" omitted]")
		}
	}
	return join(parts, "\n")
}

func join(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}
