package agent

import (
	"encoding/json"

	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/memory"
	"github.com/notshekhar/pi/internal/modules/core/skills"
)

// Where the context window actually goes.
//
// "83% full" is not an answer anyone can act on; "the system prompt is 700
// tokens and the transcript is 400k" is. The categories are the units a user
// can do something about — turn a feature off, compact, start a new session —
// so the report is built out of the same pieces the prompt is assembled from
// rather than from a single total.
//
// Every figure here is a chars/4 ESTIMATE. There is no tokenizer in this
// binary, and one that only matched a single provider would be worse than an
// honest approximation. The caller pairs it with the provider's real count
// for the headline, so the number a user quotes is measured even when the
// breakdown is not.

// ContextCategory is one labelled slice of the window.
type ContextCategory struct {
	Label  string
	Tokens int
}

// SkillTokens is one skill's cost in the index.
type SkillTokens struct {
	Name   string
	Tokens int
}

// ContextReport is the whole breakdown.
type ContextReport struct {
	ContextWindow int
	Categories    []ContextCategory
	Skills        []SkillTokens
	// TotalTokens is the sum of the categories.
	TotalTokens int
	FreeTokens  int
	// AutoCompactThreshold is the fraction at which the session compacts
	// itself, 0 when that is switched off.
	AutoCompactThreshold float64
}

// BuildContextReport measures where a session's window is going.
func BuildContextReport(r *Run, window int) ContextReport {
	settings := config.LoadSettings()
	cwd := r.Config.CWD

	report := ContextReport{
		ContextWindow:        window,
		AutoCompactThreshold: settings.AutoCompact(),
	}

	// The system prompt WITHOUT the pieces that get their own category, so
	// nothing is counted twice.
	names := r.ToolNames()
	base := SystemPrompt(cwd, names)
	if r.Planning {
		base += PlanPrompt
	}
	add := func(label string, tokens int) {
		if tokens > 0 {
			report.Categories = append(report.Categories, ContextCategory{Label: label, Tokens: tokens})
		}
	}
	add("System prompt", EstimateTokens(len(base)))
	add("System tools", EstimateTokens(r.ToolSchemaChars()))

	if settings.WorkspaceContextOn() {
		add("Workspace context", EstimateTokens(len(LoadWorkspaceContext(cwd).Text)))
	}
	if settings.MemoryOn() {
		if store, err := memory.Open(cwd); err == nil {
			add("Memory", EstimateTokens(len(store.Index())))
		}
	}
	if index := skills.Index(cwd); index != "" {
		add("Skills", EstimateTokens(len(index)))
		for _, s := range skills.Load(cwd) {
			report.Skills = append(report.Skills, SkillTokens{
				Name: s.Name, Tokens: EstimateTokens(len(s.Body)),
			})
		}
	}

	// The transcript, measured the way it goes on the wire rather than as
	// visible text: a tool result's JSON envelope is context too, and a
	// session full of small tool calls is mostly envelope.
	messageChars := 0
	for _, msg := range r.Session.Messages {
		if encoded, err := json.Marshal(msg); err == nil {
			messageChars += len(encoded)
		}
	}
	report.Categories = append(report.Categories,
		ContextCategory{Label: "Messages", Tokens: EstimateTokens(messageChars)})

	for _, c := range report.Categories {
		report.TotalTokens += c.Tokens
	}
	if window > report.TotalTokens {
		report.FreeTokens = window - report.TotalTokens
	}
	return report
}
