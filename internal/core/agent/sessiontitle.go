package agent

import (
	"context"
	"regexp"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
)

// Session titles (agent/session-title.ts): a short name for what this session
// is about, generated from the first exchange.
//
// A session's identity is otherwise a ULID and a cwd, which is fine while you
// are looking at it and useless the moment you are not: a sidebar of panes, a
// notification on a phone, `pi sessions` a week later. Generated once, early,
// with the session's OWN model — the only one guaranteed to be reachable and
// paid for — and never over a name the user chose.

// maxTitleChars: titles are chips in someone else's UI, past this they are
// just elided.
const maxTitleChars = 48

// maxTitleInputChars is enough of the exchange to know the subject, not
// enough to cost anything.
const maxTitleInputChars = 2_000

const titlePrompt = `Write a title for this coding session: what the user is trying to get done.

Rules:
- At most 6 words. Shorter is better.
- Start with a verb in plain form ("Fix", "Add", "Debug", "Refactor").
- Name the specific thing worked on — a file, feature, or symbol — not the
  activity in general. "Fix flaky pty test" beats "Fix a test".
- No quotes, no trailing period, no markdown, no preamble.
- Same language as the user's message.

Reply with the title alone.`

var (
	titleEdgeLead  = regexp.MustCompile("^[\"'`*#\\s]+")
	titleEdgeTrail = regexp.MustCompile("[\"'`*\\s]+$")
	titleSpaces    = regexp.MustCompile(`\s+`)
)

// CleanTitle is one line, no punctuation noise, short enough for a tab.
func CleanTitle(raw string) string {
	line := ""
	for _, l := range strings.Split(raw, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	// Quoting or markdown a model added anyway.
	s := titleEdgeLead.ReplaceAllString(line, "")
	s = titleEdgeTrail.ReplaceAllString(s, "")
	s = strings.TrimSuffix(s, ".")
	s = strings.TrimSpace(titleSpaces.ReplaceAllString(s, " "))
	r := []rune(s)
	if len(r) <= maxTitleChars {
		return s
	}
	// Cut at a word boundary so the elision reads as a title, not a
	// truncation.
	cut := string(r[:maxTitleChars])
	if i := strings.LastIndex(cut, " "); i >= 0 && len([]rune(cut[:i])) > maxTitleChars/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " \t") + "…"
}

// SessionTitleOptions are generateSessionTitle's inputs.
type SessionTitleOptions struct {
	// UserInput is what the user opened the session with.
	UserInput string
	// AssistantText is what the agent replied — it disambiguates a terse
	// opening prompt.
	AssistantText string
	// ModelID is the session's own model.
	ModelID    string
	Tracker    *CostTracker
	SessionPub string
	CWD        string
}

// GenerateSessionTitle returns a title for the session, or "" when there
// isn't one worth showing.
//
// Best-effort by construction: the caller is a turn that has already
// finished, so a provider hiccup here must cost nothing and say nothing —
// every failure is "", and the session keeps the name it had.
func GenerateSessionTitle(ctx context.Context, o SessionTitleOptions) string {
	user := strings.TrimSpace(o.UserInput)
	if user == "" {
		return ""
	}
	exchange := "User: " + clipChars(user, maxTitleInputChars)
	if said := strings.TrimSpace(o.AssistantText); said != "" {
		exchange += "\n\nAssistant: " + clipChars(said, 500)
	}
	prov, model := config.SplitModel(o.ModelID)
	cfg, err := config.Resolve(config.Config{Provider: prov, ModelID: model, CWD: o.CWD})
	if err != nil {
		return ""
	}
	lm, err := config.LanguageModel(cfg)
	if err != nil {
		return ""
	}
	res, err := ai.GenerateText(ctx, ai.Options{
		Model:    lm,
		Messages: []provider.Message{ai.UserText("<exchange>\n" + exchange + "\n</exchange>\n\n" + titlePrompt)},
	})
	if err != nil || res == nil {
		return ""
	}
	if o.Tracker != nil {
		o.Tracker.Add(o.ModelID, ToUsage(res.Usage), BillContext{
			Source: session.SourceSessionTitle, CWD: o.CWD, SessionPub: o.SessionPub,
		})
	}
	title := CleanTitle(res.Text)
	// A one-word answer is the model failing to understand the task, and a
	// title is not worth a retry — no title is the honest fallback.
	if len([]rune(title)) > 2 && strings.Contains(title, " ") {
		return title
	}
	return ""
}

// clipChars is JS's slice(0, n) over characters.
func clipChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
