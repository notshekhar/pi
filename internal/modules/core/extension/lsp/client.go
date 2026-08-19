package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// One language-server process, driven over stdio.
//
// LSP is PUSH-based in its original design — the server volunteers
// `publishDiagnostics` whenever it finishes analysing a document — and
// PULL-based in its modern one, where the server answers only when asked.
// Both are handled, because waiting for a push from a pull-only server just
// burns the timeout and then reports a clean file.

// message is a JSON-RPC frame.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *json.Number    `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Spec is what it takes to launch a server.
type Spec struct {
	Name       string
	Command    string
	Args       []string
	LanguageID func(absPath string) string
}

// Client is one running server.
type Client struct {
	spec Spec
	root string

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	nextID  int
	pending map[string]chan message
	// diagnostics holds the latest PUSHED set per URI.
	diagnostics map[string][]Diagnostic
	// waiters are unblocked when a push arrives for their URI.
	waiters map[string][]chan struct{}
	// openDocs is the version counter per URI: didOpen the first time,
	// didChange after.
	openDocs     map[string]int
	capabilities map[string]any
	started      bool
	dead         bool
}

// NewClient prepares a client. Nothing is launched until Start.
func NewClient(spec Spec, root string) *Client {
	return &Client{
		spec: spec, root: root,
		pending:     map[string]chan message{},
		diagnostics: map[string][]Diagnostic{},
		waiters:     map[string][]chan struct{}{},
		openDocs:    map[string]int{},
	}
}

// Alive reports whether the server is running.
func (c *Client) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started && !c.dead
}

// Start launches the server and performs the handshake.
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return nil
	}
	cmd := exec.Command(c.spec.Command, c.spec.Args...)
	cmd.Dir = c.root
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		c.mu.Unlock()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.mu.Unlock()
		return err
	}
	// Drained and discarded. A server that logs to stderr will BLOCK once the
	// pipe buffer fills if nobody reads it, and the symptom is a language
	// server that mysteriously stops answering after a few minutes.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		c.mu.Unlock()
		return err
	}
	if err := cmd.Start(); err != nil {
		c.mu.Unlock()
		return err
	}
	c.cmd, c.stdin, c.started = cmd, stdin, true
	c.mu.Unlock()

	go io.Copy(io.Discard, stderr)
	go c.read(stdout)
	go func() {
		_ = cmd.Wait()
		c.mu.Lock()
		c.dead = true
		pending := c.pending
		c.pending = map[string]chan message{}
		c.mu.Unlock()
		// Every in-flight request is answered rather than left hanging: a
		// caller blocked on a server that just died would wait out its whole
		// timeout for an answer that is never coming.
		for _, ch := range pending {
			close(ch)
		}
	}()

	// The capabilities declared here decide what the server will answer. A
	// server only offers what the client asked for, so a missing line means
	// a navigation operation silently returns nothing.
	result, err := c.request(ctx, "initialize", map[string]any{
		"processId": os.Getpid(),
		"rootUri":   PathToURI(c.root),
		"workspaceFolders": []map[string]any{
			{"uri": PathToURI(c.root), "name": c.spec.Name},
		},
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"synchronization":    map[string]any{"didSave": true, "dynamicRegistration": false},
				"publishDiagnostics": map[string]any{"relatedInformation": false},
				"definition":         map[string]any{"linkSupport": true},
				"implementation":     map[string]any{"linkSupport": true},
				"references":         map[string]any{},
				"hover":              map[string]any{"contentFormat": []string{"plaintext", "markdown"}},
				"documentSymbol":     map[string]any{"hierarchicalDocumentSymbolSupport": true},
				"callHierarchy":      map[string]any{},
			},
			"workspace": map[string]any{"symbol": map[string]any{}, "workspaceFolders": true},
		},
	})
	if err != nil {
		return err
	}
	var initialized struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	_ = json.Unmarshal(result, &initialized)

	c.mu.Lock()
	c.capabilities = initialized.Capabilities
	c.mu.Unlock()

	c.notify("initialized", map[string]any{})
	return nil
}

// Supports reports whether the server advertised a capability.
func (c *Client) Supports(capability string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.capabilities[capability]
	return ok && v != nil && v != false
}

// OpenDocument makes the server aware of a file's current content.
//
// Required before any position request: a server only answers about documents
// it has been told about. Reads from disk when no content is given.
func (c *Client) OpenDocument(absPath, content string) {
	if !c.Alive() {
		return
	}
	if content == "" {
		body, err := os.ReadFile(absPath)
		if err != nil {
			return
		}
		content = string(body)
	}
	c.sync(absPath, content)
}

// sync sends didOpen the first time and didChange after.
func (c *Client) sync(absPath, content string) {
	uri := PathToURI(absPath)
	c.mu.Lock()
	version, seen := c.openDocs[uri]
	if !seen {
		c.openDocs[uri] = 1
	} else {
		version++
		c.openDocs[uri] = version
	}
	c.mu.Unlock()

	if !seen {
		c.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri": uri, "languageId": c.spec.LanguageID(absPath),
				"version": 1, "text": content,
			},
		})
		return
	}
	c.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": version},
		"contentChanges": []map[string]any{{"text": content}},
	})
}

// Send issues a request, returning nil on any failure.
//
// Errors and crashes come back as nil rather than an error: one unsupported
// operation must not take down a tool call that other servers may still
// answer.
func (c *Client) Send(ctx context.Context, method string, params any, timeout time.Duration) json.RawMessage {
	if !c.Alive() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := c.request(ctx, method, params)
	if err != nil {
		return nil
	}
	return result
}

// Diagnose reports a file's problems, by whichever mechanism the server
// implements.
//
// Pull first where it is offered, then merged with anything already pushed:
// a server can do both, and the two mechanisms report the same problem
// differently often enough that deduplication is not optional.
func (c *Client) Diagnose(ctx context.Context, absPath, content string, timeout time.Duration) []Diagnostic {
	if !c.Alive() {
		return nil
	}
	uri := PathToURI(absPath)
	c.sync(absPath, content)

	if c.Supports("diagnosticProvider") {
		raw := c.Send(ctx, "textDocument/diagnostic", map[string]any{
			"textDocument": map[string]any{"uri": uri},
		}, timeout)
		var report struct {
			Items []Diagnostic `json:"items"`
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &report)
		}
		c.mu.Lock()
		pushed := c.diagnostics[uri]
		c.mu.Unlock()
		return dedupe(append(report.Items, pushed...))
	}

	c.waitForPush(ctx, uri, timeout)
	c.mu.Lock()
	defer c.mu.Unlock()
	return dedupe(c.diagnostics[uri])
}

// waitForPush blocks until the server publishes for this URI, or the deadline
// passes.
func (c *Client) waitForPush(ctx context.Context, uri string, timeout time.Duration) {
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.waiters[uri] = append(c.waiters[uri], ch)
	c.mu.Unlock()

	select {
	case <-ch:
		// A server often publishes an empty set first and the real one a
		// moment later, as analysis settles. Returning on the first push
		// would report a clean file that is about to be reported as broken.
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
		}
	case <-time.After(timeout):
	case <-ctx.Done():
	}
}

// dedupe drops repeats: two servers, or push and pull, can report the same
// problem, and it should be shown once.
func dedupe(list []Diagnostic) []Diagnostic {
	seen := map[string]bool{}
	out := make([]Diagnostic, 0, len(list))
	for _, d := range list {
		severity := d.Severity
		if severity == 0 {
			severity = SeverityError
		}
		key := fmt.Sprintf("%d|%d|%d|%s", severity, d.Range.Start.Line, d.Range.Start.Character, d.Message)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, d)
	}
	return out
}

// Shutdown ends the server politely, then makes sure.
func (c *Client) Shutdown() {
	c.mu.Lock()
	cmd, started, dead := c.cmd, c.started, c.dead
	c.mu.Unlock()
	if !started || cmd == nil {
		return
	}
	if !dead {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = c.request(ctx, "shutdown", nil)
		cancel()
		c.notify("exit", nil)
	}
	// Killed regardless: a server that ignores `exit` would otherwise outlive
	// the session, and a leaked language server holds a whole toolchain's
	// memory.
	_ = cmd.Process.Kill()
}

// ── wire plumbing ───────────────────────────────────────────────────────────

func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.dead || c.stdin == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("lsp: %s is not running", c.spec.Name)
	}
	c.nextID++
	// Captured INSIDE the lock and used from here on. Reading c.nextID again
	// when building the message let a concurrent request increment it in
	// between, so the frame went out under a different id than the one
	// registered in `pending` — and every answer went to the wrong caller.
	number := c.nextID
	id := strconv.Itoa(number)
	ch := make(chan message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.write(map[string]any{
		"jsonrpc": "2.0", "id": number, "method": method, "params": params,
	}); err != nil {
		return nil, err
	}

	select {
	case msg, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("lsp: %s exited", c.spec.Name)
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("lsp: %s", msg.Error.Message)
		}
		return msg.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) notify(method string, params any) {
	_ = c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *Client) write(msg any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdin == nil {
		return fmt.Errorf("lsp: not running")
	}
	if _, err := fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = c.stdin.Write(body)
	return err
}

// read is the frame loop: `Content-Length: N\r\n\r\n` then exactly N bytes.
func (c *Client) read(stdout io.Reader) {
	reader := bufio.NewReaderSize(stdout, 64*1024)
	for {
		length := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break // end of headers
			}
			if name, value, ok := strings.Cut(line, ":"); ok &&
				strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
				length, _ = strconv.Atoi(strings.TrimSpace(value))
			}
		}
		if length <= 0 {
			continue
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(reader, body); err != nil {
			return
		}
		var msg message
		if err := json.Unmarshal(body, &msg); err != nil {
			continue // an unparseable frame is skipped, not fatal
		}
		c.dispatch(msg)
	}
}

func (c *Client) dispatch(msg message) {
	// A response: it has an id and a result or an error.
	if msg.ID != nil && (msg.Result != nil || msg.Error != nil) {
		id := msg.ID.String()
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- msg
		}
		return
	}

	if msg.Method == "textDocument/publishDiagnostics" {
		var params PublishDiagnosticsParams
		if err := json.Unmarshal(msg.Params, &params); err == nil {
			c.mu.Lock()
			c.diagnostics[params.URI] = params.Diagnostics
			waiters := c.waiters[params.URI]
			delete(c.waiters, params.URI)
			c.mu.Unlock()
			for _, w := range waiters {
				select {
				case w <- struct{}{}:
				default:
				}
			}
		}
		return
	}

	// A server-to-client REQUEST (it has an id and a method) must be
	// answered, or a server that asks for configuration blocks forever
	// waiting. A null result is a valid answer to everything asked here.
	if msg.ID != nil && msg.Method != "" {
		var id any
		if n, err := msg.ID.Int64(); err == nil {
			id = n
		} else {
			id = msg.ID.String()
		}
		_ = c.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": nil})
	}
}
