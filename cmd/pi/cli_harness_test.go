package main

import (
	"bytes"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
)

// The subcommand tests run the real binary in a scratch HOME: exit codes,
// which stream a line lands on, and what reaches disk are the behaviour, and
// only a real process shows all three. Nothing here can touch ~/.loop.

var (
	piBinOnce sync.Once
	piBinDir  string
	piBin     string
	piBinErr  error
)

// builtPi compiles cmd/pi once per test run.
func builtPi(t *testing.T) string {
	t.Helper()
	piBinOnce.Do(func() {
		piBinDir, piBinErr = os.MkdirTemp("", "pi-cli-test-")
		if piBinErr != nil {
			return
		}
		piBin = filepath.Join(piBinDir, "pi")
		out, err := exec.Command("go", "build", "-o", piBin, ".").CombinedOutput()
		if err != nil {
			piBinErr = errors.New(string(out))
		}
	})
	if piBinErr != nil {
		t.Fatalf("building pi: %v", piBinErr)
	}
	return piBin
}

// removeBuiltPi is called from TestMain once every test has finished.
func removeBuiltPi() {
	if piBinDir != "" {
		_ = os.RemoveAll(piBinDir)
	}
}

// piResult is one finished invocation.
type piResult struct {
	stdout, stderr string
	code           int
}

// piHome is a scratch HOME with a working directory inside it.
type piHome struct {
	home, cwd string
}

func newPiHome(t *testing.T) piHome {
	t.Helper()
	// Resolved: the macOS temp dir is behind a symlink, and the process
	// reports its working directory without it.
	h, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(h, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	return piHome{home: h, cwd: cwd}
}

// run invokes pi with args, stdin closed unless given.
func (h piHome) run(t *testing.T, stdin string, args ...string) piResult {
	t.Helper()
	cmd := exec.Command(builtPi(t), args...)
	cmd.Dir = h.cwd
	cmd.Env = append(os.Environ(), "HOME="+h.home, "USERPROFILE="+h.home, "LOOP_SOUND=0")
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return piResult{out.String(), errOut.String(), code}
}

// mustRun is run, failing the test unless the exit code is want.
func (h piHome) mustRun(t *testing.T, want int, args ...string) piResult {
	t.Helper()
	r := h.run(t, "", args...)
	if r.code != want {
		t.Fatalf("pi %s: exit %d, want %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), r.code, want, r.stdout, r.stderr)
	}
	return r
}

// anthropicReply is one streamed Anthropic message saying text.
func anthropicReply(text string) string {
	delta, _ := json.Marshal(text, jsonopt.Write)
	return `event: message_start
data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"claude-x","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + string(delta) + `}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`
}

// fakeAnthropic answers every streamed request with reply, or with status
// when it is not 200.
func fakeAnthropic(t *testing.T, status int, reply string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"refused by the fake"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// withGateway registers custom provider "gw" (Anthropic dialect) at url, so
// `--model gw/claude-x` reaches the fake.
func (h piHome) withGateway(t *testing.T, url string) {
	t.Helper()
	auth := map[string]any{"providers": map[string]any{}, "customProviders": map[string]any{
		"gw": map[string]any{"name": "gw", "sdk": "anthropic", "baseURL": url, "apiKey": "k",
			"models": []map[string]any{{"id": "claude-x", "contextWindow": 100000}}},
	}}
	h.writeLoopFile(t, "auth.json", auth)
}

// writeLoopFile writes JSON into the scratch ~/.loop.
func (h piHome) writeLoopFile(t *testing.T, name string, v any) {
	t.Helper()
	dir := filepath.Join(h.home, ".loop")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(v, jsonopt.Write)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
