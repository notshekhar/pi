package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Transports. Two are implemented: a subprocess speaking newline-delimited
// JSON on its stdio, and HTTP POST. Between them they cover essentially every
// server in the wild.

// Transport carries one request/response exchange.
type Transport interface {
	// Send writes a message and returns the raw reply. A notification (no id)
	// returns nil.
	Send(ctx context.Context, payload []byte, wantReply bool) ([]byte, error)
	Close() error
}

// --- stdio ------------------------------------------------------------------

// stdioTransport runs a server as a child process and talks over its stdio.
type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	// One exchange at a time. MCP allows interleaving by id, but a coding
	// agent issues calls one at a time and serialising removes a whole class
	// of correlation bugs for no practical cost.
	mu sync.Mutex

	stderr *strings.Builder
}

// StdioConfig describes a subprocess server.
type StdioConfig struct {
	Command string
	Args    []string
	Env     []string
	CWD     string
}

func newStdioTransport(cfg StdioConfig) (*stdioTransport, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.CWD
	// Inherit the environment: servers routinely need PATH, HOME, and their
	// own credentials, and an empty env makes them fail in ways that look
	// like protocol errors.
	cmd.Env = append(os.Environ(), cfg.Env...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Stderr is captured rather than discarded: when a server dies during the
	// handshake, its stderr is the only thing that says why.
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, remaining: 8 << 10}

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &stdioTransport{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReaderSize(stdout, 1<<20),
		stderr: &stderr,
	}, nil
}

func (t *stdioTransport) Send(ctx context.Context, payload []byte, wantReply bool) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, err := t.stdin.Write(append(payload, '\n')); err != nil {
		return nil, t.wrap(err)
	}
	if !wantReply {
		return nil, nil
	}

	type result struct {
		line []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := t.stdout.ReadBytes('\n')
		done <- result{line, err}
	}()

	select {
	case <-ctx.Done():
		// The read goroutine stays blocked on a pipe that will unblock when
		// the process is killed on Close. Abandoning it is safe precisely
		// because Send is serialised.
		return nil, ctx.Err()
	case r := <-done:
		if r.err != nil {
			return nil, t.wrap(r.err)
		}
		return bytes.TrimSpace(r.line), nil
	}
}

// wrap adds the server's stderr to an I/O error, which is otherwise just
// "EOF" for what is really "the server crashed on startup".
func (t *stdioTransport) wrap(err error) error {
	if msg := strings.TrimSpace(t.stderr.String()); msg != "" {
		return fmt.Errorf("%w: %s", err, lastLines(msg, 5))
	}
	return err
}

func (t *stdioTransport) Close() error {
	t.stdin.Close()
	if t.cmd.Process != nil {
		t.cmd.Process.Kill()
	}
	return t.cmd.Wait()
}

// limitedWriter keeps a bounded tail of a stream, so a chatty server cannot
// grow this process's memory without limit.
type limitedWriter struct {
	w         io.Writer
	remaining int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.remaining <= 0 {
		return len(p), nil
	}
	if len(p) > l.remaining {
		p = p[:l.remaining]
	}
	l.remaining -= len(p)
	_, err := l.w.Write(p)
	return len(p), err
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}

// --- http -------------------------------------------------------------------

// httpTransport POSTs each message to a URL.
type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client
	// sessionID is issued by the server on initialize and echoed back on
	// every later request; servers that use it reject messages without it.
	sessionID string
	mu        sync.Mutex
}

// HTTPConfig describes a remote server.
type HTTPConfig struct {
	URL     string
	Headers map[string]string
}

func newHTTPTransport(cfg HTTPConfig) *httpTransport {
	return &httpTransport{
		url:     cfg.URL,
		headers: cfg.Headers,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (t *httpTransport) Send(ctx context.Context, payload []byte, wantReply bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	t.mu.Lock()
	if t.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	t.mu.Unlock()

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		t.mu.Lock()
		t.sessionID = id
		t.mu.Unlock()
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if !wantReply {
		return nil, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	// Streamable HTTP may answer a single request as an SSE stream, so the
	// JSON has to be dug out of the event framing.
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return extractSSEData(body), nil
	}
	return bytes.TrimSpace(body), nil
}

// extractSSEData pulls the last `data:` payload out of an SSE response.
func extractSSEData(body []byte) []byte {
	var last []byte
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if after, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			if trimmed := bytes.TrimSpace(after); len(trimmed) > 0 {
				last = trimmed
			}
		}
	}
	return last
}

func (t *httpTransport) Close() error { return nil }

// jsonBytes marshals a message, keeping the error at the call site.
func jsonBytes(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("mcp: encoding message: %w", err)
	}
	return data, nil
}
