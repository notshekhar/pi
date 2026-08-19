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

// Goal mode: plan, work the plan, then have the work checked.
//
// The failure it exists to prevent is the one every agent has: declaring
// success. A model that has just written code is the worst possible judge of
// whether that code is right — it is reporting on its own intent rather than
// on the result. So the verifier runs as a SEPARATE turn with no memory of
// having done the work, and is told to look for reasons it is wrong.
//
// Three phases:
//
//  1. plan     — turn the goal into concrete, checkable steps
//  2. work     — the ordinary agent loop, with the plan in the todo list
//  3. verify   — a fresh, adversarial pass over what actually changed
//
// It is not a replacement for the normal loop. It costs at least three model
// calls and is worth it when a goal is big enough that "done" is a claim
// somebody should check.

// GoalResult is what a goal run concluded.
type GoalResult struct {
	Plan     string
	Verdict  string
	Passed   bool
	Attempts int
}

// MaxGoalAttempts bounds the work→verify cycle. Unbounded, a model that
// cannot satisfy the verifier will spend money forever.
const MaxGoalAttempts = 3

const plannerPrompt = `Turn the goal below into a short, concrete plan.

Each step must be something a person could check afterwards — "the tests pass", "the function returns X for Y" — not "improve the code". Order them so each is possible once the ones before it are done. Five steps or fewer; if the goal needs more than that, it is really several goals and you should say so instead.

Output only the numbered steps.`

const verifierPrompt = `You are checking someone else's work against a goal. You did not do this work and you have no stake in it being right.

Look at what actually changed — read the files, run the tests, run the build. Do not take any claim on trust.

Answer in this shape:

VERDICT: PASS or FAIL
Then, briefly: what you checked, and for a FAIL exactly what is wrong and where.

FAIL anything you could not verify. "It looks correct" is a FAIL — you either checked it or you did not.`

// RunGoal drives a goal to a verified conclusion.
//
// `work` runs one ordinary turn and is supplied by the caller, so the UI
// renders it exactly as it renders any other turn.
func (r *Run) RunGoal(ctx context.Context, goal string, work func(context.Context, string) error) (GoalResult, error) {
	var result GoalResult

	plan, err := r.oneShot(ctx, plannerPrompt+"\n\nGoal: "+goal)
	if err != nil {
		return result, fmt.Errorf("planning: %w", err)
	}
	result.Plan = plan

	for attempt := 1; attempt <= MaxGoalAttempts; attempt++ {
		result.Attempts = attempt

		prompt := "Goal: " + goal + "\n\nPlan:\n" + plan +
			"\n\nCarry this out. Track it with the todo tool, and verify each step as you finish it."
		if result.Verdict != "" {
			// A retry must carry WHY it failed, or the model repeats itself.
			prompt = "Goal: " + goal + "\n\nA previous attempt was rejected:\n\n" +
				result.Verdict + "\n\nFix exactly what the review found, then stop."
		}
		if err := work(ctx, prompt); err != nil {
			return result, err
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}

		verdict, err := r.oneShot(ctx, verifierPrompt+"\n\nGoal: "+goal+"\n\nPlan:\n"+plan)
		if err != nil {
			return result, fmt.Errorf("verifying: %w", err)
		}
		result.Verdict = verdict
		if passed(verdict) {
			result.Passed = true
			return result, nil
		}
	}
	return result, nil
}

// passed reads the verdict line.
//
// Requires an explicit PASS. A verdict that does not say so is a fail — the
// safe reading when the checker's output is not what was asked for.
func passed(verdict string) bool {
	for _, line := range strings.Split(verdict, "\n") {
		line = strings.TrimSpace(strings.ToUpper(line))
		if strings.HasPrefix(line, "VERDICT:") {
			return strings.Contains(line, "PASS") && !strings.Contains(line, "FAIL")
		}
	}
	return false
}

// oneShot runs a turn whose output goes nowhere near the transcript, for the
// planner and the verifier.
//
// The verifier gets READ-ONLY tools deliberately: a checker that can edit is
// a checker that can make the problem go away instead of reporting it.
func (r *Run) oneShot(ctx context.Context, prompt string) (string, error) {
	model, err := config.LanguageModel(r.Config)
	if err != nil {
		return "", err
	}
	toolCtx := &tools.Context{
		CWD:      r.Config.CWD,
		Registry: tools.NewRegistry(),
		Todos:    &tools.TodoList{},
	}
	toolset := []ai.Tool{
		tools.Read(toolCtx), tools.Ls(toolCtx), tools.Grep(toolCtx),
		tools.Glob(toolCtx), tools.Find(toolCtx), tools.Bash(toolCtx),
	}

	sub := &Run{Config: r.Config, Permissions: r.Permissions}
	needsApproval, approveTool := sub.approvalHooks()

	res, err := ai.GenerateText(ctx, ai.Options{
		Model:         model,
		System:        "You work inside " + r.Config.CWD + ".",
		Messages:      []provider.Message{ai.UserText(prompt)},
		Tools:         toolset,
		MaxSteps:      r.Config.MaxSteps,
		NeedsApproval: needsApproval,
		ApproveTool:   approveTool,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Text), nil
}
