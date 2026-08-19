package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/core/config"
)

// Compaction: trade the conversation for a summary of it, so a long session
// can keep going without falling out of the context window.
//
// The summary is written by the model itself and then STANDS IN for the
// history — everything it fails to carry is genuinely lost, which is why the
// prompt asks for decisions and open work rather than a readable précis.

const compactPrompt = `Summarize this conversation so it can replace the transcript entirely.

Everything you leave out is lost, so prioritise what the work needs to continue:

- What the user asked for, including constraints and corrections they gave
- Decisions made and the reasoning that is not re-derivable from the code
- Files created or changed, and what changed in each
- What is still open, in progress, or known broken

Write it as terse markdown notes, not prose. Do not editorialise, do not
address the user, and do not describe the conversation ("we discussed…") —
state the facts themselves.`

// CharsPerToken is the estimate used wherever a real tokenizer is not
// available. Roughly right for English prose and code; it is a size hint for
// the UI, never anything a request depends on.
const CharsPerToken = 4

// EstimateTokens is an approximate token count for a byte length.
func EstimateTokens(chars int) int { return chars / CharsPerToken }

// Compact replaces the session's history with a model-written summary of it,
// returning the character counts before and after.
//
// A failed compaction leaves the session untouched: a half-compacted history
// is worse than an oversized one.
func Compact(ctx context.Context, r *Run) (before, after int, err error) {
	before = r.Session.Chars()
	if len(r.Session.Messages) == 0 {
		return before, before, fmt.Errorf("nothing to compact")
	}

	// A summariser does not need the model you chose for the conversation,
	// and on a long session paying for it is a real cost.
	model, err := config.LanguageModel(r.Config.ForScope("compact"))
	if err != nil {
		return before, before, err
	}

	// The summary request rides on the existing history, so the model reads
	// the conversation rather than a flattened rendering of it.
	messages := append(append([]provider.Message{}, r.Session.Messages...), ai.UserText(compactPrompt))

	res, err := ai.GenerateText(ctx, ai.Options{
		Model:    model,
		System:   "You compact conversations for an autonomous coding agent.",
		Messages: messages,
		MaxSteps: 1,
	})
	if err != nil {
		return before, before, err
	}
	summary := strings.TrimSpace(res.Text)
	if summary == "" {
		return before, before, fmt.Errorf("compaction produced no summary")
	}

	// The summary enters as a user turn: it is context the assistant should
	// treat as established fact, not as something it said and might revise.
	if err := r.Session.Replace(ai.UserText("Summary of the conversation so far:\n\n" + summary)); err != nil {
		return before, before, err
	}
	return before, r.Session.Chars(), nil
}

// DefaultAutoCompact is the fraction of the context window at which a session
// compacts itself. Deliberately below the limit rather than at it: compaction
// is itself a model call that must fit, and a turn that overflows has already
// failed by the time anyone notices.
const DefaultAutoCompact = 0.8

// ShouldCompact reports whether a session has grown past the threshold.
//
// `used` is the REAL input-token count from the last turn's usage, not an
// estimate — after a request, the provider has told us exactly how large the
// context was, and that beats chars÷4 by a wide margin. Callers with no usage
// figure can pass EstimateTokens(session.Chars()) and get the old guess.
//
// A zero or negative threshold disables auto-compaction; a zero window means
// the model's limit is unknown, and guessing a limit is worse than not acting.
func ShouldCompact(used, window int, threshold float64) bool {
	if threshold <= 0 || window <= 0 || used <= 0 {
		return false
	}
	return float64(used) >= threshold*float64(window)
}
