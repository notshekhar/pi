package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai"
)

// `ask` — put a real question to the user, with the options spelled out.
//
// The alternative is the model guessing, or asking in prose and having the
// answer arrive as free text it then has to interpret. A structured question
// gets a structured answer, and — more importantly — makes the moment of
// choice visible instead of buried in a paragraph.
//
// It is deliberately hard to reach for: the description tells the model to
// use it when the answers diverge, not whenever it feels uncertain. A model
// that asks about everything is worse than one that picks a default and says
// which it picked.

// Asker puts a question to the user and returns the chosen option, or "" if
// they declined to answer.
type Asker func(ctx context.Context, question string, options []string) string

type askArgs struct {
	Question string   `json:"question" jsonschema:"description=The question; specific and answerable"`
	Options  []string `json:"options" jsonschema:"description=Two to four mutually exclusive answers"`
}

// Ask returns the ask tool.
func Ask(t *Context) ai.Tool {
	return ai.NewTool("ask",
		"Ask the user to choose between options. Use it ONLY when the answers lead "+
			"to materially different work and you cannot pick from the code or the "+
			"request — otherwise choose the sensible default, say which you chose, "+
			"and carry on. Never ask about something you could look up.",
		func(ctx context.Context, a askArgs) (ai.ToolResult, error) {
			if ctx.Err() != nil {
				return ai.ToolError("aborted"), nil
			}
			question := strings.TrimSpace(a.Question)
			if question == "" {
				return ai.ToolError("question is empty"), nil
			}
			var options []string
			for _, o := range a.Options {
				if o = strings.TrimSpace(o); o != "" {
					options = append(options, o)
				}
			}
			if len(options) < 2 {
				return ai.ToolError("give at least two options"), nil
			}
			if t.Ask == nil {
				// Nobody to ask: say so rather than inventing an answer, so
				// the model falls back to choosing and explaining.
				return ai.ToolError(
					"there is nobody to ask here — choose the best option and say which you chose"), nil
			}

			answer := t.Ask(ctx, question, options)
			if answer == "" {
				return ai.ToolText("the user did not answer; choose the best option and say which you chose"), nil
			}
			return ai.ToolTextf("the user chose: %s", answer), nil
		})
}

// Plan returns the plan tool.
func Plan(t *Context) ai.Tool {
	return ai.NewTool("plan",
		"Present a plan for a multi-step change and wait for approval before "+
			"starting. Use it when the work is large enough that doing the wrong "+
			"thing would waste real effort. Write it as the user would want to read "+
			"it: what changes, in which files, and what you are unsure about.",
		func(ctx context.Context, a planArgs) (ai.ToolResult, error) {
			if ctx.Err() != nil {
				return ai.ToolError("aborted"), nil
			}
			plan := strings.TrimSpace(a.Plan)
			if plan == "" {
				return ai.ToolError("plan is empty"), nil
			}
			if t.Ask == nil {
				return ai.ToolText("no reviewer here — proceeding is on you; state the plan and carry on"), nil
			}
			switch t.Ask(ctx, planHeadline(plan), []string{"Approve", "Revise"}) {
			case "Approve":
				return ai.ToolText("approved — carry it out"), nil
			case "Revise":
				return ai.ToolText("the user wants changes; ask what to alter before proceeding"), nil
			}
			return ai.ToolText("no answer; treat the plan as not yet approved"), nil
		})
}

type planArgs struct {
	Plan string `json:"plan" jsonschema:"description=The plan, as markdown"`
}

// planHeadline is the plan's first heading or line, for the prompt title —
// the whole plan belongs in the transcript, not squeezed into a dialog.
func planHeadline(plan string) string {
	line := strings.TrimSpace(strings.SplitN(plan, "\n", 2)[0])
	line = strings.TrimLeft(line, "# ")
	if line == "" {
		line = "Proceed with this plan?"
	}
	if len(line) > 70 {
		line = line[:67] + "…"
	}
	return fmt.Sprintf("%s — approve?", line)
}
