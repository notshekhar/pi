package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/notshekhar/pi/internal/ai/provider"
)

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// managerWith starts a manager over a mutable list of configs.
func managerWith(t *testing.T, configs *[]ServerConfig) *Manager {
	t.Helper()
	t.Setenv("LOOP_MCP_HEALTH_INTERVAL_MS", "0")
	m := NewManager()
	t.Cleanup(m.Close)
	m.Init(context.Background(), t.TempDir(), func() []ServerConfig { return append([]ServerConfig{}, *configs...) })
	return m
}

func toolNamed(m *Manager, name string) *bridgedTool {
	for _, tool := range m.Tools() {
		if tool.Name() == name {
			return tool.(*bridgedTool)
		}
	}
	return nil
}

func state(m *Manager, name string) State {
	s, _ := m.Server(name)
	return s.State
}

// One broken server never takes the others down.
func TestManagerIsolatesABrokenServer(t *testing.T) {
	configs := []ServerConfig{fakeConfig(t, "fake"), {Name: "broken", Command: "/nonexistent/server"}}
	m := managerWith(t, &configs)
	if state(m, "fake") != StateReady || state(m, "broken") != StateError {
		t.Fatalf("status = %+v", m.Status())
	}
	if toolNamed(m, "mcp__fake__echo") == nil {
		t.Fatalf("tools = %v", toolNames(m.Tools()))
	}
}

// A server that dies is withdrawn at once and brought back on its own.
func TestManagerReconnectsADroppedServer(t *testing.T) {
	old := reconnectBase
	reconnectBase = 20 * time.Millisecond
	t.Cleanup(func() { reconnectBase = old })
	configs := []ServerConfig{fakeConfig(t, "fake")}
	m := managerWith(t, &configs)
	die := toolNamed(m, "mcp__fake__die")
	res, _ := die.Execute(context.Background(), json.RawMessage(`{}`))
	if res.Output() == nil {
		t.Fatal("no result for a dying call")
	}
	eventually(t, "the server to come back", func() bool { return state(m, "fake") == StateReady })
	echo := toolNamed(m, "mcp__fake__echo")
	out, _ := echo.Execute(context.Background(), json.RawMessage(`{"text":"back"}`))
	if textResult(out) != "back" {
		t.Errorf("after reconnect: %v", out.Output())
	}
}

// list_changed re-lists the server's tools into the live set.
func TestManagerFollowsListChanged(t *testing.T) {
	configs := []ServerConfig{fakeConfig(t, "fake")}
	m := managerWith(t, &configs)
	changes := make(chan struct{}, 16)
	m.OnChange(func() { changes <- struct{}{} })
	_, _ = toolNamed(m, "mcp__fake__grow").Execute(context.Background(), json.RawMessage(`{}`))
	eventually(t, "the new tool", func() bool { return toolNamed(m, "mcp__fake__extra") != nil })
	if len(changes) == 0 {
		t.Error("no change was announced")
	}
}

// Disabling applies at once: the server closes and its tools leave. Enabling
// brings it back. A removed entry is forgotten.
func TestManagerAppliesEnableDisableAndRemove(t *testing.T) {
	configs := []ServerConfig{fakeConfig(t, "fake")}
	m := managerWith(t, &configs)
	configs[0].SetDisabled(true)
	m.Reconnect(context.Background(), "fake")
	if state(m, "fake") != StateDisabled || len(m.Tools()) != 0 {
		t.Fatalf("after disable: %v, %d tools", state(m, "fake"), len(m.Tools()))
	}
	configs[0].SetDisabled(false)
	m.Reconnect(context.Background(), "fake")
	if state(m, "fake") != StateReady {
		t.Fatalf("after enable: %v", state(m, "fake"))
	}
	configs = nil
	m.Reconnect(context.Background(), "fake")
	if _, ok := m.Server("fake"); ok || len(m.Tools()) != 0 {
		t.Error("a removed server was kept")
	}
}

// A full reconnect picks up added servers and drops removed ones.
func TestManagerFullReconnectResyncs(t *testing.T) {
	configs := []ServerConfig{fakeConfig(t, "one")}
	m := managerWith(t, &configs)
	configs = []ServerConfig{fakeConfig(t, "two")}
	m.Reconnect(context.Background(), "")
	names := []string{}
	for _, s := range m.Status() {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"two"}) || state(m, "two") != StateReady {
		t.Errorf("servers = %v", names)
	}
}

// Two names that share a tool prefix are refused rather than overwriting
// each other's tools.
func TestManagerRefusesAPrefixClash(t *testing.T) {
	configs := []ServerConfig{fakeConfig(t, "my-fs")}
	m := managerWith(t, &configs)
	clash := fakeConfig(t, "my.fs")
	configs = append(configs, clash)
	m.Reconnect(context.Background(), "my.fs")
	if s, _ := m.Server("my.fs"); s.State != StateError || toolNamed(m, "mcp__my_fs__echo") == nil {
		t.Errorf("clash = %+v", s)
	}
}

// A tool called after its server is gone says so instead of hanging.
func TestBridgedToolOnAGoneServer(t *testing.T) {
	configs := []ServerConfig{fakeConfig(t, "fake")}
	m := managerWith(t, &configs)
	echo := toolNamed(m, "mcp__fake__echo")
	m.Forget("fake")
	out, err := echo.Execute(context.Background(), json.RawMessage(`{"text":"x"}`))
	if err != nil || !isToolError(out) {
		t.Errorf("call on a forgotten server: %v %v", out.Output(), err)
	}
}

// A tool policy filters on the server's own names.
func TestManagerAppliesToolPolicy(t *testing.T) {
	cfg := fakeConfig(t, "fake")
	cfg.AllowedTools = []string{"echo"}
	configs := []ServerConfig{cfg}
	m := managerWith(t, &configs)
	if names := toolNames(m.Tools()); !slices.Equal(names, []string{"mcp__fake__echo"}) {
		t.Errorf("tools = %v", names)
	}
}

func textResult(r interface {
	Output() provider.ToolResultOutput
}) string {
	if out, ok := r.Output().(provider.ToolOutputText); ok {
		return out.Value
	}
	return ""
}

func isToolError(r interface {
	Output() provider.ToolResultOutput
}) bool {
	_, ok := r.Output().(provider.ToolOutputErrorText)
	return ok
}
