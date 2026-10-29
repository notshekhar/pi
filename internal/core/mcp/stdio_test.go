package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/notshekhar/pi/internal/ai/provider"
)

func connectFake(t *testing.T, o ConnectOptions) *Client {
	t.Helper()
	c, err := Connect(context.Background(), fakeConfig(t, "fake"), t.TempDir(), o)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func call(t *testing.T, c *Client, tool, text string) CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := c.CallTool(ctx, tool, json.RawMessage(`{"text":`+strconv.Quote(text)+`}`))
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res
}

func textOf(r CallToolResult) string {
	var parts []string
	for _, c := range r.Content {
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, "\n")
}

// The legacy handshake: initialize, the server's version accepted, its
// identity and instructions kept.
func TestStdioHandshake(t *testing.T) {
	c := connectFake(t, ConnectOptions{})
	if c.Info().Name != "fake" || c.ProtocolVersion() != "2025-06-18" || c.Instructions() != "use echo" {
		t.Errorf("info=%+v version=%q instructions=%q", c.Info(), c.ProtocolVersion(), c.Instructions())
	}
	if got := textOf(call(t, c, "echo", "hello there")); got != "hello there" {
		t.Errorf("echo = %q", got)
	}
}

// A log line on stdout and a notification ahead of the reply must not be
// taken for the reply — and must not cost the next call its answer.
func TestStdioIgnoresNoiseAndRoutesByID(t *testing.T) {
	var notes []string
	c := connectFake(t, ConnectOptions{OnNotification: func(method string, _ json.RawMessage) { notes = append(notes, method) }})
	if got := textOf(call(t, c, "noisy", "")); got != "quiet now" {
		t.Errorf("noisy = %q", got)
	}
	if got := textOf(call(t, c, "echo", "next")); got != "next" {
		t.Errorf("the call after the noise got %q", got)
	}
	if len(notes) == 0 || notes[0] != "notifications/message" {
		t.Errorf("notifications = %v", notes)
	}
}

// A server's own request is answered, and the call that triggered it then
// completes.
func TestStdioAnswersServerRequests(t *testing.T) {
	c := connectFake(t, ConnectOptions{})
	if got := textOf(call(t, c, "pingme", "")); got != "pong received" {
		t.Errorf("pingme = %q", got)
	}
}

// Many calls in flight at once each get their own answer.
func TestStdioConcurrentCalls(t *testing.T) {
	c := connectFake(t, ConnectOptions{})
	errs := make(chan string, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			want := "n" + strconv.Itoa(i)
			res, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"`+want+`"}`))
			switch {
			case err != nil:
				errs <- err.Error()
			case textOf(res) != want:
				errs <- "got " + textOf(res) + " want " + want
			default:
				errs <- ""
			}
		}(i)
	}
	for i := 0; i < 20; i++ {
		if e := <-errs; e != "" {
			t.Error(e)
		}
	}
}

// A call that never returns times out, tells the server it was cancelled,
// and leaves the connection usable.
func TestStdioTimeoutCancelsTheRequest(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("FAKE_LOG", log)
	c := connectFake(t, ConnectOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := c.CallTool(ctx, "hang", nil); err == nil {
		t.Fatal("a hung call returned")
	}
	if got := textOf(call(t, c, "echo", "still here")); got != "still here" {
		t.Errorf("after a timeout: %q", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if data, _ := os.ReadFile(log); strings.Contains(string(data), "notifications/cancelled") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the server was never told the call was cancelled")
}

// A server that dies is reported, pending calls fail at once, and stderr
// says why when there is anything to say.
func TestStdioDeathIsReported(t *testing.T) {
	closed := make(chan error, 1)
	c := connectFake(t, ConnectOptions{OnClose: func(err error) { closed <- err }})
	if _, err := c.CallTool(context.Background(), "die", nil); err == nil || !isDisconnect(err) {
		t.Errorf("call on a dying server: %v", err)
	}
	select {
	case err := <-closed:
		if !strings.Contains(err.Error(), "connection closed") {
			t.Errorf("close reason = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("death was never reported")
	}
}

func TestStdioReportsStartupFailure(t *testing.T) {
	_, err := Connect(context.Background(), ServerConfig{
		Name: "broken", Command: "sh", Args: []string{"-c", "echo 'boom: missing API key' >&2; exit 1"},
	}, t.TempDir(), ConnectOptions{})
	if err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Errorf("stderr was not surfaced: %v", err)
	}
}

// Closing reaches what the server spawned, not just the server.
func TestStdioCloseKillsTheProcessGroup(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("FAKE_PIDFILE", pidfile)
	c, err := Connect(context.Background(), fakeConfig(t, "fake"), t.TempDir(), ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	call(t, c, "spawn", "")
	raw, _ := os.ReadFile(pidfile)
	pid, _ := strconv.Atoi(string(raw))
	if pid == 0 {
		t.Fatal("no child pid")
	}
	c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Error("the server's child outlived Close")
}

// Secrets in env come from the environment, not the file.
func TestStdioResolvesEnvSecrets(t *testing.T) {
	t.Setenv("PI_TEST_SECRET", "s3cret")
	cfg := ServerConfig{Name: "env", Command: "sh", Args: []string{"-c", `printf '%s' "$TOKEN" >&2; exit 1`},
		Env: map[string]string{"TOKEN": "${env:PI_TEST_SECRET}"}}
	_, err := Connect(context.Background(), cfg, t.TempDir(), ConnectOptions{})
	if err == nil || !strings.Contains(err.Error(), "s3cret") {
		t.Errorf("secret not resolved: %v", err)
	}
}

func TestToolResultMapping(t *testing.T) {
	structured := toolResult(CallToolResult{StructuredContent: json.RawMessage(`{"a":1}`)})
	if out, ok := structured.Output().(provider.ToolOutputText); !ok || !strings.Contains(out.Value, `"a": 1`) {
		t.Errorf("structuredContent dropped: %#v", structured.Output())
	}
	failed := toolResult(CallToolResult{IsError: true, Content: []Content{{Type: "text", Text: "nope"}}})
	if out, ok := failed.Output().(provider.ToolOutputErrorText); !ok || out.Value != "nope" {
		t.Errorf("isError lost: %#v", failed.Output())
	}
	image := toolResult(CallToolResult{Content: []Content{{Type: "text", Text: "see"}, {Type: "image", Data: "aGk=", MimeType: "image/png"}}})
	if _, ok := image.Output().(provider.ToolOutputContent); !ok {
		t.Errorf("image dropped: %#v", image.Output())
	}
	empty := toolResult(CallToolResult{})
	if out, ok := empty.Output().(provider.ToolOutputText); !ok || out.Value != "(no output)" {
		t.Errorf("empty = %#v", empty.Output())
	}
}

func TestToolNaming(t *testing.T) {
	if got := ToolName("my-fs", "read.file"); got != "mcp__my_fs__read_file" {
		t.Errorf("ToolName = %q", got)
	}
	long := ToolName(strings.Repeat("server", 20), strings.Repeat("tool", 30))
	if len(long) > maxToolName || !strings.HasPrefix(long, ServerPrefix(strings.Repeat("server", 20))) {
		t.Errorf("long name %q (%d)", long, len(long))
	}
	taken := map[string]bool{}
	a := uniqueToolName("s", "get-issue", taken)
	taken[a] = true
	b := uniqueToolName("s", "get_issue", taken)
	if a == b || len(b) > maxToolName {
		t.Errorf("collision not disambiguated: %q %q", a, b)
	}
	if again := uniqueToolName("s", "get_issue", map[string]bool{a: true}); again != b {
		t.Errorf("disambiguation is not stable: %q vs %q", again, b)
	}
}
