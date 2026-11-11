package agent

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/shells"
	"github.com/notshekhar/pi/internal/core/tools"
)

// One-step notices: a background shell's exit, or a nudge about a stale todo
// list, rides on the end of ONE request and is never saved. pi's
// StepOverrides.Messages changes only the step it is returned for, so
// nothing has to be taken back out afterwards (AI SDK v7 carries
// prepareStep's output forward, which is why loop has to).

// stepNotices is the PrepareStep that adds them, or nil when the toolset
// has neither the shells nor the todo tool.
func (r *Run) stepNotices(toolset []ai.Tool) ai.PrepareStepFunc {
	names := toolNames(toolset)
	var notices []func(step ai.StepContext) string
	if slices.Contains(names, "shells") {
		sessionID := r.Tools.SessionID
		notices = append(notices, func(ai.StepContext) string {
			return shells.FormatNotices(shells.TakeNotices(sessionID))
		})
	}
	if slices.Contains(names, "todo") {
		nudger := tools.NewTodoNudger(r.Tools.Todos)
		notices = append(notices, func(step ai.StepContext) string {
			return nudger.Nudge(calledTools(step.Steps))
		})
	}
	if len(notices) == 0 {
		return nil
	}
	return func(_ context.Context, step ai.StepContext) (ai.StepOverrides, error) {
		var extra []provider.Message
		for _, notice := range notices {
			if text := notice(step); text != "" {
				extra = append(extra, ai.UserText(text))
			}
		}
		if len(extra) == 0 {
			return ai.StepOverrides{}, nil
		}
		return ai.StepOverrides{Messages: append(step.Messages, extra...)}, nil
	}
}

// calledTools is every tool called in the steps so far, in order.
func calledTools(steps []ai.Step) []string {
	var calls []string
	for _, s := range steps {
		for _, c := range s.ToolCalls {
			calls = append(calls, c.ToolName)
		}
	}
	return calls
}

// planDelivered stops a turn whose step handed over a real plan. A stub
// keeps the turn alive, so the model can call again with the whole plan.
func planDelivered(step ai.Step) bool {
	for _, c := range step.ToolCalls {
		if c.ToolName != "plan" {
			continue
		}
		var args struct {
			Plan string `json:"plan"`
		}
		if json.Unmarshal(c.Input, &args) == nil && tools.IsDeliveredPlan(args.Plan) {
			return true
		}
	}
	return false
}

// stopWhen is the turn's early stop: a delivered plan, when the turn can
// deliver one.
func stopWhen(toolset []ai.Tool) func(ai.Step) bool {
	if slices.Contains(toolNames(toolset), "plan") {
		return planDelivered
	}
	return nil
}
