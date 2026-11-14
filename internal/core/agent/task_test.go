package agent

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/tools"
)

// A fork gets exactly the parent's tools; a named agent can only narrow
// them; task never reaches a subagent.
func TestResolveSubagentTools(t *testing.T) {
	candidates := []string{"read", "write", "edit", "bash", "ls", "grep", "find", "sql", "mcp__x__y"}
	parent := []string{"read", "write", "edit", "bash", "ls", "grep", "find", "sql", "mcp__x__y", "task"}
	if got := resolveSubagentTools(candidates, nil, parent); !slices.Equal(got, candidates) {
		t.Errorf("fork = %v", got)
	}
	if got := resolveSubagentTools(candidates, []string{"read", "grep", "task"}, parent); !slices.Equal(got, []string{"read", "grep"}) {
		t.Errorf("named = %v", got)
	}
	readOnlyParent := []string{"read", "ls", "bash"}
	if got := resolveSubagentTools(candidates, []string{"read", "write", "bash"}, readOnlyParent); !slices.Equal(got, []string{"read", "bash"}) {
		t.Errorf("a named agent widened the parent: %v", got)
	}
}

// taskStep is a scripted Anthropic step calling the task tool.
func taskStep(id string, input map[string]string) string { return toolCallStep("task", id, input) }

// toolCallStep is a scripted Anthropic step calling one tool.
func toolCallStep(name, id string, input any) string {
	raw, _ := json.Marshal(input, jsonopt.Write)
	quoted, _ := json.Marshal(string(raw), jsonopt.Write)
	return fmt.Sprintf(`event: message_start
data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"claude-x","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"%s","name":"%s","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%s}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`, id, name, quoted)
}

func systemText(body map[string]any) string {
	raw, _ := json.Marshal(body["system"], jsonopt.Write)
	return string(raw)
}

func requestTools(body map[string]any) []string {
	var names []string
	list, _ := body["tools"].([]any)
	for _, tl := range list {
		if m, ok := tl.(map[string]any); ok {
			names = append(names, fmt.Sprint(m["name"]))
		}
	}
	return names
}

// A delegated run: the subagent is a fork with the run rules and no task
// tool, its report reaches the parent, and the run is saved under the call's
// id — which a later task call continues with follow_up.
func TestTaskForksAndFollowUpContinues(t *testing.T) {
	r, g, probe := newTurn(t,
		taskStep("toolu_task1", map[string]string{"prompt": "find the config loader"}),
		textStep, // the subagent's answer: "done"
		textStep, // the parent's answer
	)
	r.Tools = &tools.Context{CWD: r.Config.CWD, Registry: tools.NewRegistry(), Todos: &tools.TodoList{}}
	if _, err := r.RunTurn(context.Background(), TurnOptions{Input: "go", Emit: probe.emit}); err != nil {
		t.Fatal(err)
	}
	bodies := g.requests()
	if len(bodies) != 3 {
		t.Fatalf("requests = %d, want 3", len(bodies))
	}
	sub := bodies[1]
	if !strings.Contains(systemText(sub), "You are a subagent launched by a main agent") {
		t.Error("the subagent did not get the run rules")
	}
	if names := requestTools(sub); slices.Contains(names, "task") || !slices.Contains(names, "read") {
		t.Errorf("subagent tools = %v", names)
	}
	var saved *session.Entry
	for _, e := range r.Session.CurrentBranch() {
		if e.Type == session.TypeSubagent {
			saved = &e
		}
	}
	if saved == nil || extraString(*saved, "toolCallId") != "toolu_task1" || saved.Result != "done" || saved.Agent != "default" {
		t.Fatalf("saved run = %+v", saved)
	}
	if !strings.Contains(fmt.Sprint(bodies[2]["messages"]), "done") {
		t.Error("the parent never saw the subagent's report")
	}

	// Second turn: continue that run.
	g.mu.Lock()
	g.bodies, g.script = nil, []string{
		taskStep("toolu_task2", map[string]string{"prompt": "and where is it called?", "follow_up": "toolu_task1"}),
		textStep, textStep,
	}
	g.mu.Unlock()
	if _, err := r.RunTurn(context.Background(), TurnOptions{Input: "more", Emit: probe.emit}); err != nil {
		t.Fatal(err)
	}
	followUp := fmt.Sprint(g.requests()[1]["messages"])
	for _, want := range []string{"find the config loader", "done", "and where is it called?"} {
		if !strings.Contains(followUp, want) {
			t.Errorf("follow-up request lacks %q:\n%s", want, followUp)
		}
	}
}
