package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// A transport moves JSON-RPC messages. Delivery is asynchronous in both
// directions: send hands a message over, and everything the server says —
// responses, notifications, requests of its own — arrives through onMessage.
// The client correlates responses by id, so nothing here assumes the next
// thing a server says is the answer to the last thing it was asked.
type transport interface {
	start(ctx context.Context, h transportHandlers) error
	// send delivers one message. headers are extra HTTP headers for this
	// request (the modern era's per-tool bindings); stdio ignores them.
	send(ctx context.Context, m *message, headers map[string]string) error
	close() error
	setProtocolVersion(v string)
	// supportsDiscovery is whether the modern era's server/discover is worth
	// trying (@ai-sdk/mcp: streamable HTTP only).
	supportsDiscovery() bool
}

type transportHandlers struct {
	onMessage func(*message)
	// onClose is called once, when the connection ends for any reason other
	// than close(). err says why.
	onClose func(err error)
}

func encode(m *message) ([]byte, error) {
	m.JSONRPC = "2.0"
	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("mcp: encoding message: %w", err)
	}
	return data, nil
}

// decodeMessages parses one JSON-RPC message or a batch.
func decodeMessages(data []byte) ([]*message, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	if data[0] == '[' {
		var batch []*message
		if err := json.Unmarshal(data, &batch); err != nil {
			return nil, err
		}
		return batch, nil
	}
	var m message
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return []*message{&m}, nil
}

// --- stdio ------------------------------------------------------------------

// stdioTransport runs a server as a child process speaking newline-delimited
// JSON on its stdio.
type stdioTransport struct {
	command string
	args    []string
	env     []string
	dir     string

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	stderr  *stderrTail
	exited  chan struct{}
	waitErr error

	closeOnce sync.Once
	closing   chan struct{}
}

func newStdioTransport(cfg ServerConfig, cwd string) *stdioTransport {
	return &stdioTransport{command: cfg.Command, args: cfg.Args, env: cfg.envList(), dir: cwd, closing: make(chan struct{})}
}

func (t *stdioTransport) supportsDiscovery() bool   { return false }
func (t *stdioTransport) setProtocolVersion(string) {}

func (t *stdioTransport) start(_ context.Context, h transportHandlers) error {
	cmd := exec.Command(t.command, t.args...)
	cmd.Dir = t.dir
	// Inherit the environment: servers routinely need PATH, HOME, and their
	// own credentials, and an empty env makes them fail in ways that look
	// like protocol errors.
	cmd.Env = append(os.Environ(), t.env...)
	// Its own process group, so closing reaches what it spawned: `npx foo`
	// is npx plus the node server it started, and killing only npx leaves
	// the server running with nobody to ever stop it.
	setProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// Captured, never written to the terminal: a server's stderr writing
	// into the TUI desyncs the renderer, and when a server dies during the
	// handshake its stderr is the only thing that says why.
	t.stderr = &stderrTail{remaining: 8 << 10}
	cmd.Stderr = t.stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	t.cmd, t.stdin, t.exited = cmd, stdin, make(chan struct{})
	go func() {
		t.waitErr = cmd.Wait()
		close(t.exited)
	}()
	go t.read(stdout, h)
	return nil
}

func (t *stdioTransport) read(stdout io.Reader, h transportHandlers) {
	r := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			msgs, perr := decodeMessages(line)
			// A line that is not JSON-RPC is a server logging to the wrong
			// stream. It is not a reason to drop the connection, and it is
			// certainly not the reply to anything.
			if perr == nil {
				for _, m := range msgs {
					h.onMessage(m)
				}
			}
		}
		if err != nil {
			select {
			case <-t.closing:
				return // our own close: not news
			default:
			}
			h.onClose(t.exitError(err))
			return
		}
	}
}

// exitError adds the server's stderr to an I/O error, which is otherwise
// just "EOF" for what is really "the server crashed".
//
// It waits for the child first, briefly: stdout reaching EOF and os/exec
// finishing the copy of stderr are independent events, and reading the
// tail straight away loses the useful half of the error on a fast machine.
func (t *stdioTransport) exitError(err error) error {
	select {
	case <-t.exited:
	case <-time.After(500 * time.Millisecond):
	}
	if errors.Is(err, io.EOF) {
		err = errors.New("connection closed")
	}
	if msg := strings.TrimSpace(t.stderr.String()); msg != "" {
		return fmt.Errorf("%w: %s", err, lastLines(msg, 5))
	}
	return err
}

func (t *stdioTransport) send(_ context.Context, m *message, _ map[string]string) error {
	data, err := encode(m)
	if err != nil {
		return err
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if t.stdin == nil {
		return errNotConnected
	}
	if _, err := t.stdin.Write(append(data, '\n')); err != nil {
		return t.exitError(err)
	}
	return nil
}

// close is the spec's shutdown: close stdin and let the server exit, then
// SIGTERM, then SIGKILL — each to the whole process group.
func (t *stdioTransport) close() error {
	t.closeOnce.Do(func() {
		close(t.closing)
		if t.cmd == nil {
			return
		}
		t.writeMu.Lock()
		t.stdin.Close()
		t.writeMu.Unlock()
		select {
		case <-t.exited:
		case <-time.After(2 * time.Second):
			signalGroup(t.cmd, false)
			select {
			case <-t.exited:
			case <-time.After(2 * time.Second):
				signalGroup(t.cmd, true)
				<-t.exited
			}
		}
		// The server is gone; anything it left behind in its group is not.
		signalGroup(t.cmd, true)
	})
	return nil
}

// stderrTail keeps a bounded, concurrency-safe head of a stream: a chatty
// server must not be able to grow this process's memory, and os/exec writes
// from its own goroutine while the error path reads.
type stderrTail struct {
	mu        sync.Mutex
	buf       strings.Builder
	remaining int
}

func (l *stderrTail) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	if l.remaining <= 0 {
		return n, nil
	}
	if len(p) > l.remaining {
		p = p[:l.remaining]
	}
	l.remaining -= len(p)
	l.buf.Write(p)
	return n, nil
}

func (l *stderrTail) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}
