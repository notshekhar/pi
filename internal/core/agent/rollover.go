package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/notshekhar/pi/internal/core/session"
)

// The recovery record a no-summary boundary carries into a fresh window
// (loop's agent/rollover.ts).
//
// Built mechanically from the transcript — no model call, ever: the boundary
// is free, deterministic, cannot hallucinate, and works when no summarizer
// model is reachable. What it keeps is inputs and externalized state, never
// inferred progress: every user message, the live todo list, the files the
// window touched, the commands it ran, and the trailing tool batch no model
// has consumed yet. Assistant prose, reasoning and already-consumed tool
// results are dropped on purpose — if it mattered it is in a file, in the
// todos, or in agent memory.

const (
	// MaxHandoffChars is the absolute ceiling; the live budget usually cuts
	// well below it.
	MaxHandoffChars = 20_000
	// maxSectionChars per section, so one runaway source cannot crowd out
	// the rest.
	maxSectionChars = 4_000
	// handoffOverheadReserve is headroom for joins and the preamble the
	// caller wraps around the record.
	handoffOverheadReserve = 1_000
)

var (
	handoffFileTools  = map[string]bool{"write": true, "edit": true, "apply_patch": true, "multi_edit": true, "notebook_edit": true}
	handoffShellTools = map[string]bool{"bash": true, "shell": true, "run": true}
)

// excerpt is a middle-out truncation: the head and tail of a record both
// carry signal.
func excerpt(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	const marker = "\n… middle omitted …\n"
	if limit <= len(marker) {
		return text[:limit]
	}
	head := (limit - len(marker)) / 2
	return text[:head] + marker + text[len(text)-(limit-len(marker)-head):]
}

// windowStart is where the current window starts. Pre-cut entries are still
// on the branch — compaction never deletes — so scanning from the root would
// re-collect every historical user message on each boundary.
func windowStart(branch []session.Entry) int {
	for i := len(branch) - 1; i >= 0; i-- {
		if branch[i].Type == session.TypeCompact {
			return i + 1
		}
	}
	return 0
}

func priorHandoff(branch []session.Entry) string {
	for i := len(branch) - 1; i >= 0; i-- {
		e := branch[i]
		if e.Type != session.TypeCompact {
			continue
		}
		// Never nest a prior recovery record: it would compound every
		// rollover into the next until the whole budget is old handoffs.
		if e.Handoff != "" {
			return "(an earlier rollover record — recover it with history if needed)"
		}
		return e.Summary
	}
	return ""
}

// unconsumedToolBatch is the trailing tool batch no model has seen: a
// boundary can land right after tools finish, and the result that triggered
// it would otherwise vanish before any model read it.
func unconsumedToolBatch(current []session.Entry) []string {
	var out []string
	sawAssistantText := false
	for i := len(current) - 1; i >= 0; i-- {
		e := current[i]
		if e.Type != session.TypeMessage {
			continue
		}
		if e.Role == "assistant" {
			// Text after the tool results means a model already consumed them.
			if strings.TrimSpace(textOf(e.Content)) != "" {
				sawAssistantText = true
			}
			break
		}
		if e.Role != "tool" {
			continue
		}
		ps, _ := partsOf(e.Content)
		for j := len(ps) - 1; j >= 0; j-- {
			p := ps[j]
			if p.Type != "tool-result" {
				continue
			}
			name := p.ToolName
			if name == "" {
				name = "tool"
			}
			out = append([]string{name + ": " + excerpt(toolOutputText(p.Output), 600)}, out...)
		}
	}
	if sawAssistantText {
		return nil
	}
	return out
}

func collectToolUse(current []session.Entry) (files, commands []string) {
	for _, e := range current {
		if e.Type != session.TypeMessage || e.Role != "assistant" {
			continue
		}
		ps, _ := partsOf(e.Content)
		for _, p := range ps {
			if p.Type != "tool-call" {
				continue
			}
			var in map[string]any
			_ = json.Unmarshal(p.Input, &in)
			switch {
			case handoffFileTools[p.ToolName]:
				for _, k := range []string{"file_path", "path", "filePath"} {
					if path, ok := in[k].(string); ok {
						if !slices.Contains(files, path) {
							files = append(files, path)
						}
						break
					}
				}
			case handoffShellTools[p.ToolName]:
				for _, k := range []string{"command", "cmd"} {
					if cmd, ok := in[k].(string); ok {
						line, _, _ := strings.Cut(cmd, "\n")
						commands = append(commands, clipChars(line, 200))
						break
					}
				}
			}
		}
	}
	return files, commands
}

func latestTodosOn(branch []session.Entry) []session.TodoItem {
	for i := len(branch) - 1; i >= 0; i-- {
		if branch[i].Type == session.TypeCustom {
			if items, ok := session.TodosOf(branch[i].Payload); ok {
				return items
			}
		}
	}
	return nil
}

// HandoffOptions shapes a recovery record.
type HandoffOptions struct {
	// Limit is the hard character ceiling, from the live context budget.
	Limit int
	// NotesPath is the session scratchpad, named when notes exist.
	NotesPath string
	// OmitCommands leaves the command list out (settings-gated; output is
	// never kept either way).
	OmitCommands bool
}

// BuildRolloverHandoff builds the record, or "" when nothing useful fits —
// the caller then leaves the turn to whatever compaction already applied
// rather than committing a boundary that loses the window for nothing.
func BuildRolloverHandoff(branch []session.Entry, o HandoffOptions) string {
	limit := min(MaxHandoffChars, o.Limit) - handoffOverheadReserve
	if limit <= 0 {
		return ""
	}
	current := branch[windowStart(branch):]
	var sections []string
	used := 0
	// Sections are added strongest-first and simply skipped once full.
	push := func(header, body string) {
		if strings.TrimSpace(body) == "" {
			return
		}
		block := header + "\n" + excerpt(strings.TrimSpace(body), maxSectionChars)
		if used+len(block)+2 > limit {
			return
		}
		sections = append(sections, block)
		used += len(block) + 2
	}

	// P0 — owner intent. Mid-session corrections are the highest-value bytes
	// in the transcript and are never inferable again.
	var asked []string
	for _, e := range current {
		if e.Type == session.TypeMessage && e.Role == "user" {
			if t := strings.TrimSpace(textOf(e.Content)); t != "" {
				asked = append(asked, fmt.Sprintf("%d. %s", len(asked)+1, excerpt(t, 1_200)))
			}
		}
	}
	if len(asked) > 0 {
		push("## What the user asked for (their own words, in order)", strings.Join(asked, "\n\n"))
	}

	// P1 — the checklist, already a persisted custom entry.
	if todos := latestTodosOn(branch); todos != nil && session.HasActiveTodos(todos) {
		push("## Todo list, as it stands", session.FormatTodoList(todos))
	}

	// P2/P3 — externalized work, derived from the tool calls.
	files, commands := collectToolUse(current)
	if len(files) > 0 {
		lines := make([]string, len(files))
		for i, f := range files {
			lines[i] = "- " + f
		}
		push("## Files this window touched (re-read before editing; they are on disk)", strings.Join(lines, "\n"))
	}
	if !o.OmitCommands && len(commands) > 0 {
		recent := commands[max(0, len(commands)-25):]
		lines := make([]string, len(recent))
		for i, c := range recent {
			lines[i] = "- " + c
		}
		push("## Commands already run (output not kept — re-run if you need it)", strings.Join(lines, "\n"))
	}

	// P4 — results that exist only here.
	if batch := unconsumedToolBatch(current); len(batch) > 0 {
		push("## Tool results no model has read yet (they arrived as this window ended)", strings.Join(batch, "\n\n"))
	}

	if o.NotesPath != "" {
		push("## Notes", "Durable state for this session is at "+o.NotesPath+" — read it before continuing.")
	}

	// P5 — clearly labelled, never nested.
	if prior := priorHandoff(branch); prior != "" {
		push("## From an earlier boundary — POSSIBLY STALE", excerpt(prior, 1_000))
	}

	return strings.Join(sections, "\n\n")
}
