package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/build"
)

// Client is a live connection to one MCP server.
//
// Requests are correlated by id, not by order: a server may answer out of
// order, send notifications in between, ask requests of its own, or answer
// a request that already timed out — none of which can hand one call
// another's reply.
type Client struct {
	Name string
	t    transport

	mu      sync.Mutex
	pending map[int64]chan *message
	nextID  int64
	closed  bool
	doneErr error
	done    chan struct{}

	modern       bool
	version      string
	caps         Capabilities
	info         ServerInfo
	instructions string
	// bindings are the modern era's x-mcp-header tool arguments, per tool.
	bindings map[string][]headerBinding

	onNotification func(method string, params json.RawMessage)
	onClose        func(err error)
}

// ConnectOptions are what the manager learns from a live connection.
type ConnectOptions struct {
	// OnNotification gets every notification the server sends.
	OnNotification func(method string, params json.RawMessage)
	// OnClose is called once if the connection dies on its own.
	OnClose func(err error)
	// Open is the browser opener for an interactive login; nil keeps a
	// background connect from ever opening one.
	Open func(url string)
}

var clientCapabilities = map[string]any{}

func clientInfo() map[string]any { return map[string]any{"name": "pi-agent", "version": build.Version} }

// Connect starts a server and completes the protocol negotiation. A remote
// server that turns out to speak the other HTTP transport is retried once on
// it, since nothing in a URL says which one a server implements.
func Connect(ctx context.Context, cfg ServerConfig, cwd string, o ConnectOptions) (*Client, error) {
	c, err := connectOnce(ctx, cfg, cwd, o)
	if err == nil || !cfg.IsHTTP() || IsUnauthorized(err) || !wrongTransport(err) {
		return c, err
	}
	other := cfg
	other.Type = "sse"
	if cfg.remoteType() == "sse" {
		other.Type = "http"
	}
	if c2, err2 := connectOnce(ctx, other, cwd, o); err2 == nil {
		return c2, nil
	}
	return nil, err
}

var wrongTransportText = regexp.MustCompile("(?i)does not support (?:HTTP|SSE) transport|try using `(?:sse|http)` transport")

func wrongTransport(err error) bool {
	if wrongTransportText.MatchString(err.Error()) {
		return true
	}
	var he *HTTPError
	return errors.As(err, &he) && (he.StatusCode == 404 || he.StatusCode == 405)
}

// IsUnauthorized is a server refusing us for lack of credentials rather
// than failing — whatever shape the refusal took.
func IsUnauthorized(err error) bool {
	if err == nil {
		return false
	}
	var ar *ErrAuthRequired
	var he *HTTPError
	if errors.As(err, &ar) || errors.Is(err, ErrUnauthorized) || (errors.As(err, &he) && he.StatusCode == 401) {
		return true
	}
	return unauthorizedText.MatchString(err.Error())
}

var unauthorizedText = regexp.MustCompile(`(?i)\bHTTP 401\b|\bunauthorized\b`)

func connectOnce(ctx context.Context, cfg ServerConfig, cwd string, o ConnectOptions) (*Client, error) {
	var t transport
	switch {
	case cfg.Command != "":
		t = newStdioTransport(cfg, cwd)
	case cfg.URL != "":
		var auth authorizer
		// A stored session is reason enough to attach the provider: most
		// servers reveal they want OAuth with a 401, and without it a
		// server already signed in to would be called anonymously. Without
		// one, a 401 is just a 401 — reported as needs-auth, and no client
		// is registered with anyone before the user asks to sign in.
		if cfg.Auth == "oauth" || HasStoredTokens(cfg.Name) || o.Open != nil {
			auth = newOAuthProvider(cfg.Name, cfg, refreshRedirectURI(), o.Open)
		}
		url, headers := ResolveSecrets(cfg.URL), resolveSecretMap(cfg.Headers)
		var err error
		if cfg.remoteType() == "sse" {
			t, err = newSSETransport(url, headers, auth)
		} else {
			t, err = newHTTPTransport(url, headers, auth)
		}
		if err != nil {
			return nil, fmt.Errorf("mcp %s: %w", cfg.Name, err)
		}
	default:
		return nil, fmt.Errorf("mcp %s: needs a command or a url", cfg.Name)
	}
	c := &Client{
		Name: cfg.Name, t: t, pending: map[int64]chan *message{}, done: make(chan struct{}),
		onNotification: o.OnNotification, onClose: o.OnClose,
	}
	if err := t.start(ctx, transportHandlers{onMessage: c.dispatch, onClose: c.lost}); err != nil {
		return nil, fmt.Errorf("mcp %s: %w", cfg.Name, err)
	}
	if err := c.negotiate(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("mcp %s: %w", cfg.Name, err)
	}
	return c, nil
}

// refreshRedirectURI is the redirect a background connect registers with:
// the callback server's preferred port, so an allow-listed URI matches.
func refreshRedirectURI() string {
	port := 8976
	if p, err := strconv.Atoi(os.Getenv("LOOP_MCP_OAUTH_CALLBACK_PORT")); err == nil && p > 0 {
		port = p
	}
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/callback"
}

// negotiate picks the protocol era: server/discover on a transport that
// supports it, else (or when the server does not know it) initialize.
func (c *Client) negotiate(ctx context.Context) error {
	if c.t.supportsDiscovery() {
		c.setVersion(LatestProtocolVersion, true)
		raw, err := c.request(ctx, "server/discover", nil, discoverTimeoutMs*time.Millisecond)
		if err == nil {
			var r discoverResult
			if err := json.Unmarshal(raw, &r); err != nil {
				return fmt.Errorf("malformed server/discover result: %w", err)
			}
			if !slices.Contains(r.SupportedVersions, LatestProtocolVersion) {
				return fmt.Errorf("server does not support the requested protocol version: %s", LatestProtocolVersion)
			}
			if raw := r.Meta[metaServerInfo]; raw != nil {
				_ = json.Unmarshal(raw, &c.info)
			}
			c.caps, c.instructions = r.Capabilities, r.Instructions
			return nil
		}
		var re *RPCError
		if errors.As(err, &re) && slices.Contains(modernProtocolErrorCodes, re.Code) {
			return err
		}
		if IsUnauthorized(err) || ctx.Err() != nil {
			return err
		}
	}
	c.setVersion(LatestLegacyProtocolVersion, false)
	raw, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": LatestLegacyProtocolVersion,
		"capabilities":    clientCapabilities,
		"clientInfo":      clientInfo(),
	}, 0)
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	var r initializeResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("server sent invalid initialize result: %w", err)
	}
	if !supportedVersion(r.ProtocolVersion) {
		return fmt.Errorf("server's protocol version is not supported: %s", r.ProtocolVersion)
	}
	c.setVersion(r.ProtocolVersion, false)
	c.caps, c.info, c.instructions = r.Capabilities, r.ServerInfo, r.Instructions
	// The spec requires this before any other request; servers that enforce
	// it reject tools/list without it.
	return c.notify(ctx, "notifications/initialized", nil)
}

func (c *Client) setVersion(v string, modern bool) {
	c.mu.Lock()
	c.version, c.modern = v, modern
	c.mu.Unlock()
	c.t.setProtocolVersion(v)
}

// Info is what the server reported about itself.
func (c *Client) Info() ServerInfo { return c.info }

// Instructions are the server's own usage notes for its tools.
func (c *Client) Instructions() string { return c.instructions }

// ProtocolVersion is the negotiated revision.
func (c *Client) ProtocolVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

// Capabilities are what the server declared.
func (c *Client) Capabilities() Capabilities { return c.caps }

// Done is closed when the connection ends.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close shuts the connection down. Pending calls fail at once.
func (c *Client) Close() error {
	c.fail(errors.New("closed client"), false)
	return c.t.close()
}

// lost is the transport reporting the connection gone on its own.
func (c *Client) lost(err error) {
	if err == nil {
		err = errors.New("connection closed")
	}
	c.fail(fmt.Errorf("connection closed: %w", err), true)
}

func (c *Client) fail(err error, report bool) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed, c.doneErr = true, err
	pending := c.pending
	c.pending = map[int64]chan *message{}
	c.mu.Unlock()
	close(c.done)
	for _, ch := range pending {
		ch <- &message{Error: &RPCError{Code: -32000, Message: err.Error()}}
	}
	if report && c.onClose != nil {
		c.onClose(err)
	}
}

// dispatch routes one incoming message.
func (c *Client) dispatch(m *message) {
	switch {
	case m.isRequest():
		go c.answer(m)
	case m.isNotification():
		if c.onNotification != nil {
			c.onNotification(m.Method, m.Params)
		}
	case len(m.ID) > 0:
		id, err := strconv.ParseInt(strings.Trim(string(m.ID), `"`), 10, 64)
		if err != nil {
			return
		}
		c.mu.Lock()
		ch, ok := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		// A reply nobody is waiting for — a call that timed out or was
		// cancelled — is dropped here, not handed to whoever asks next.
		if ok {
			ch <- m
		}
	}
}

// answer is the client side of a server's own request. ping is answered;
// everything this client does not serve (sampling, roots, elicitation — it
// declares none of them) gets method-not-found, never silence: a server
// left waiting stalls the tool call that asked.
func (c *Client) answer(m *message) {
	reply := &message{ID: m.ID}
	if m.Method == "ping" {
		reply.Result = json.RawMessage(`{}`)
	} else {
		reply.Error = &RPCError{Code: codeMethodNotFound, Message: "Unsupported request method: " + m.Method}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.t.send(ctx, reply, nil)
}

// request sends one request and waits for its own answer. A non-zero
// timeout bounds it on top of ctx.
func (c *Client) request(ctx context.Context, method string, params map[string]any, timeout time.Duration) (json.RawMessage, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	c.mu.Lock()
	if c.closed {
		err := c.doneErr
		c.mu.Unlock()
		return nil, err
	}
	id := c.nextID
	c.nextID++
	ch := make(chan *message, 1)
	c.pending[id] = ch
	modern := c.modern
	version := c.version
	c.mu.Unlock()

	if modern {
		if params == nil {
			params = map[string]any{}
		}
		meta, _ := params["_meta"].(map[string]any)
		merged := map[string]any{}
		for k, v := range meta {
			merged[k] = v
		}
		merged[metaProtocolVersion] = version
		merged[metaClientCapabilities] = clientCapabilities
		merged[metaClientInfo] = clientInfo()
		params["_meta"] = merged
	}
	m := &message{ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			c.forget(id)
			return nil, err
		}
		m.Params = raw
	}
	var headers map[string]string
	if modern && method == "tools/call" {
		var err error
		if headers, err = c.toolHeaders(params); err != nil {
			c.forget(id)
			return nil, err
		}
	}
	if err := c.t.send(ctx, m, headers); err != nil {
		c.forget(id)
		return nil, err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		if modern {
			var r struct {
				ResultType *string `json:"resultType"`
			}
			_ = json.Unmarshal(resp.Result, &r)
			if r.ResultType == nil {
				return nil, errors.New("modern MCP result is missing resultType")
			}
			if *r.ResultType == "input_required" {
				return nil, errors.New("server requested additional input, but multi round-trip requests are not supported yet")
			}
		}
		return resp.Result, nil
	case <-ctx.Done():
		c.forget(id)
		// Tell the server, so a timed-out call stops occupying it.
		if method != "initialize" && method != "server/discover" {
			reason := "request cancelled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				reason = "request timed out"
			}
			nctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = c.notify(nctx, "notifications/cancelled", map[string]any{"requestId": id, "reason": reason})
			cancel()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && timeout > 0 {
			return nil, fmt.Errorf("request timed out after %s", timeout)
		}
		return nil, ctx.Err()
	}
}

func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) notify(ctx context.Context, method string, params map[string]any) error {
	m := &message{Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		m.Params = raw
	}
	return c.t.send(ctx, m, nil)
}

// Ping is the liveness check.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.request(ctx, "ping", nil, 0)
	return err
}

// maxTools bounds a runaway tool list: every entry costs context on every
// turn, and a list this long is a misconfiguration.
const maxTools = 1000

// ListTools fetches every tool the server offers, following pagination.
func (c *Client) ListTools(ctx context.Context) ([]ToolDef, error) {
	var all []ToolDef
	cursor := ""
	for {
		var params map[string]any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		raw, err := c.request(ctx, "tools/list", params, 0)
		if err != nil {
			// A server that declared no tools may simply not implement the
			// method: no tools, not a broken connection.
			var re *RPCError
			if c.caps.Tools == nil && errors.As(err, &re) && re.Code == codeMethodNotFound {
				return nil, nil
			}
			return nil, err
		}
		var r listToolsResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("malformed tools/list result: %w", err)
		}
		all = append(all, r.Tools...)
		// The second check guards a server that returns its own cursor
		// forever, which would otherwise be an infinite loop.
		if r.NextCursor == "" || r.NextCursor == cursor || len(all) >= maxTools {
			c.setBindings(all)
			return all, nil
		}
		cursor = r.NextCursor
	}
}

// CallTool invokes a tool.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (CallToolResult, error) {
	params := map[string]any{"name": name}
	if len(args) > 0 && string(args) != "null" {
		var a any
		if err := json.Unmarshal(args, &a); err != nil {
			return CallToolResult{}, fmt.Errorf("arguments are not JSON: %w", err)
		}
		params["arguments"] = a
	}
	raw, err := c.request(ctx, "tools/call", params, 0)
	if err != nil {
		return CallToolResult{}, err
	}
	var r CallToolResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return CallToolResult{}, fmt.Errorf("malformed tools/call result: %w", err)
	}
	return r, nil
}

// --- x-mcp-header bindings (modern era) ---------------------------------------------

type headerBinding struct {
	header    string
	path      []string
	valueType string
}

var httpToken = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")

func (c *Client) setBindings(tools []ToolDef) {
	c.mu.Lock()
	modern := c.modern
	c.mu.Unlock()
	if !modern {
		return
	}
	out := map[string][]headerBinding{}
	for _, t := range tools {
		var schema any
		if json.Unmarshal(t.InputSchema, &schema) != nil {
			continue
		}
		if b, err := headerBindings(schema); err == nil && len(b) > 0 {
			out[t.Name] = b
		}
	}
	c.mu.Lock()
	c.bindings = out
	c.mu.Unlock()
}

// headerBindings finds the statically reachable properties a tool's schema
// marks with x-mcp-header (boolean, integer or string, unique names).
func headerBindings(schema any) ([]headerBinding, error) {
	var out []headerBinding
	seen := map[string]bool{}
	var walk func(v any, path []string, static bool) error
	walk = func(v any, path []string, static bool) error {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		if h, ok := m["x-mcp-header"]; ok {
			name, _ := h.(string)
			switch {
			case !static || len(path) == 0:
				return errors.New("x-mcp-header is not on a statically reachable property")
			case !httpToken.MatchString(name):
				return errors.New("x-mcp-header must be a non-empty HTTP token")
			case seen[strings.ToLower(name)]:
				return fmt.Errorf("x-mcp-header value %q is not unique", name)
			}
			typ, _ := m["type"].(string)
			if typ != "boolean" && typ != "integer" && typ != "string" {
				return errors.New("x-mcp-header can only annotate boolean, integer, or string properties")
			}
			seen[strings.ToLower(name)] = true
			out = append(out, headerBinding{header: name, path: path, valueType: typ})
		}
		for k, child := range m {
			if k == "x-mcp-header" {
				continue
			}
			if props, ok := child.(map[string]any); ok && k == "properties" {
				for name, p := range props {
					if err := walk(p, append(append([]string{}, path...), name), static); err != nil {
						return err
					}
				}
				continue
			}
			if err := walk(child, path, false); err != nil {
				return err
			}
		}
		return nil
	}
	return out, walk(schema, nil, true)
}

func (c *Client) toolHeaders(params map[string]any) (map[string]string, error) {
	name, _ := params["name"].(string)
	c.mu.Lock()
	bindings := c.bindings[name]
	c.mu.Unlock()
	args, _ := params["arguments"].(map[string]any)
	if len(bindings) == 0 || args == nil {
		return nil, nil
	}
	out := map[string]string{}
	for _, b := range bindings {
		var v any = args
		for _, seg := range b.path {
			m, ok := v.(map[string]any)
			if !ok {
				v = nil
				break
			}
			v = m[seg]
		}
		if v == nil {
			continue
		}
		var s string
		switch b.valueType {
		case "string":
			str, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("tool argument %q does not match its x-mcp-header type", strings.Join(b.path, "."))
			}
			s = str
		case "boolean":
			bv, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("tool argument %q does not match its x-mcp-header type", strings.Join(b.path, "."))
			}
			s = strconv.FormatBool(bv)
		case "integer":
			f, ok := v.(float64)
			if !ok || f != float64(int64(f)) || f > 1<<53 || f < -(1<<53) {
				return nil, fmt.Errorf("tool argument %q does not match its x-mcp-header type", strings.Join(b.path, "."))
			}
			s = strconv.FormatInt(int64(f), 10)
		}
		out["Mcp-Param-"+b.header] = encodeHeaderValue(s)
	}
	return out, nil
}
