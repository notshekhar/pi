package agent

import (
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/core/tools"
)

func TestFindAgent(t *testing.T) {
	if _, ok := FindAgent("explore"); !ok {
		t.Error("explore should exist")
	}
	// Names are matched case-insensitively — the model will not be careful.
	if _, ok := FindAgent("EXPLORE"); !ok {
		t.Error("agent lookup should ignore case")
	}
	if _, ok := FindAgent("nonexistent"); ok {
		t.Error("unknown agents must not resolve")
	}
}

func TestAgentNames(t *testing.T) {
	names := AgentNames()
	if len(names) != len(TaskAgents) {
		t.Fatalf("got %d names for %d agents", len(names), len(TaskAgents))
	}
	for _, name := range names {
		if name == "" {
			t.Error("an agent has no name")
		}
	}
}

func TestEveryAgentIsFullyDescribed(t *testing.T) {
	for _, a := range TaskAgents {
		if a.Name == "" || a.About == "" || a.Prompt == "" {
			t.Errorf("agent %+v is missing a field", a)
		}
	}
}

// A subagent that edits files the user never sees makes a session
// unreviewable, so every shipped persona is read-only.
func TestShippedAgentsAreReadOnly(t *testing.T) {
	for _, a := range TaskAgents {
		if !a.ReadOnly {
			t.Errorf("agent %q is not read-only", a.Name)
		}
	}
}

// A subagent that can spawn subagents recurses without bound in a loop that
// spends money.
func TestSubagentCannotDelegate(t *testing.T) {
	ctx := &tools.Context{CWD: "/repo", Registry: tools.NewRegistry(), Todos: &tools.TodoList{}}
	for _, agent := range TaskAgents {
		for _, tool := range subagentTools(ctx, agent) {
			if tool.Name() == "task" {
				t.Fatalf("agent %q can spawn subagents", agent.Name)
			}
		}
	}
}

// The plan panel belongs to the conversation the user is watching.
func TestSubagentHasNoTodoTool(t *testing.T) {
	ctx := &tools.Context{CWD: "/repo", Registry: tools.NewRegistry(), Todos: &tools.TodoList{}}
	for _, tool := range subagentTools(ctx, TaskAgents[0]) {
		if tool.Name() == "todo" {
			t.Error("a subagent should not drive the plan panel")
		}
	}
}

func TestReadOnlyAgentHasNoMutatingTools(t *testing.T) {
	ctx := &tools.Context{CWD: "/repo", Registry: tools.NewRegistry(), Todos: &tools.TodoList{}}
	readOnly := subagentTools(ctx, TaskAgent{Name: "x", ReadOnly: true})
	for _, tool := range readOnly {
		if tool.Name() == "write" || tool.Name() == "edit" {
			t.Errorf("a read-only agent has %s", tool.Name())
		}
	}
	// And a writable one does get them, so the flag is what decides.
	writable := subagentTools(ctx, TaskAgent{Name: "x"})
	if len(writable) <= len(readOnly) {
		t.Error("a writable agent should get more tools than a read-only one")
	}
}

// Investigation is the whole job, so reading must always be available.
func TestSubagentCanRead(t *testing.T) {
	ctx := &tools.Context{CWD: "/repo", Registry: tools.NewRegistry(), Todos: &tools.TodoList{}}
	have := map[string]bool{}
	for _, tool := range subagentTools(ctx, TaskAgents[0]) {
		have[tool.Name()] = true
	}
	for _, want := range []string{"read", "ls", "grep", "glob"} {
		if !have[want] {
			t.Errorf("a subagent cannot %s", want)
		}
	}
}

// The description has to tell the model the one thing it will otherwise get
// wrong: the subagent sees none of this conversation.
func TestTaskToolDescriptionWarnsAboutIsolation(t *testing.T) {
	tool := TaskTool(&Run{}, nil)
	desc := strings.ToLower(tool.Description())
	if !strings.Contains(desc, "sees none of this conversation") {
		t.Errorf("description does not warn about isolation:\n%s", tool.Description())
	}
	for _, name := range AgentNames() {
		if !strings.Contains(desc, name) {
			t.Errorf("description does not list agent %q", name)
		}
	}
}
