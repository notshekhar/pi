package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/tools"
)

// Consume drains the stream. The TUI renders it; the one-shot path uses
// the tui Printer instead.
type Consume func(stream <-chan ai.StreamPart) error

// TurnStream is Turn with the renderer injected, so main.go can glue the
// TUI to the model without this package importing the UI.
func (r *Run) TurnStream(ctx context.Context, prompt string, consume Consume) (*ai.Result, error) {
	model, err := config.LanguageModel(r.Config)
	if err != nil {
		return nil, err
	}

	if err := r.Session.Add(ai.UserText(prompt)); err != nil {
		return nil, err
	}

	// `task` is added here rather than in tools.All, because it needs the Run
	// it belongs to — and because only a PARENT run gets it. A subagent
	// builds its own set without it, so delegation cannot recurse.
	toolset := r.Toolset()
	names := r.ToolNames()

	needsApproval, approveTool := r.approvalHooks()
	res, err := ai.StreamText(ctx, ai.Options{
		Model:         model,
		System:        SystemPromptFor(r.Config.CWD, names, r.Planning) + r.personaBlock() + r.extensionBlock(),
		Messages:      r.Session.Messages,
		Tools:         toolset,
		MaxSteps:      r.Config.MaxSteps,
		Reasoning:     r.Config.Reasoning,
		NeedsApproval: needsApproval,
		ApproveTool:   approveTool,
	})
	if err != nil {
		return nil, err
	}

	if err := consume(res.Stream); err != nil {
		return nil, err
	}

	final, err := res.Final()
	if err != nil {
		return nil, err
	}
	if final == nil {
		return nil, fmt.Errorf("agent: run produced no result")
	}

	added := final.Messages[len(r.Session.Messages):]
	return final, r.Session.Add(added...)
}

// personaBlock is the active agent's instructions, appended to the system
// prompt. Empty for the default agent.
// Toolset is the tools this turn will carry.
//
// `task` is added here rather than in tools.All, because it needs the Run it
// belongs to — and because only a PARENT run gets it. A subagent builds its
// own set without it, so delegation cannot recurse.
//
// Subagents are a setting, and an off switch has to REMOVE the tool: one that
// exists and refuses spends context on its description and teaches the model
// to keep trying it.
func (r *Run) Toolset() []ai.Tool {
	toolset := tools.All(r.Tools)
	if r.Subagents {
		toolset = append(toolset, TaskTool(r, r.Progress))
	}
	toolset = append(toolset, r.Extra...)
	toolset = append(toolset, r.ExtensionTools...)
	if r.WrapTools != nil {
		toolset = r.WrapTools(toolset)
	}
	return toolset
}

// ToolNames is the toolset's names, for the prompt's tool list.
func (r *Run) ToolNames() []string {
	toolset := r.Toolset()
	names := make([]string, 0, len(toolset))
	for _, t := range toolset {
		names = append(names, t.Name())
	}
	return names
}

// ToolSchemaChars is how much of the window the tool definitions occupy —
// name, description, and JSON schema, which is what actually goes on the wire.
func (r *Run) ToolSchemaChars() int {
	total := 0
	for _, t := range r.Toolset() {
		total += len(t.Name()) + len(t.Description())
		if schema, err := json.Marshal(t.InputSchema()); err == nil {
			total += len(schema)
		}
	}
	return total
}

// extensionBlock is what the enabled extensions add to the system prompt.
//
// LAST, after the persona: an extension is something the user switched on for
// this session, and it should be able to qualify the agent's own instructions
// rather than be qualified by them.
func (r *Run) extensionBlock() string {
	if r.ExtensionPrompt == "" {
		return ""
	}
	return "\n\n" + r.ExtensionPrompt
}

func (r *Run) personaBlock() string {
	if r.Persona == "" {
		return ""
	}
	return "\n\n" + r.Persona
}
