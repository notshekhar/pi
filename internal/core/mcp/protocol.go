// Package mcp speaks the Model Context Protocol, so tools living in another
// process — or behind a URL — are offered to the model alongside the
// built-in ones. It is loop's mcp/ (on @ai-sdk/mcp) in Go: the same protocol
// eras, transports, OAuth flow and token file, the same tool naming, and the
// same connection manager (health probes, reconnect backoff, list_changed).
//
// A bridged tool is a tool like any other, which means it goes through the
// SAME permissions layer. A server that could bypass the policy would make
// the policy decorative.
package mcp

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Protocol revisions, as @ai-sdk/mcp names them.
//
// The modern era (LatestProtocolVersion) is stateless: no initialize
// handshake and no session — the client asks `server/discover` once and then
// stamps its version, capabilities and identity onto every request's _meta.
// Everything older is the legacy era: initialize, then
// notifications/initialized, then a session.
const (
	LatestProtocolVersion       = "2026-07-28"
	LatestLegacyProtocolVersion = "2025-11-25"
)

// SupportedProtocolVersions are the revisions this client accepts from a
// server, newest first.
var SupportedProtocolVersions = []string{
	LatestProtocolVersion,
	LatestLegacyProtocolVersion,
	"2025-06-18",
	"2025-03-26",
	"2024-11-05",
}

func supportedVersion(v string) bool { return slices.Contains(SupportedProtocolVersions, v) }

// modernProtocolErrorCodes are the errors a modern server answers
// server/discover with when it understood the request and refused it — not
// a reason to fall back to the legacy handshake.
var modernProtocolErrorCodes = []int{-32020, -32021, -32022}

// discoverTimeout bounds the server/discover probe: a legacy server that
// never answers an unknown method must not cost a slow connect.
const discoverTimeoutMs = 1_000

// Meta keys of the modern era.
const (
	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaClientInfo         = "io.modelcontextprotocol/clientInfo"
	metaServerInfo         = "io.modelcontextprotocol/serverInfo"
)

// message is any JSON-RPC 2.0 message: a request (method + id), a
// notification (method, no id) or a response (id + result or error).
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func (m *message) isRequest() bool      { return m.Method != "" && len(m.ID) > 0 }
func (m *message) isNotification() bool { return m.Method != "" && len(m.ID) == 0 }
func (m *message) isResponse() bool     { return m.Method == "" && len(m.ID) > 0 }

// RPCError is a JSON-RPC error object, returned as a Go error.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if len(e.Data) > 0 && string(e.Data) != "null" {
		return fmt.Sprintf("%s (code %d): %s", e.Message, e.Code, e.Data)
	}
	return fmt.Sprintf("%s (code %d)", e.Message, e.Code)
}

const codeMethodNotFound = -32601

// ServerInfo names the server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Title   string `json:"title,omitempty"`
}

// Capabilities are what a server declared. Only the parts this client acts
// on are typed; the rest is kept raw.
type Capabilities struct {
	Tools       *listChangedCap `json:"tools,omitempty"`
	Resources   *listChangedCap `json:"resources,omitempty"`
	Prompts     *listChangedCap `json:"prompts,omitempty"`
	Logging     json.RawMessage `json:"logging,omitempty"`
	Completions json.RawMessage `json:"completions,omitempty"`
}

type listChangedCap struct {
	ListChanged bool `json:"listChanged,omitempty"`
	Subscribe   bool `json:"subscribe,omitempty"`
}

type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
	Instructions    string       `json:"instructions,omitempty"`
}

type discoverResult struct {
	SupportedVersions []string                   `json:"supportedVersions"`
	Capabilities      Capabilities               `json:"capabilities"`
	Instructions      string                     `json:"instructions,omitempty"`
	Meta              map[string]json.RawMessage `json:"_meta,omitempty"`
}

// ToolDef is a tool the server offers.
type ToolDef struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type listToolsResult struct {
	Tools      []ToolDef `json:"tools"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

// CallToolResult is a tool's output.
type CallToolResult struct {
	Content           []Content       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

// Content is one piece of a tool result.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"` // base64, for image and audio
	MimeType string `json:"mimeType,omitempty"`
	// resource_link
	URI  string `json:"uri,omitempty"`
	Name string `json:"name,omitempty"`
	// resource (embedded)
	Resource *embeddedResource `json:"resource,omitempty"`
}

type embeddedResource struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}
