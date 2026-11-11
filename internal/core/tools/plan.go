package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
)

// Plan delivery and the two tools that move a session in and out of plan
// mode. The agent asks, the user decides, both ways.

// planMinChars is the least a plan may be: shorter is a title or a summary.
const planMinChars = 200

type planArgs struct {
	Plan string `json:"plan"`
}

// IsDeliveredPlan reports whether a plan call's input carries a real plan.
// A turn stops on a delivered plan and carries on past a rejected stub.
func IsDeliveredPlan(plan string) bool {
	return len(strings.TrimSpace(plan)) >= planMinChars
}

// Plan returns the plan tool. A stub is rejected as ordinary text rather
// than as an error, which keeps the turn alive: the model reads the result
// and calls again with the whole document.
func Plan() ai.Tool {
	return LoopTool("plan", func(_ context.Context, a planArgs) (ai.ToolResult, error) {
		if !IsDeliveredPlan(a.Plan) {
			return ai.ToolText(stubPlanRejection("plan")), nil
		}
		return ai.ToolText("Plan delivered to the user."), nil
	})
}

func stubPlanRejection(tool string) string {
	return fmt.Sprintf("REJECTED: that was only a title/summary, not the plan. Call %s again with the COMPLETE plan "+
		"document in the `plan` argument — every section and step as markdown. Do not write the plan as "+
		"chat text; only the `plan` argument reaches the user.", tool)
}

// PlanApproval is what the user is asked to approve.
type PlanApproval string

const (
	// ApproveEnterPlan asks to switch the session into plan mode.
	ApproveEnterPlan PlanApproval = "plan"
	// ApproveExitPlan asks to accept a plan and leave plan mode.
	ApproveExitPlan PlanApproval = "exit-plan"
)

// PlanMode is a session's read-only planning gate and the user's say over it.
type PlanMode interface {
	Active() bool
	Set(active bool)
	// Approve asks the user; text is the reason for entering, or the
	// headline of the plan being delivered.
	Approve(ctx context.Context, kind PlanApproval, text string) bool
}

type enterPlanArgs struct {
	Reason string `json:"reason"`
}

// EnterPlanMode returns the tool that asks the user to start planning.
func EnterPlanMode(mode PlanMode) ai.Tool {
	return LoopTool("enter_plan_mode", func(ctx context.Context, a enterPlanArgs) (ai.ToolResult, error) {
		if mode.Active() {
			return ai.ToolText("Plan mode is already active."), nil
		}
		approved := mode.Approve(ctx, ApproveEnterPlan, a.Reason)
		if aborted(ctx) {
			return fail(errAborted)
		}
		if !approved {
			return ai.ToolText("The user declined plan mode. Continue in normal mode — investigate as needed and proceed with " +
				"the task directly. Do not call enter_plan_mode again for this request."), nil
		}
		mode.Set(true)
		return ai.ToolText("Plan mode is ON (the user approved). From now on file edits are rejected and bash is read-only — " +
			"in every permission mode. Investigate the codebase with the read-only tools and design the " +
			"implementation approach. Finish this turn by presenting your findings and the proposed approach in " +
			"chat; from the next turn the plan delivery tool is available to hand the user the final plan " +
			"document for approval. Plan mode ends when the user accepts a plan or toggles it off."), nil
	})
}

// ExitPlanMode returns the tool that delivers a plan and asks to build it.
func ExitPlanMode(mode PlanMode) ai.Tool {
	return LoopTool("exit_plan_mode", func(ctx context.Context, a planArgs) (ai.ToolResult, error) {
		if !mode.Active() {
			return ai.ToolText("Plan mode is not active — nothing to exit. Continue with the task directly."), nil
		}
		if !IsDeliveredPlan(a.Plan) {
			return ai.ToolText(stubPlanRejection("exit_plan_mode")), nil
		}
		approved := mode.Approve(ctx, ApproveExitPlan, PlanHeadline(a.Plan))
		if aborted(ctx) {
			return fail(errAborted)
		}
		if !approved {
			return ai.ToolText("The user did not approve the plan. Plan mode stays ON — edits are still rejected and bash is " +
				"still read-only. Ask what they want changed or keep investigating, then call exit_plan_mode " +
				"again with the revised plan. Do not attempt file edits in the meantime."), nil
		}
		mode.Set(false)
		return ai.ToolText("Approved — plan mode is OFF. File edits and writable bash are enabled again. Implement the plan " +
			"you just delivered now, in this same turn: work through its steps in order, then run the " +
			"verification it describes. Do not re-plan and do not wait for another message."), nil
	})
}

var headingMarks = regexp.MustCompile(`^#+\s*`)

// planHeadlineChars is the longest headline an approval prompt shows.
const planHeadlineChars = 100

// PlanHeadline is a plan's first non-empty line without its heading marks —
// the whole plan belongs in the transcript, not squeezed into a prompt.
func PlanHeadline(plan string) string {
	for _, line := range strings.Split(plan, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		first := []rune(strings.TrimSpace(headingMarks.ReplaceAllString(line, "")))
		if len(first) > planHeadlineChars {
			return string(first[:planHeadlineChars-3]) + "…"
		}
		return string(first)
	}
	return ""
}
