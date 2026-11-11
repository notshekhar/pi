package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
)

// `ask` — put real questions to the user, with the options spelled out.
// The shapes are loop's ask bridge, shared by every surface that can answer:
// the TUI, cmux's card and the RPC clients.

// AskOption is one answer the user can pick.
type AskOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// AskQuestion is one question with its choices.
type AskQuestion struct {
	Question    string      `json:"question"`
	Header      string      `json:"header"`
	Options     []AskOption `json:"options"`
	MultiSelect bool        `json:"multiSelect,omitempty"`
}

// AskAnswer is what the user did with one question.
type AskAnswer struct {
	Answers []string `json:"answers"`
	// Custom: the answer is the user's own text, not one of the options.
	Custom bool `json:"custom,omitempty"`
	// Declined: the user skipped this question.
	Declined bool `json:"declined,omitempty"`
	// Note is free text the user attached to a picked option.
	Note string `json:"note,omitempty"`
}

// Asker puts questions to the user and returns one answer per question.
type Asker func(ctx context.Context, questions []AskQuestion) []AskAnswer

type askArgs struct {
	Questions []AskQuestion `json:"questions"`
}

// Ask returns the ask tool.
func Ask(t *Context) ai.Tool {
	return LoopTool("ask", func(ctx context.Context, a askArgs) (ai.ToolResult, error) {
		if aborted(ctx) {
			return fail(errAborted)
		}
		if t.Ask == nil {
			return ai.ToolText("The ask tool is not available in this session (no interactive UI). Proceed without asking."), nil
		}
		answers := t.Ask(ctx, a.Questions)
		if aborted(ctx) {
			return fail(errAborted)
		}
		return ai.ToolText(FormatAskAnswers(a.Questions, answers)), nil
	})
}

// FormatAskAnswers is the answers as the model reads them.
func FormatAskAnswers(questions []AskQuestion, answers []AskAnswer) string {
	if allDeclined(answers) {
		return "The user declined to answer. Proceed with your best judgment."
	}
	parts := []string{"User answers:"}
	for i, q := range questions {
		suffix := ""
		if q.MultiSelect {
			suffix = " (multi-select)"
		}
		parts = append(parts, fmt.Sprintf("[%s] %s%s\n%s", q.Header, q.Question, suffix, answerLine(answers, i)))
	}
	return strings.Join(parts, "\n\n")
}

func allDeclined(answers []AskAnswer) bool {
	for _, a := range answers {
		if !a.Declined {
			return false
		}
	}
	return true
}

func answerLine(answers []AskAnswer, i int) string {
	if i >= len(answers) || answers[i].Declined {
		return "→ (no answer — user skipped this question)"
	}
	a := answers[i]
	line := "→ " + strings.Join(a.Answers, ", ")
	if a.Custom {
		line = fmt.Sprintf("→ (custom answer) %q", strings.Join(a.Answers, "; "))
	}
	if a.Note != "" {
		line += fmt.Sprintf("\n→ user note: %q", a.Note)
	}
	return line
}
