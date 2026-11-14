package agent

import (
	json "encoding/json/v2"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/session"
)

// ActivityPart is one piece of a subagent's run, in stream order: what its
// box shows, saved with the run (loop's SubagentActivityPart).
type ActivityPart struct {
	Type    string `json:"type"` // text | reasoning | tool
	Text    string `json:"text,omitempty"`
	Name    string `json:"name,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// Activity is a run's ordered parts; deltas of one kind merge.
type Activity []ActivityPart

func (a *Activity) delta(kind, text string) {
	if n := len(*a); n > 0 && (*a)[n-1].Type == kind {
		(*a)[n-1].Text += text
		return
	}
	*a = append(*a, ActivityPart{Type: kind, Text: text})
}

func (a *Activity) tool(name, input string) {
	*a = append(*a, ActivityPart{Type: "tool", Name: name, Summary: argSummary(input)})
}

// Text flattens the activity: text as-is, tools as "> name summary" lines.
func (a Activity) Text() string {
	var b strings.Builder
	for _, p := range a {
		switch p.Type {
		case "tool":
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
				b.WriteString("\n")
			}
			b.WriteString("> " + p.Name + p.Summary + "\n")
		case "text":
			b.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// argSummaryMax caps an activity line's argument.
const argSummaryMax = 70

// argSummary is a one-line glimpse of a tool call's input.
func argSummary(input string) string {
	var args map[string]any
	if json.Unmarshal([]byte(input), &args, jsonopt.Read) != nil {
		return ""
	}
	for _, key := range []string{"command", "path", "file_path", "pattern", "prompt"} {
		if v, ok := args[key].(string); ok && v != "" {
			line, _, _ := strings.Cut(v, "\n")
			if r := []rune(line); len(r) > argSummaryMax {
				line = string(r[:argSummaryMax-3]) + "…"
			}
			return " " + line
		}
	}
	return ""
}

// maxFollowUpTranscript tail-caps each earlier run replayed into a
// follow-up, so a chain of long runs cannot fill its window before it starts.
const maxFollowUpTranscript = 12_000

// followUpThread rebuilds an earlier task run — and the runs it continued —
// as user/assistant pairs, so a follow_up resumes with that context. ok is
// false when no run on this branch has that tool call id.
func followUpThread(branch []session.Entry, callID string) (agentName string, messages []provider.Message, ok bool) {
	byCall := map[string]session.Entry{}
	for _, e := range branch {
		if e.Type == session.TypeSubagent {
			if id := extraString(e, "toolCallId"); id != "" {
				byCall[id] = e
			}
		}
	}
	target, ok := byCall[callID]
	if !ok {
		return "", nil, false
	}
	var chain []session.Entry
	seen := map[string]bool{}
	for run, found := target, true; found && !seen[extraString(run, "toolCallId")]; run, found = byCall[extraString(run, "followUpOf")] {
		seen[extraString(run, "toolCallId")] = true
		chain = append([]session.Entry{run}, chain...)
	}
	for _, run := range chain {
		messages = append(messages, ai.UserText(run.Prompt), ai.AssistantText(runTranscript(run)))
	}
	return target.Agent, messages, true
}

// runTranscript is what a follow-up replays of one run: its activity (which
// shows what was explored), else its report.
func runTranscript(run session.Entry) string {
	var activity Activity
	_ = json.Unmarshal(run.Extra["activity"], &activity, jsonopt.Read)
	text := activity.Text()
	if text == "" {
		text = run.Result
	}
	if len(text) > maxFollowUpTranscript {
		text = "[earlier activity truncated]\n…" + strings.ToValidUTF8(text[len(text)-maxFollowUpTranscript:], "")
	}
	if text == "" {
		return "(no output)"
	}
	return text
}

func extraString(e session.Entry, key string) string {
	var s string
	_ = json.Unmarshal(e.Extra[key], &s, jsonopt.Read)
	return s
}

// maxReportChars bounds the report the parent reads: delegation must shrink
// the main context, never grow it.
const maxReportChars = 24_000

// boundReport is the report as the parent model receives it — never empty.
func boundReport(report string) string {
	report = strings.TrimSpace(report)
	if report == "" {
		return "(subagent finished without a final response)"
	}
	if len(report) <= maxReportChars {
		return report
	}
	return strings.ToValidUTF8(report[:maxReportChars], "") +
		fmt.Sprintf("\n\n[subagent report truncated at %d chars — ask a narrower follow-up task if you need more]", maxReportChars)
}
