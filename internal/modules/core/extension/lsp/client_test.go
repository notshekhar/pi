package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tests drive a REAL server process over a real pipe — the test binary
// re-executed with an env var, which is the standard Go trick for this.
//
// Nothing here can be checked by unit-testing the framing in isolation: the
// bugs an LSP client actually has are a header parsed across two reads, a
// response correlated to the wrong id, and a pull-only server waited on for a
// push that never comes. All three need two processes and a pipe.

const fixtureEnv = "PI_LSP_FIXTURE"

// TestMain runs the fixture server when the env var is set, and the tests
// otherwise.
func TestMain(m *testing.M) {
	if mode := os.Getenv(fixtureEnv); mode != "" {
		runFixture(mode)
		return
	}
	os.Exit(m.Run())
}

// fixtureSpec builds a Spec that launches this test binary as a server.
func fixtureSpec(t *testing.T, mode string) Spec {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Spec{
		Name:       "fixture",
		Command:    self,
		Args:       nil,
		LanguageID: func(string) string { return "go" },
	}
}

// startFixture launches a client against the fixture in `mode`.
func startFixture(t *testing.T, mode string) *Client {
	t.Helper()
	spec := fixtureSpec(t, mode)
	c := NewClient(spec, t.TempDir())
	// The env var is what turns the test binary into a server; exec.Cmd is
	// built inside Start, so it is threaded through the process environment.
	t.Setenv(fixtureEnv, mode)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Shutdown)
	return c
}

func TestHandshakeAndCapabilities(t *testing.T) {
	c := startFixture(t, "push")
	if !c.Alive() {
		t.Fatal("client is not alive after start")
	}
	if !c.Supports("hoverProvider") {
		t.Error("hoverProvider was advertised and not seen")
	}
	if c.Supports("nonsenseProvider") {
		t.Error("an unadvertised capability reported as supported")
	}
}

// A PUSH server volunteers diagnostics; the client must wait for them.
func TestPushDiagnostics(t *testing.T) {
	c := startFixture(t, "push")
	file := filepath.Join(t.TempDir(), "main.go")
	os.WriteFile(file, []byte("package main\n"), 0o644)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got := c.Diagnose(ctx, file, "package main\n", 5*time.Second)
	if len(got) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Message, "pushed") {
		t.Errorf("message = %q", got[0].Message)
	}
}

// A PULL-only server answers when asked. Waiting for a push from one just
// burns the timeout and then reports a clean file — the bug this branch
// exists to prevent.
func TestPullDiagnostics(t *testing.T) {
	c := startFixture(t, "pull")
	file := filepath.Join(t.TempDir(), "main.go")
	os.WriteFile(file, []byte("package main\n"), 0o644)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got := c.Diagnose(ctx, file, "package main\n", 5*time.Second)
	if len(got) != 1 || !strings.Contains(got[0].Message, "pulled") {
		t.Fatalf("got %+v", got)
	}
	// It must not have waited out the push timeout.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("a pull-only server was waited on for %v", elapsed)
	}
}

// Responses are correlated by id, so concurrent requests cannot cross.
func TestConcurrentRequestsDoNotCross(t *testing.T) {
	c := startFixture(t, "echo")
	ctx := context.Background()

	const n = 20
	results := make([]string, n)
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			raw := c.Send(ctx, "test/echo", map[string]any{"value": i}, 5*time.Second)
			var out struct {
				Value int `json:"value"`
			}
			_ = json.Unmarshal(raw, &out)
			results[i] = fmt.Sprintf("%d", out.Value)
			done <- i
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
	for i := 0; i < n; i++ {
		if results[i] != strconv.Itoa(i) {
			t.Fatalf("request %d got %q — responses crossed", i, results[i])
		}
	}
}

// A server that dies must fail its in-flight requests rather than leaving the
// caller to wait out the timeout.
func TestDeadServerFailsFast(t *testing.T) {
	c := startFixture(t, "die")
	ctx := context.Background()
	start := time.Now()
	// The fixture exits on the first request after initialize.
	c.Send(ctx, "test/echo", map[string]any{"value": 1}, 30*time.Second)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("waited %v on a dead server", elapsed)
	}
}

// Shutdown must reap the process.
func TestShutdownReapsTheProcess(t *testing.T) {
	c := startFixture(t, "push")
	c.Shutdown()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !c.Alive() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("the server was still alive after shutdown")
}

// ── the fixture server ──────────────────────────────────────────────────────

// runFixture is a minimal language server: enough LSP to exercise the client.
func runFixture(mode string) {
	reader := bufio.NewReader(os.Stdin)
	// Serialised: the fixture answers from goroutines, and two of them
	// writing a header and a body concurrently would interleave into a
	// corrupt stream — which looks exactly like a client bug.
	var writeMu sync.Mutex
	write := func(v any) {
		body, _ := json.Marshal(v)
		writeMu.Lock()
		defer writeMu.Unlock()
		fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n", len(body))
		os.Stdout.Write(body)
	}

	for {
		length := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
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
		var msg struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			Params json.RawMessage  `json:"params"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}

		switch msg.Method {
		case "initialize":
			capabilities := map[string]any{"hoverProvider": true, "definitionProvider": true}
			if mode == "pull" {
				capabilities["diagnosticProvider"] = map[string]any{}
			}
			write(map[string]any{
				"jsonrpc": "2.0", "id": msg.ID,
				"result": map[string]any{"capabilities": capabilities},
			})

		case "textDocument/didOpen", "textDocument/didChange":
			if mode != "push" {
				continue
			}
			// Published a moment later, as a real server does once analysis
			// settles — the client must wait rather than read an empty set.
			var params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			uri := params.TextDocument.URI
			go func() {
				time.Sleep(100 * time.Millisecond)
				write(map[string]any{
					"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics",
					"params": map[string]any{
						"uri": uri,
						"diagnostics": []map[string]any{{
							"range":    map[string]any{"start": map[string]int{"line": 0, "character": 0}, "end": map[string]int{"line": 0, "character": 1}},
							"severity": 1, "message": "pushed problem",
						}},
					},
				})
			}()

		case "textDocument/diagnostic":
			write(map[string]any{
				"jsonrpc": "2.0", "id": msg.ID,
				"result": map[string]any{"items": []map[string]any{{
					"range":    map[string]any{"start": map[string]int{"line": 0, "character": 0}, "end": map[string]int{"line": 0, "character": 1}},
					"severity": 1, "message": "pulled problem",
				}}},
			})

		case "test/echo":
			if mode == "die" {
				os.Exit(1)
			}
			var params struct {
				Value int `json:"value"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			// Answered out of order on purpose: a client that assumed
			// responses arrive in request order would pass every other test
			// and fail this one.
			go func(id *json.RawMessage, value int) {
				time.Sleep(time.Duration(value%5) * 10 * time.Millisecond)
				write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"value": value}})
			}(msg.ID, params.Value)

		case "shutdown":
			write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": nil})
		case "exit":
			os.Exit(0)
		}
	}
}

var _ = exec.Command
