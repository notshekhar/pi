package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/tools"
)

// Subagents: a nested run with its own context window, reporting back only
// its conclusion.
//
// The point is not parallelism, it is CONTEXT. A search that reads thirty
// files to answer one question would otherwise leave all thirty in the parent
// conversation forever. A subagent burns its own window on the noise and
// hands back the paragraph that mattered.
//
// Consequences that shape the design:
//
//   - A subagent CANNOT ask the user anything. It has no prompt of its own,
//     so its `ask` calls resolve to deny — which means it gets a read-mostly
//     tool set and is told to report rather than change things.
//   - It cannot spawn subagents. Unbounded recursion in a loop that spends
//     money is not a feature.
//   - Its transcript is not the user's. Only the final text comes back.

// TaskAgent is a named subagent persona.
type TaskAgent struct {
	Name   string
	About  string
	Prompt string
	// ReadOnly denies the mutating tools. Every shipped agent is read-only;
	// a subagent that edits files without the user seeing the work is how a
	// session becomes unreviewable.
	ReadOnly bool
}

// TaskAgents are the shipped personas.
var TaskAgents = []TaskAgent{
	{
		Name:  "explore",
		About: "Search the codebase and report what is where",
		Prompt: `You are a code explorer. Find what was asked for and report it precisely.

Report file paths with line numbers, the shape of what you found, and how the pieces connect. Quote only the lines that matter. Do not propose changes, do not editorialise, and do not pad the answer — the caller is spending its own context on your reply.

If you cannot find something, say so plainly and say where you looked.`,
		ReadOnly: true,
	},
	{
		Name:  "review",
		About: "Read a change or a file and report problems",
		Prompt: `You are a code reviewer. Report real problems, most serious first.

For each: the file and line, what breaks, and the concrete input or state that makes it break. Skip style opinions and anything you cannot substantiate from the code in front of you. If you find nothing, say so — a clean review is a useful result, an invented one is not.`,
		ReadOnly: true,
	},
}

// FindAgent looks up a persona by name.
func FindAgent(name string) (TaskAgent, bool) {
	for _, a := range TaskAgents {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return TaskAgent{}, false
}

// AgentNames lists the shipped personas.
func AgentNames() []string {
	names := make([]string, 0, len(TaskAgents))
	for _, a := range TaskAgents {
		names = append(names, a.Name)
	}
	return names
}

// TaskProgress reports a subagent's activity so the parent can show a live
// status on its row. Nil disables reporting.
type TaskProgress func(id, status string)

type taskArgs struct {
	Agent  string `json:"agent" jsonschema:"description=Which subagent to run"`
	Prompt string `json:"prompt" jsonschema:"description=The complete task; the subagent sees none of this conversation"`
}

// TaskTool returns the task tool for a parent run.
func TaskTool(parent *Run, progress TaskProgress) ai.Tool {
	return ai.NewTool("task",
		fmt.Sprintf("Delegate a self-contained question to a subagent (%s). It has a "+
			"fresh context window and returns only its conclusion, so use it when "+
			"answering would otherwise fill this conversation with files you do not "+
			"need to keep. It sees NONE of this conversation — put everything it "+
			"needs in the prompt. It cannot ask you questions or change files.",
			strings.Join(AgentNames(), ", ")),
		func(ctx context.Context, a taskArgs) (ai.ToolResult, error) {
			if ctx.Err() != nil {
				return ai.ToolError("aborted"), nil
			}
			agent, ok := FindAgent(a.Agent)
			if !ok {
				return ai.ToolErrorf("unknown agent %q; available: %s",
					a.Agent, strings.Join(AgentNames(), ", ")), nil
			}
			if strings.TrimSpace(a.Prompt) == "" {
				return ai.ToolError("prompt is empty"), nil
			}

			// The row to report against is THIS call's, which only the model
			// layer knows. A counter of our own would name a row that does
			// not exist.
			id := ai.ToolCallID(ctx)
			if progress != nil && id != "" {
				progress(id, "running "+agent.Name)
				defer progress(id, "")
			}

			text, err := runSubagent(ctx, parent, agent, a.Prompt, id, progress)
			if err != nil {
				return ai.ToolErrorf("subagent failed: %v", err), nil
			}
			if strings.TrimSpace(text) == "" {
				return ai.ToolText("(the subagent returned nothing)"), nil
			}
			return ai.ToolText(text), nil
		})
}

// runSubagent drives one nested run to completion and returns its final text.
func runSubagent(ctx context.Context, parent *Run, agent TaskAgent, prompt, id string, progress TaskProgress) (string, error) {
	model, err := config.LanguageModel(parent.Config.ForScope("subagent"))
	if err != nil {
		return "", err
	}

	toolCtx := &tools.Context{
		CWD: parent.Config.CWD,
		// A FRESH registry: read-before-edit records what has been seen this
		// run, and the subagent has seen nothing. Sharing the parent's would
		// let it edit a file it never opened.
		Registry: tools.NewRegistry(),
		Todos:    &tools.TodoList{},
	}
	toolset := subagentTools(toolCtx, agent)

	names := make([]string, 0, len(toolset))
	for _, t := range toolset {
		names = append(names, t.Name())
	}

	// The subagent inherits the parent's policy but never its asker: with
	// nobody to prompt, `ask` resolves to deny, which is the safe reading.
	sub := &Run{Config: parent.Config, Permissions: parent.Permissions}
	needsApproval, approveTool := sub.approvalHooks()

	steps := parent.Config.MaxSteps
	if steps <= 0 {
		steps = config.DefaultMaxSteps
	}

	res, err := ai.StreamText(ctx, ai.Options{
		Model: model,
		System: fmt.Sprintf("%s\n\nWorking directory: %s\n\nYou have these tools: %s.",
			agent.Prompt, parent.Config.CWD, strings.Join(names, ", ")),
		Messages:      []provider.Message{ai.UserText(prompt)},
		Tools:         toolset,
		MaxSteps:      steps,
		Reasoning:     parent.Config.Reasoning,
		NeedsApproval: needsApproval,
		ApproveTool:   approveTool,
	})
	if err != nil {
		return "", err
	}

	// The subagent's stream is drained here rather than shown: its transcript
	// is not the user's, and only the conclusion travels back.
	var lastTool string
	for part := range res.Stream {
		if v, ok := part.(ai.ToolExecuted); ok && progress != nil {
			if v.Execution.ToolName != lastTool {
				lastTool = v.Execution.ToolName
				progress(id, agent.Name+" · "+lastTool)
			}
		}
	}

	final, err := res.Final()
	if err != nil {
		return "", err
	}
	if final == nil {
		return "", fmt.Errorf("subagent produced no result")
	}
	return final.Text, nil
}

// subagentTools is the tool set a persona gets.
//
// No `task`: a subagent that can spawn subagents recurses without bound in a
// loop that spends money. No `todo`: the plan panel belongs to the
// conversation the user is watching.
func subagentTools(ctx *tools.Context, agent TaskAgent) []ai.Tool {
	set := []ai.Tool{
		tools.Read(ctx),
		tools.Ls(ctx),
		tools.Grep(ctx),
		tools.Glob(ctx),
		tools.Bash(ctx),
	}
	if !agent.ReadOnly {
		set = append(set, tools.Write(ctx), tools.Edit(ctx))
	}
	return set
}
