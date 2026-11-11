package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/mcp"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/shells"
	"github.com/notshekhar/pi/internal/core/tools"
)

// testRun is a run in a scratch home with the given settings.
func testRun(t *testing.T, settings map[string]any) *Run {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	raw, _ := json.Marshal(settings)
	if err := os.MkdirAll(filepath.Join(home, ".loop"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".loop", "settings.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	return &Run{
		Config:  config.Config{CWD: cwd},
		Session: session.New("test/model", cwd),
		Tools:   &tools.Context{CWD: cwd, Registry: tools.NewRegistry(), Todos: &tools.TodoList{}},
	}
}

func approveAll(context.Context, string, map[string]any, string) bool { return true }

// loop's defaults: the file tools and task — every opt-in tool stays out,
// and with nobody to approve a switch there are no plan-mode tools.
func TestDefaultToolset(t *testing.T) {
	r := testRun(t, map[string]any{})
	want := []string{"read", "write", "edit", "bash", "ls", "grep", "find", "sql", "task"}
	if got := r.ToolNames(); !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
}

// Each opt-in setting adds its tool, and the prompt explains the tools it
// added — and only those.
func TestOptInToolsAndTheirNotes(t *testing.T) {
	r := testRun(t, map[string]any{"backgroundShells": true, "todos": true, "webSearch": true, "askUser": true})
	r.Ask = approveAll
	r.Tools.Ask = func(context.Context, []tools.AskQuestion) []tools.AskAnswer { return nil }
	toolset := r.Toolset()
	got := toolNames(toolset)
	want := []string{"read", "write", "edit", "bash", "ls", "grep", "find", "sql", "shells", "websearch", "task", "todo", "ask", "enter_plan_mode"}
	if !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
	system := r.SystemPrompt(toolset)
	for _, note := range []string{BackgroundShellsNote, TodoNote, "Subagents (task tool)"} {
		if !strings.Contains(system, note) {
			t.Errorf("prompt lacks %q", note[:30])
		}
	}
	if strings.Contains(system, "PLAN MODE IS ACTIVE") {
		t.Error("plan-mode note outside plan mode")
	}

	bare := testRun(t, map[string]any{"subagents": false})
	system = bare.SystemPrompt(bare.Toolset())
	for _, note := range []string{BackgroundShellsNote, TodoNote, "Subagents (task tool)"} {
		if strings.Contains(system, note) {
			t.Errorf("prompt explains a tool it does not have: %q", note[:30])
		}
	}
}

// A restricted agent keeps exactly its list, and an agent allowed bash but
// not write/edit gets read-only bash.
func TestRestrictedAgent(t *testing.T) {
	r := testRun(t, map[string]any{"backgroundShells": true})
	r.Ask = approveAll
	r.AllowedTools = []string{"read", "ls", "grep", "find", "bash", "plan"}
	want := []string{"read", "bash", "ls", "grep", "find", "plan"}
	if got := r.ToolNames(); !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
	if !r.Tools.ReadOnlyBash {
		t.Error("a bash-but-no-write agent got writable bash")
	}
	r.AllowedTools = []string{"read", "write", "edit", "bash"}
	r.Toolset()
	if r.Tools.ReadOnlyBash {
		t.Error("an agent allowed write got read-only bash")
	}
}

// In plan mode the main agent asks its own way out; with nobody to ask it
// delivers with `plan` instead.
func TestPlanModeTools(t *testing.T) {
	r := testRun(t, map[string]any{})
	r.Ask = approveAll
	r.SetPlanning(true)
	toolset := r.Toolset()
	names := toolNames(toolset)
	if !slices.Contains(names, "exit_plan_mode") || slices.Contains(names, "enter_plan_mode") || slices.Contains(names, "plan") {
		t.Errorf("tools = %v", names)
	}
	if !strings.Contains(r.SystemPrompt(toolset), "PLAN MODE IS ACTIVE") {
		t.Error("plan-mode note missing")
	}

	r.Ask = nil
	if names := r.ToolNames(); !slices.Contains(names, "plan") || slices.Contains(names, "exit_plan_mode") {
		t.Errorf("headless plan mode tools = %v", names)
	}
}

// The agent switching plan mode (exit_plan_mode) reaches the UI.
func TestPlanModeSwitchIsReported(t *testing.T) {
	r := testRun(t, map[string]any{})
	var seen []bool
	r.OnPlanMode = func(on bool) { seen = append(seen, on) }
	r.SetPlanning(true)
	r.SetPlanning(true)
	r.SetPlanning(false)
	if !slices.Equal(seen, []bool{true, false}) {
		t.Errorf("reported %v, want one report per change", seen)
	}
}

// An agent's prompt replaces the base prompt; the environment stays.
func TestPersonaReplacesTheBasePrompt(t *testing.T) {
	r := testRun(t, map[string]any{})
	r.Persona = "You review code. Do not fix it."
	system := r.SystemPrompt(r.Toolset())
	if !strings.HasPrefix(system, r.Persona) || strings.Contains(system, "You are pi") {
		t.Errorf("prompt does not start with the persona:\n%.200s", system)
	}
	if !strings.Contains(system, "Working directory: "+r.Config.CWD) {
		t.Error("environment block missing")
	}
}

func TestMemoryBlockFollowsTheSetting(t *testing.T) {
	on := testRun(t, map[string]any{})
	if !strings.Contains(on.SystemPrompt(on.Toolset()), "<memory>") {
		t.Error("memory block missing by default")
	}
	off := testRun(t, map[string]any{"memory": false})
	if strings.Contains(off.SystemPrompt(off.Toolset()), "<memory>") {
		t.Error("memory block present with memory off")
	}
}

func TestPlanDeliveredStopsTheTurn(t *testing.T) {
	call := func(plan string) ai.Step {
		input, _ := json.Marshal(map[string]string{"plan": plan})
		return ai.Step{ToolCalls: []ai.ToolCall{{ToolName: "plan", Input: input}}}
	}
	if planDelivered(call("## Plan")) {
		t.Error("a stub plan stopped the turn")
	}
	if !planDelivered(call(strings.Repeat("A real step of the plan.\n", 20))) {
		t.Error("a delivered plan did not stop the turn")
	}
	if stopWhen([]ai.Tool{tools.Read(&tools.Context{})}) != nil {
		t.Error("a turn without the plan tool got a plan stop")
	}
}

// A todo nudge rides one request and is not left in the history.
func TestTodoNudgeIsOneStep(t *testing.T) {
	r := testRun(t, map[string]any{"todos": true})
	prepare := r.stepNotices(r.Toolset())
	if prepare == nil {
		t.Fatal("no PrepareStep with the todo tool on")
	}
	var steps []ai.Step
	for range 15 {
		steps = append(steps, ai.Step{ToolCalls: []ai.ToolCall{{ToolName: "read"}}})
	}
	history := []provider.Message{ai.UserText("go")}
	out, err := prepare(context.Background(), ai.StepContext{Messages: history, Steps: steps})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 2 || !strings.Contains(messageText(out.Messages[1]), "todo") {
		t.Fatalf("messages = %+v", out.Messages)
	}
	if len(history) != 1 {
		t.Error("the nudge was written into the history")
	}
	again, _ := prepare(context.Background(), ai.StepContext{Messages: history, Steps: steps})
	if again.Messages != nil {
		t.Error("the same nudge was sent twice")
	}
}

func messageText(m provider.Message) string {
	raw, _ := json.Marshal(m)
	return string(raw)
}

// A background shell that exits is announced once, on the next request —
// "you will be told when it exits" is the bash tool's promise.
func TestShellExitNoticeIsOneStep(t *testing.T) {
	shells.ResetForTests()
	t.Cleanup(func() { shells.KillAll(); shells.ResetForTests() })
	r := testRun(t, map[string]any{"backgroundShells": true})
	toolset := r.Toolset()
	prepare := r.stepNotices(toolset)
	bash := toolset[slices.IndexFunc(toolset, func(tl ai.Tool) bool { return tl.Name() == "bash" })]
	if _, err := bash.Execute(context.Background(), json.RawMessage(`{"command":"exit 3","run_in_background":true}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for shells.List(r.Session.ID)[0].Status == "running" {
		if time.Now().After(deadline) {
			t.Fatal("the shell never exited")
		}
		time.Sleep(10 * time.Millisecond)
	}
	history := []provider.Message{ai.UserText("go")}
	out, err := prepare(context.Background(), ai.StepContext{Messages: history})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 2 || !strings.Contains(messageText(out.Messages[1]), "exited with code 3") {
		t.Fatalf("messages = %+v", out.Messages)
	}
	if again, _ := prepare(context.Background(), ai.StepContext{Messages: history}); again.Messages != nil {
		t.Error("the exit was announced twice")
	}
}

// Past the threshold (here forced by the setting) MCP tools are reached
// through mcp_tools, and the prompt says so.
func TestMCPToolSearchMode(t *testing.T) {
	mcpTools := []ai.Tool{tools.Read(&tools.Context{})}
	listed := testRun(t, map[string]any{})
	listed.MCPTools = func() []ai.Tool { return mcpTools }
	if names := listed.ToolNames(); !slices.Contains(names, "read") || slices.Contains(names, "mcp_tools") {
		t.Errorf("listed mode tools = %v", names)
	}
	search := testRun(t, map[string]any{"mcpToolSearch": true})
	search.MCPTools = func() []ai.Tool { return mcpTools }
	search.MCPServers = func() []MCPServer { return []MCPServer{{Name: "fs", Instructions: "paths are relative"}} }
	toolset := search.Toolset()
	if !slices.Contains(toolNames(toolset), "mcp_tools") {
		t.Fatalf("search mode tools = %v", toolNames(toolset))
	}
	system := search.SystemPrompt(toolset)
	if !strings.Contains(system, "MCP tools (from fs) are NOT listed") || !strings.Contains(system, `From MCP server "fs"`) {
		t.Error("search-mode prompt notes missing")
	}
}

type resourceList []mcp.Resource

func (l resourceList) Resources() []mcp.Resource                 { return l }
func (l resourceList) ResourceTemplates() []mcp.ResourceTemplate { return nil }
func (l resourceList) ReadResource(context.Context, string, string) ([]mcp.ResourcePart, error) {
	return nil, nil
}

// mcp_resource rides along only while a server publishes resources, and
// never for a restricted agent.
func TestMCPResourceOnlyWithResources(t *testing.T) {
	r := testRun(t, map[string]any{})
	r.MCPResources = resourceList(nil)
	if slices.Contains(r.ToolNames(), "mcp_resource") {
		t.Error("mcp_resource offered with no resources")
	}
	r.MCPResources = resourceList{{Server: "docs", URI: "doc://a", Name: "a"}}
	if !slices.Contains(r.ToolNames(), "mcp_resource") {
		t.Error("mcp_resource missing with resources published")
	}
	r.UseAgent("plan")
	if slices.Contains(r.ToolNames(), "mcp_resource") {
		t.Error("a restricted agent got mcp_resource")
	}
}
