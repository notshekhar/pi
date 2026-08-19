package ai

import (
	"context"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// GenerateText runs the model to completion, executing any tools it calls and
// stepping until it stops asking for them.
//
// Use StreamText instead when output should appear as it is produced.
func GenerateText(ctx context.Context, opts Options) (*Result, error) {
	r, err := newRunner(&opts)
	if err != nil {
		return nil, err
	}

	for {
		plan, err := r.planStep(ctx)
		if err != nil {
			return nil, err
		}

		out, err := r.generateStep(ctx, plan)
		if err != nil {
			return nil, err
		}

		calls, err := r.repairCalls(ctx, plan, out.toolCalls())
		if err != nil {
			return nil, err
		}

		step := Step{
			Number:       len(r.steps),
			Text:         out.text,
			Reasoning:    out.reasoning,
			Content:      out.content,
			ToolCalls:    calls,
			FinishReason: out.finishReason,
			Usage:        out.usage,
			Warnings:     out.warnings,
			Messages:     []provider.Message{out.assistantMessage()},
		}

		// `resolved` may differ from `calls`: an approver can rewrite a
		// call's input. The step keeps the ORIGINAL — see UpdatedInput.
		resolved, denials, err := r.resolveApprovals(ctx, calls, nil)
		if err != nil {
			return nil, err
		}

		executions, abortErr := r.executeTools(ctx, plan, resolved, denials, nil)
		step.ToolExecutions = executions
		if len(executions) > 0 {
			step.Messages = append(step.Messages, toolMessage(executions))
		}

		r.recordStep(step)

		if abortErr != nil {
			return nil, abortErr
		}
		if !r.shouldContinue(step) {
			return r.result(), nil
		}
	}
}

// generateStep performs one non-streaming model call.
func (r *runner) generateStep(ctx context.Context, plan stepPlan) (*stepOutput, error) {
	prompt, err := r.prompt(ctx, plan)
	if err != nil {
		return nil, err
	}

	res, err := plan.model.DoGenerate(ctx, plan.callOptions(r, prompt, false))
	if err != nil {
		return nil, err
	}

	out := &stepOutput{
		content:      res.Content,
		finishReason: res.FinishReason,
		usage:        res.Usage,
		warnings:     res.Warnings,
	}

	var text, reasoning strings.Builder
	for _, c := range res.Content {
		switch v := c.(type) {
		case provider.Text:
			text.WriteString(v.Text)
		case provider.Reasoning:
			reasoning.WriteString(v.Text)
		}
	}
	out.text = text.String()
	out.reasoning = reasoning.String()

	return out, nil
}
