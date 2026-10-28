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

// Commit messages from a diff (agent/commit-message.ts): one cheap,
// non-streaming call on the given model, nothing persisted.

const commitMessagePrompt = `You write git commit messages for a diff.

Rules:
- A single subject line, imperative mood, no trailing period. Max 72 characters.
- Say what the change does and why it matters, not which files moved.
- No "chore:"/"feat:" prefix unless the diff shows the repo already uses one.
- No quotes, no markdown, no backticks, no body — the subject line only.
- Describe only what is in the diff. Never guess at intent you cannot see.`

const (
	maxDiffChars = 24_000
	maxFileChars = 4_000
)

var diffHeader = regexp.MustCompile(`(?m)^diff --git `)

// TrimDiffForPrompt keeps every file's header with a bounded slice of its
// hunks, so a 40-file change shows the model all 40 names.
func TrimDiffForPrompt(diff string) string {
	if len(diff) <= maxDiffChars {
		return diff
	}
	idx := diffHeader.FindAllStringIndex(diff, -1)
	var files []string
	start := 0
	for _, m := range idx {
		if m[0] > start {
			files = append(files, diff[start:m[0]])
		}
		start = m[0]
	}
	files = append(files, diff[start:])
	var b strings.Builder
	for _, f := range files {
		if f == "" {
			continue
		}
		if len(f) <= maxFileChars {
			b.WriteString(f)
		} else {
			b.WriteString(f[:maxFileChars] + "\n... (file truncated)\n")
		}
	}
	joined := b.String()
	if len(joined) <= maxDiffChars {
		return joined
	}
	return joined[:maxDiffChars] + "\n... (diff truncated)\n"
}

var bullet = regexp.MustCompile(`^\s*[-*]\s+`)

// CleanSubject strips the quoting and bullets models like to add.
func CleanSubject(raw string) string {
	first := ""
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			first = line
			break
		}
	}
	s := strings.TrimSpace(bullet.ReplaceAllString(first, ""))
	s = strings.TrimSpace(strings.Trim(s, "`"))
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return strings.TrimSpace(strings.TrimSuffix(s, "."))
}

// CommitMessageOptions are generateCommitMessage's inputs.
type CommitMessageOptions struct {
	Diff, ModelID, Branch, CWD string
	Tracker                    *CostTracker
}

// GenerateCommitMessage writes a subject line for a diff, "" when the model
// gave nothing usable.
func GenerateCommitMessage(ctx context.Context, o CommitMessageOptions) (string, error) {
	diff := strings.TrimSpace(o.Diff)
	if diff == "" {
		return "", nil
	}
	prompt := ""
	if o.Branch != "" {
		prompt = "Branch: " + o.Branch + "\n\n"
	}
	prompt += "Diff:\n" + TrimDiffForPrompt(diff)
	prov, model := config.SplitModel(o.ModelID)
	cfg, err := config.Resolve(config.Config{Provider: prov, ModelID: model, CWD: o.CWD})
	if err != nil {
		return "", err
	}
	lm, err := config.LanguageModel(cfg)
	if err != nil {
		return "", err
	}
	res, err := ai.GenerateText(ctx, ai.Options{Model: lm, System: commitMessagePrompt, Messages: []provider.Message{ai.UserText(prompt)}})
	if err != nil {
		return "", err
	}
	tracker := o.Tracker
	if tracker == nil {
		tracker = NewCostTracker()
	}
	tracker.Add(o.ModelID, ToUsage(res.Usage), BillContext{Source: session.SourceCommitMessage, CWD: o.CWD})
	subject := CleanSubject(res.Text)
	if len([]rune(subject)) <= 72 {
		return subject, nil
	}
	cut := string([]rune(subject)[:72])
	if i := strings.LastIndex(cut, " "); i > 40 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut), nil
}
