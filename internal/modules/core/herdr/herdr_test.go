package herdr

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/notshekhar/pi/internal/modules/core/status"
)

// fakeHerdr is a real unix-socket server speaking the same one-request-per-
// connection protocol herdr does. Real, because the failure this integration
// has to survive is a socket-level one — a server that is gone, or slow — and
// a mock of the socket cannot exhibit it.
type fakeHerdr struct {
	path string
	ln   net.Listener

	mu   sync.Mutex
	reqs []map[string]any
}

func newFakeHerdr(t *testing.T) *fakeHerdr {
	t.Helper()
	// Socket paths are limited to ~100 bytes; t.TempDir() under /var/folders
	// on macOS is close enough to the limit that a long test name breaks it.
	path := filepath.Join(t.TempDir(), "h.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("no unix sockets here: %v", err)
	}
	f := &fakeHerdr{path: path, ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				var req map[string]any
				if json.Unmarshal(line, &req) == nil {
					f.mu.Lock()
					f.reqs = append(f.reqs, req)
					f.mu.Unlock()
				}
				conn.Write([]byte("{\"ok\":true}\n"))
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeHerdr) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		out = append(out, r["method"].(string))
	}
	return out
}

func (f *fakeHerdr) waitFor(t *testing.T, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := len(f.reqs)
		f.mu.Unlock()
		if got >= n {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.reqs...)
}

func env(socket string) func(string) string {
	return func(k string) string {
		switch k {
		case "HERDR_ENV":
			return "1"
		case "HERDR_SOCKET_PATH":
			return socket
		case "HERDR_PANE_ID":
			return "pane-7"
		}
		return ""
	}
}

func TestReportsStateAndSessionToHerdr(t *testing.T) {
	f := newFakeHerdr(t)
	bus := status.New(0)
	r := Attach(bus, Options{
		Env:     env(f.path),
		Session: func() Session { return Session{ID: "s1", Path: "/tmp/s1.jsonl"} },
	})
	if !r.Active() {
		t.Fatal("reporter did not activate inside a herdr pane")
	}
	bus.SetWorking()

	// The launch announcement (session + idle) and the working report.
	reqs := f.waitFor(t, 3)
	if len(reqs) < 3 {
		t.Fatalf("got %d requests (%v), want the session, idle and working reports", len(reqs), f.methods())
	}
	if reqs[0]["method"] != "pane.report_agent_session" {
		t.Fatalf("identity was not announced first: %v", f.methods())
	}
	params := reqs[2]["params"].(map[string]any)
	if params["state"] != "working" {
		t.Fatalf("state = %v, want working", params["state"])
	}
	if params["agent_session_id"] != "s1" || params["agent_session_path"] != "/tmp/s1.jsonl" {
		t.Fatalf("session identity missing from the report: %v", params)
	}
	if params["pane_id"] != "pane-7" || params["source"] != source || params["agent"] != agent {
		t.Fatalf("pane identity wrong: %v", params)
	}

	// A blocked report carries what the agent is waiting on, which is the
	// half of the sidebar a bare state cannot say.
	bus.ModalOpened("bash approval")
	reqs = f.waitFor(t, 4)
	last := reqs[len(reqs)-1]["params"].(map[string]any)
	if last["state"] != "blocked" || last["message"] != "bash approval" {
		t.Fatalf("blocked report = %v", last)
	}

	r.Release()
	if got := f.methods(); got[len(got)-1] != "pane.release_agent" {
		t.Fatalf("the pane was not released on exit: %v", got)
	}
}

// A session id that changes — /new, /resume, a fork — is re-announced, and one
// that has not is not re-announced on every report.
func TestSessionIsReannouncedOnlyWhenItChanges(t *testing.T) {
	f := newFakeHerdr(t)
	id := "s1"
	bus := status.New(0)
	Attach(bus, Options{Env: env(f.path), Session: func() Session { return Session{ID: id} }})
	bus.SetWorking()
	bus.SetIdle()
	f.waitFor(t, 4)
	if got := count(f.methods(), "pane.report_agent_session"); got != 1 {
		t.Fatalf("announced identity %d times for one session: %v", got, f.methods())
	}
	id = "s2"
	bus.SetWorking()
	f.waitFor(t, 6)
	if got := count(f.methods(), "pane.report_agent_session"); got != 2 {
		t.Fatalf("a new session id was not announced: %v", f.methods())
	}
}

// Outside a herdr pane nothing subscribes and no socket is opened — the gate
// is the whole reason this is safe to wire in unconditionally.
func TestInertOutsideAHerdrPane(t *testing.T) {
	dialed := false
	bus := status.New(0)
	r := Attach(bus, Options{
		Env:  func(string) string { return "" },
		Dial: func(string) (net.Conn, error) { dialed = true; return nil, nil },
	})
	bus.SetWorking()
	r.Release()
	if r.Active() || dialed {
		t.Fatal("the reporter ran outside a herdr pane")
	}
}

// The setting turns it off even inside a pane.
func TestDisabledSettingWins(t *testing.T) {
	f := newFakeHerdr(t)
	bus := status.New(0)
	r := Attach(bus, Options{Env: env(f.path), Disabled: true})
	bus.SetWorking()
	if r.Active() {
		t.Fatal("the herdr setting was ignored")
	}
}

// A dead or wedged server must cost the caller nothing. This is the property
// the whole design exists for: the publisher is the render loop's own thread
// of control, and a socket that never answers must not stall it.
func TestASlowServerDoesNotBlockThePublisher(t *testing.T) {
	f := newFakeHerdr(t)
	bus := status.New(0)
	Attach(bus, Options{
		Env:     env(f.path),
		Timeout: 20 * time.Millisecond,
		Dial: func(string) (net.Conn, error) {
			time.Sleep(500 * time.Millisecond)
			return nil, net.ErrClosed
		},
	})
	start := time.Now()
	for i := 0; i < 200; i++ {
		bus.SetWorking()
		bus.SetIdle()
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("publishing took %v — a slow herdr server reached the caller", elapsed)
	}
}

func count(list []string, want string) int {
	n := 0
	for _, s := range list {
		if s == want {
			n++
		}
	}
	return n
}
