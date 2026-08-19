package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"
)

// Client is a connection to one MCP server.
type Client struct {
	Name      string
	transport Transport
	info      ServerInfo
	nextID    atomic.Int64
}

// ServerConfig describes one server. Exactly one of Command or URL is set.
type ServerConfig struct {
	Name    string            `json:"-"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     []string          `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Disabled keeps a server configured but unused, which is friendlier than
	// deleting and retyping it.
	Disabled bool `json:"disabled,omitempty"`
}

// handshakeTimeout bounds a server that starts but never answers. Without it
// a single broken entry hangs startup forever.
const handshakeTimeout = 20 * time.Second

// Connect starts a server and completes the handshake.
func Connect(ctx context.Context, cfg ServerConfig, cwd string) (*Client, error) {
	var transport Transport
	switch {
	case cfg.Command != "":
		t, err := newStdioTransport(StdioConfig{
			Command: cfg.Command, Args: cfg.Args, Env: cfg.Env, CWD: cwd,
		})
		if err != nil {
			return nil, fmt.Errorf("mcp %s: %w", cfg.Name, err)
		}
		transport = t
	case cfg.URL != "":
		transport = newHTTPTransport(HTTPConfig{URL: cfg.URL, Headers: cfg.Headers})
	default:
		return nil, fmt.Errorf("mcp %s: needs a command or a url", cfg.Name)
	}

	c := &Client{Name: cfg.Name, transport: transport}

	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	var result InitializeResult
	if err := c.call(ctx, "initialize", initializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    map[string]any{},
		ClientInfo:      clientInfo{Name: "pi-agent", Version: "0.1.3"},
	}, &result); err != nil {
		transport.Close()
		return nil, fmt.Errorf("mcp %s: handshake: %w", cfg.Name, err)
	}
	c.info = result.ServerInfo

	// The spec requires this notification before any other request; servers
	// that enforce it reject tools/list without it.
	if err := c.notify(ctx, "notifications/initialized", nil); err != nil {
		transport.Close()
		return nil, fmt.Errorf("mcp %s: %w", cfg.Name, err)
	}
	return c, nil
}

// Info is what the server reported about itself.
func (c *Client) Info() ServerInfo { return c.info }

// Close shuts the connection down.
func (c *Client) Close() error { return c.transport.Close() }

// ListTools fetches every tool the server offers, following pagination.
func (c *Client) ListTools(ctx context.Context) ([]ToolDef, error) {
	var all []ToolDef
	cursor := ""
	for {
		var params any
		if cursor != "" {
			params = map[string]string{"cursor": cursor}
		}
		var result listToolsResult
		if err := c.call(ctx, "tools/list", params, &result); err != nil {
			return nil, err
		}
		all = append(all, result.Tools...)
		if result.NextCursor == "" || result.NextCursor == cursor {
			// The second check guards a server that returns its own cursor
			// forever, which would otherwise be an infinite loop.
			return all, nil
		}
		cursor = result.NextCursor
		if len(all) > 500 {
			// A tool list this long is a misconfiguration, and every entry
			// costs context on every turn.
			return all, nil
		}
	}
}

// CallTool invokes a tool.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (CallToolResult, error) {
	var result CallToolResult
	err := c.call(ctx, "tools/call", callToolParams{Name: name, Arguments: args}, &result)
	return result, err
}

// call performs one request/response exchange.
func (c *Client) call(ctx context.Context, method string, params, out any) error {
	id := c.nextID.Add(1)
	payload, err := jsonBytes(request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return err
	}

	raw, err := c.transport.Send(ctx, payload, true)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return fmt.Errorf("empty response to %s", method)
	}

	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("malformed response to %s: %w", method, err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if resp.ID != nil && *resp.ID != id {
		return fmt.Errorf("response id %d does not match request %d", *resp.ID, id)
	}
	if out == nil || len(resp.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("malformed %s result: %w", method, err)
	}
	return nil
}

// notify sends a message that expects no reply.
func (c *Client) notify(ctx context.Context, method string, params any) error {
	payload, err := jsonBytes(notification{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	_, err = c.transport.Send(ctx, payload, false)
	return err
}
