package agent

import (
	"context"
	"fmt"
	"github.com/notshekhar/pi/internal/core/session"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/core/config"
)

// Consume drains the stream; the TUI renders it.
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

	toolset := r.Toolset()
	needsApproval, approveTool := r.approvalHooks()
	res, err := ai.StreamText(ctx, cachedOptions(ai.Options{
		Model:         model,
		System:        r.SystemPrompt(toolset),
		Messages:      r.Session.ModelMessages(),
		Tools:         toolset,
		MaxSteps:      r.Config.MaxSteps,
		Reasoning:     r.Config.Reasoning,
		NeedsApproval: needsApproval,
		ApproveTool:   approveTool,
		PrepareStep:   r.stepNotices(toolset),
		StopWhen:      stopWhen(toolset),
	}, r.Config, r.Session.ID))
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
