package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/notshekhar/pi/internal/core/session"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/tools"
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
		Messages:      r.Session.ModelMessages(),
		Tools:         toolset,
		MaxSteps:      r.Config.MaxSteps,
		Reasoning:     r.Config.Reasoning,
		NeedsApproval: needsApproval,
		ApproveTool:   approveTool,
	})
	if err != nil {
		return nil, err
	}

	consumeErr := consume(res.Stream)
	final, err := res.Final()
	// Whatever steps finished are saved even when the run did not: their
	// tokens were billed, and the conversation must show what happened.
	if final != nil {
		if perr := r.persistSteps(final.Steps); perr != nil && err == nil {
			err = perr
		}
	}
	if consumeErr != nil {
		return final, consumeErr
	}
	if err != nil {
		return final, err
	}
	if final == nil {
		return nil, fmt.Errorf("agent: run produced no result")
	}
	return final, nil
}

// persistSteps writes each step the way loop's turn does: the assistant
// message carries the step's usage (stamped with what it cost) and the model
// that produced it, a ledger row bills it and is linked to that entry, and
// the tool results follow as their own message.
func (r *Run) persistSteps(steps []ai.Step) error {
	modelID := r.Config.FullID()
	for _, step := range steps {
		var batch []session.Entry
		var rowID int64
		billed := -1
		for _, msg := range step.Messages {
			e, err := session.MessageEntry(msg)
			if err != nil {
				return err
			}
			if e.Role == "assistant" && billed < 0 {
				u := ToUsage(step.Usage)
				rowID, _ = Bill(modelID, u, BillContext{Source: session.SourceTurn, CWD: r.Config.CWD,
					SessionPub: r.Session.ID}, false)
				stamped := StampUsage(modelID, u)
				e.Usage, e.Model = &stamped, modelID
				billed = len(batch)
			}
			batch = append(batch, e)
		}
		if err := r.Session.AppendAll(batch...); err != nil {
			return err
		}
		if billed >= 0 && rowID != 0 {
			// The batch's entries got their ids on append: the billed one is
			// that many entries back from the leaf.
			all := r.Session.Entries()
			if idx := len(all) - len(batch) + billed; idx >= 0 && idx < len(all) {
				_ = session.AttachLedgerEntry(rowID, all[idx].ID)
			}
		}
	}
	return nil
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
	allowed := r.AllowedTools
	allows := func(name string) bool {
		for _, a := range allowed {
			if a == name {
				return true
			}
		}
		return false
	}
	var toolset []ai.Tool
	for _, t := range tools.All(r.Tools) {
		if len(allowed) == 0 || allows(t.Name()) {
			toolset = append(toolset, t)
		}
	}
	// MCP and extension tools join unrestricted agents only (loop's
	// assembleTurnTools), before task so a subagent fork inherits them.
	if len(allowed) == 0 {
		toolset = append(toolset, r.Extra...)
		toolset = append(toolset, r.ExtensionTools...)
	}
	if r.Subagents && (len(allowed) == 0 || allows("task")) {
		toolset = append(toolset, TaskTool(r, r.Progress))
	}
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
