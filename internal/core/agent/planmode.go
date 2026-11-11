package agent

import (
	"context"

	"github.com/notshekhar/pi/internal/core/tools"
)

// Planning reports whether the run is in plan mode: write and edit refuse,
// and bash runs in the read-only sandbox.
func (r *Run) Planning() bool { return r.planning.Load() }

// SetPlanning switches plan mode, telling OnPlanMode when it changed — the
// agent can switch it mid-turn (enter_plan_mode, exit_plan_mode), and the UI
// has to follow.
func (r *Run) SetPlanning(on bool) {
	if r.planning.Swap(on) != on && r.OnPlanMode != nil {
		r.OnPlanMode(on)
	}
}

// runPlanMode is the run's plan-mode gate as the plan tools see it.
type runPlanMode struct{ run *Run }

func (m runPlanMode) Active() bool    { return m.run.Planning() }
func (m runPlanMode) Set(active bool) { m.run.SetPlanning(active) }

// Approve puts the switch to the user through the run's approval prompt,
// as a call to the tool asking for it.
func (m runPlanMode) Approve(ctx context.Context, kind tools.PlanApproval, text string) bool {
	if m.run.Ask == nil {
		return false
	}
	tool, args := "enter_plan_mode", map[string]any{"reason": text}
	if kind == tools.ApproveExitPlan {
		tool, args = "exit_plan_mode", map[string]any{"plan": text}
	}
	return m.run.Ask(ctx, tool, args, "")
}
