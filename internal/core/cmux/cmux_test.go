package cmux

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/notshekhar/pi/internal/core/hooks"
	"github.com/notshekhar/pi/internal/core/status"
)

// fakeCmux behaves the way cmux does: a push carrying _opencode_request_id
// parks until the test answers it, anything else is acked as telemetry, and
// plain lines are V1 commands.
type fakeCmux struct {
	mu       sync.Mutex
	frames   []map[string]any
	commands []string
	parked   map[string]net.Conn
	ln       net.Listener
}

func newFake(t *testing.T) (*fakeCmux, string) {
	dir, err := os.MkdirTemp("", "cx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeCmux{parked: map[string]net.Conn{}, ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f, sock
}

func (f *fakeCmux) serve(c net.Conn) {
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "{") {
			f.mu.Lock()
			f.commands = append(f.commands, line)
			f.mu.Unlock()
			c.Write([]byte("OK\n"))
			continue
		}
		var frame map[string]any
		json.Unmarshal([]byte(line), &frame)
		f.mu.Lock()
		f.frames = append(f.frames, frame)
		ev := frame["params"].(map[string]any)["event"].(map[string]any)
		if id, ok := ev["_opencode_request_id"].(string); ok {
			f.parked[id] = c
			f.mu.Unlock()
			continue
		}
		f.mu.Unlock()
		c.Write([]byte(`{"ok":true,"result":{"status":"acknowledged"}}` + "\n"))
	}
}

func (f *fakeCmux) events() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, fr := range f.frames {
		out = append(out, fr["params"].(map[string]any)["event"].(map[string]any))
	}
	return out
}

func (f *fakeCmux) cmds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.commands...)
}

// answer resolves the one parked card.
func (f *fakeCmux) answer(t *testing.T, decision string) {
	t.Helper()
	until(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.parked) == 1 })
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, c := range f.parked {
		c.Write([]byte(`{"ok":true,"result":{"status":"resolved","decision":` + decision + `}}` + "\n"))
		delete(f.parked, id)
	}
}

func until(t *testing.T, ok func() bool) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(end) {
			t.Fatal("timed out waiting for cmux")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func envFor(sock string) func(string) string {
	vars := map[string]string{
		"CMUX_SURFACE_ID": "surf-1", "CMUX_WORKSPACE_ID": "ws-1",
		"CMUX_SOCKET_PATH": sock, "CMUX_SOCKET_CAPABILITY": "cap-xyz",
	}
	return func(k string) string { return vars[k] }
}

func attach(t *testing.T, sock string, bus *status.Bus, sess *string) *Reporter {
	r := Attach(bus, Options{
		Env: envFor(sock),
		Session: func() (Session, bool) {
			if *sess == "" {
				return Session{}, false
			}
			return Session{ID: *sess}, true
		},
		CWD:          func() string { return "/work/pi-agent" },
		DecisionWait: time.Second,
	})
	t.Cleanup(r.Release)
	return r
}

func TestForwardsHookEventsAsFeedTelemetry(t *testing.T) {
	f, sock := newFake(t)
	sess := "S1"
	attach(t, sock, nil, &sess)
	hooks.Notify(hooks.Context{Event: hooks.SessionStart})
	hooks.Notify(hooks.Context{Event: hooks.UserPromptSubmit, Prompt: "ship the cmux bridge"})
	hooks.Notify(hooks.Context{Event: hooks.PreToolUse, ToolName: "bash", ToolInput: map[string]any{"command": "go test"}})
	hooks.Notify(hooks.Context{Event: hooks.PostToolUse, ToolName: "bash", ToolOut: "12 pass"})
	hooks.Notify(hooks.Context{Event: hooks.Stop, LastAssistantMessage: "done"})
	until(t, func() bool { return len(f.events()) == 5 })
	ev := f.events()
	names := []string{}
	for _, e := range ev {
		names = append(names, e["hook_event_name"].(string))
		if e["_source"] != "pi" || e["session_id"] != "pi-S1" || e["surface_id"] != "surf-1" || e["workspace_id"] != "ws-1" {
			t.Errorf("bad envelope: %v", e)
		}
	}
	if strings.Join(names, ",") != "SessionStart,UserPromptSubmit,PreToolUse,PostToolUse,Stop" {
		t.Errorf("order %v", names)
	}
	if ev[3]["tool_result"] != "12 pass" || ev[4]["last_assistant_message"] != "done" {
		t.Errorf("payloads %v / %v", ev[3], ev[4])
	}
	ctx := ev[2]["context"].(map[string]any)
	if ctx["lastUserMessage"] != "ship the cmux bridge" {
		t.Errorf("context %v", ctx)
	}
	// Telemetry never parks.
	for _, fr := range f.frames {
		if fr["params"].(map[string]any)["wait_timeout_seconds"].(float64) != 0 {
			t.Errorf("telemetry waited: %v", fr)
		}
	}
	// The turn-complete notification, with the capability prefix.
	until(t, func() bool {
		for _, c := range f.cmds() {
			if strings.HasPrefix(c, "_cmux_capability_v1 cap-xyz notify_target ws-1 surf-1 pi|pi-agent|done|c=turn-complete;p=0") {
				return true
			}
		}
		return false
	})
}

func TestEventsBeforeTheSessionAreReplayedIntoIt(t *testing.T) {
	f, sock := newFake(t)
	sess := ""
	attach(t, sock, nil, &sess)
	hooks.Notify(hooks.Context{Event: hooks.SessionStart})
	time.Sleep(100 * time.Millisecond)
	if len(f.events()) != 0 {
		t.Fatal("an event went out with no session to file it under")
	}
	sess = "S2"
	hooks.Notify(hooks.Context{Event: hooks.UserPromptSubmit, Prompt: "go"})
	until(t, func() bool { return len(f.events()) == 2 })
	for _, e := range f.events() {
		if e["session_id"] != "pi-S2" {
			t.Errorf("filed under %v", e["session_id"])
		}
	}
}

func TestStatusChipFollowsTheAgent(t *testing.T) {
	f, sock := newFake(t)
	sess := "S3"
	bus := status.New(0)
	attach(t, sock, bus, &sess)
	has := func(sub string) func() bool {
		return func() bool {
			for _, c := range f.cmds() {
				if strings.Contains(c, sub) {
					return true
				}
			}
			return false
		}
	}
	until(t, has("set_status pi Idle --icon=pause.circle.fill --color=#8E8E93 --tab=ws-1 --panel=surf-1"))
	bus.SetWorking()
	until(t, has("set_status pi Working --icon=bolt.fill --color=#4C8DFF --tab=ws-1 --panel=surf-1"))
	done := bus.ModalOpened("bash approval")
	until(t, has("set_status pi bash approval --icon=bell.fill --color=#4C8DFF --priority=100"))
	until(t, has("notify_target ws-1 surf-1 pi|pi-agent|bash approval|c=needs-permission;p=1"))
	done()
}

func TestAMultiLineAnswerCannotBreakTheWire(t *testing.T) {
	if got := wireText("line one\nline two | still body\n--flag", 240); got != "line one line two / still body —flag" {
		t.Errorf("wireText = %q", got)
	}
}

func TestTodoAlsoGoesOutAsTodoWrite(t *testing.T) {
	f, sock := newFake(t)
	sess := "S4"
	attach(t, sock, nil, &sess)
	todos := []any{map[string]any{"content": "a", "status": "pending"}}
	hooks.Notify(hooks.Context{Event: hooks.PostToolUse, ToolName: "todo", ToolInput: map[string]any{"todos": todos}})
	until(t, func() bool { return len(f.events()) == 2 })
	if f.events()[1]["hook_event_name"] != "TodoWrite" {
		t.Errorf("second event %v", f.events()[1])
	}
}

func TestApprovalParksUntilCmuxAnswers(t *testing.T) {
	f, sock := newFake(t)
	sess := "S5"
	r := attach(t, sock, nil, &sess)
	got := make(chan *Decision, 1)
	go func() {
		got <- r.RequestApproval(context.Background(), ApprovalRequest{Kind: "bash", ToolName: "bash", Body: "rm -rf build"})
	}()
	f.answer(t, `{"kind":"permission","mode":"once"}`)
	d := <-got
	if d == nil || d.Mode != "once" {
		t.Fatalf("decision %v", d)
	}
	ev := f.events()[0]
	if ev["hook_event_name"] != "PermissionRequest" || ev["tool_input"].(map[string]any)["command"] != "rm -rf build" {
		t.Errorf("card %v", ev)
	}
	if f.frames[0]["params"].(map[string]any)["wait_timeout_seconds"].(float64) != 1 {
		t.Errorf("card did not park")
	}
}

func TestPlanApprovalIsAnExitPlanModeCard(t *testing.T) {
	f, sock := newFake(t)
	sess := "S6"
	r := attach(t, sock, nil, &sess)
	got := make(chan *Decision, 1)
	go func() {
		got <- r.RequestApproval(context.Background(), ApprovalRequest{Kind: "exit-plan", Body: "1. do it"})
	}()
	f.answer(t, `{"kind":"exit_plan","mode":"autoAccept"}`)
	if d := <-got; d == nil || d.Mode != "autoAccept" {
		t.Fatalf("decision %v", d)
	}
	ev := f.events()[0]
	if ev["hook_event_name"] != "ExitPlanMode" || ev["context"].(map[string]any)["permissionMode"] != "plan" {
		t.Errorf("card %v", ev)
	}
}

func TestQuestionsComeBackAsSelections(t *testing.T) {
	f, sock := newFake(t)
	sess := "S7"
	r := attach(t, sock, nil, &sess)
	got := make(chan []string, 1)
	go func() {
		got <- r.RequestQuestions(context.Background(), []Question{{Header: "h", Question: "which?", Options: []Option{{Label: "a"}, {Label: "b"}}}})
	}()
	f.answer(t, `{"kind":"question","selections":["b"]}`)
	if s := <-got; len(s) != 1 || s[0] != "b" {
		t.Fatalf("selections %v", s)
	}
}

func TestTheTUIAnsweringFirstWithdrawsTheCard(t *testing.T) {
	_, sock := newFake(t)
	sess := "S8"
	r := attach(t, sock, nil, &sess)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan *Decision, 1)
	go func() { got <- r.RequestApproval(ctx, ApprovalRequest{Kind: "bash", Body: "ls"}) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case d := <-got:
		if d != nil {
			t.Errorf("withdrawn card answered %v", d)
		}
	case <-time.After(time.Second / 2):
		t.Fatal("withdrawing did not end the request")
	}
}

func TestNobodyAnsweringTimesOut(t *testing.T) {
	_, sock := newFake(t)
	sess := "S9"
	r := Attach(nil, Options{Env: envFor(sock), Session: func() (Session, bool) { return Session{ID: sess}, true },
		DecisionWait: time.Second, ConnectTimeout: 100 * time.Millisecond})
	defer r.Release()
	start := time.Now()
	if d := r.RequestApproval(context.Background(), ApprovalRequest{Kind: "bash", Body: "ls"}); d != nil {
		t.Errorf("got %v", d)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("the timeout did not hold")
	}
}

func TestInertOutsideCmux(t *testing.T) {
	r := Attach(status.New(0), Options{Env: func(string) string { return "" }})
	if r.Active() {
		t.Fatal("active outside cmux")
	}
	if r.RequestApproval(context.Background(), ApprovalRequest{}) != nil || r.RequestQuestions(context.Background(), nil) != nil {
		t.Error("inert reporter answered")
	}
	r.Release()
	off := Attach(nil, Options{Env: envFor("/nonexistent"), Disabled: true})
	if off.Active() {
		t.Error("the setting did not turn it off")
	}
}

func TestDeadSocketResolvesWithoutHanging(t *testing.T) {
	sess := "S10"
	r := Attach(status.New(0), Options{Env: envFor("/tmp/no-such-cmux.sock"),
		Session: func() (Session, bool) { return Session{ID: sess}, true }})
	hooks.Notify(hooks.Context{Event: hooks.SessionStart})
	start := time.Now()
	if d := r.RequestApproval(context.Background(), ApprovalRequest{Kind: "bash", Body: "ls"}); d != nil {
		t.Errorf("got %v", d)
	}
	r.Release()
	if time.Since(start) > 3*time.Second {
		t.Error("a dead cmux cost time")
	}
}
