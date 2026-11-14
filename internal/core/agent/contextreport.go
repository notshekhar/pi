package agent

import (
	json "encoding/json/v2"
	"slices"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/skills"
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
	toolset := r.Toolset()
	names := toolNames(toolset)
	base := BuildSystemPrompt(PromptParts{CWD: cwd, Base: r.Persona, Tools: names})
	add := func(label string, tokens int) {
		if tokens > 0 {
			report.Categories = append(report.Categories, ContextCategory{Label: label, Tokens: tokens})
		}
	}
	add("System prompt", EstimateTokens(len(base)))
	add("System tools", EstimateTokens(toolSchemaChars(toolset)))
	add("Workspace context", EstimateTokens(len(r.workspaceBlock(settings))))
	add("Memory", EstimateTokens(len(r.memoryBlock(settings))))
	turnSkills := r.turnSkills(settings)
	if index := skills.FormatForPrompt(turnSkills, slices.Contains(names, "skill")); index != "" {
		add("Skills", EstimateTokens(len(index)))
		for _, s := range turnSkills {
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
		if encoded, err := json.Marshal(msg, jsonopt.Write); err == nil {
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
