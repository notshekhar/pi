package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
)

// Branch summaries for /tree navigation — loop's agent/branch-summary.ts.
// When the user navigates away from a branch, its entries can be summarised
// into a branch-summary entry that joins the model context at the target.

// BranchSummaryPreamble introduces a branch summary in the model context.
const BranchSummaryPreamble = "The user explored a different conversation branch before returning here.\nSummary of that exploration:\n\n"

const branchSummaryPrompt = `Create a structured summary of this conversation branch for context when returning later.

Use this EXACT format:

## Goal
[What was the user trying to accomplish in this branch?]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Work that was started but not finished]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [What should happen next to continue this work]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const branchReserveTokens = 16_384

// ErrBranchSummaryAborted is a summary the user cancelled.
var ErrBranchSummaryAborted = errors.New("branch summary aborted")

// CollectEntriesForBranchSummary is the entries abandoned by navigating from
// oldLeaf to target: from the old leaf back to (excluding) the deepest
// common ancestor, in chronological order.
func CollectEntriesForBranchSummary(s *session.Session, oldLeaf, target string) []session.Entry {
	if oldLeaf == "" {
		return nil
	}
	oldPath := map[string]bool{}
	for _, e := range s.GetBranch(oldLeaf) {
		oldPath[e.ID] = true
	}
	targetPath := s.GetBranch(target)
	common := ""
	for i := len(targetPath) - 1; i >= 0; i-- {
		if oldPath[targetPath[i].ID] {
			common = targetPath[i].ID
			break
		}
	}
	var entries []session.Entry
	for cur := oldLeaf; cur != "" && cur != common; {
		e, ok := s.Entry(cur)
		if !ok {
			break
		}
		entries = append(entries, e)
		cur = e.ParentID
	}
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries
}

func userEntry(text string) session.Entry {
	raw, _ := json.Marshal(text)
	return session.Entry{Type: session.TypeMessage, Role: "user", Content: raw}
}

// branchEntryMessage is an entry as the summariser reads it; tool results
// are left out (the calls say what was done).
func branchEntryMessage(e session.Entry) (session.Entry, bool) {
	switch e.Type {
	case session.TypeMessage:
		if e.Role == "tool" {
			return session.Entry{}, false
		}
		return session.Entry{Type: session.TypeMessage, Role: e.Role, Content: e.Content}, true
	case session.TypeSubagent:
		return userEntry("[subagent " + e.Agent + " report] " + e.Result), true
	case session.TypeCompact:
		if e.Rollover || e.Handoff != "" {
			return userEntry("[context rollover] " + e.Handoff), true
		}
		return userEntry("[compaction summary] " + e.Summary), true
	case session.TypeBranchSummary:
		return userEntry("[branch summary] " + e.Summary), true
	}
	return session.Entry{}, false
}

// BranchSummaryOptions configures RunBranchSummary.
type BranchSummaryOptions struct {
	Entries            []session.Entry
	CustomInstructions string
	Tracker            *CostTracker
}

// RunBranchSummary summarises an abandoned branch on the run's model, newest
// entries winning when it is bigger than the window, billed as
// "branch-summary". Returns the text and the usage to stamp on the entry.
func (r *Run) RunBranchSummary(ctx context.Context, o BranchSummaryOptions) (string, *session.Usage, error) {
	window := 128_000
	if m, ok := config.ModelInfo(r.Config.Provider, r.Config.ModelID); ok && m.Context > 0 {
		window = m.Context
	}
	budget := max(window-branchReserveTokens, 4_000)
	var kept []session.Entry
	tokens := 0
	for i := len(o.Entries) - 1; i >= 0; i-- {
		m, ok := branchEntryMessage(o.Entries[i])
		if !ok {
			continue
		}
		size := (len(serializeMessage(m)) + 3) / 4
		if tokens+size > budget {
			break
		}
		kept = append([]session.Entry{m}, kept...)
		tokens += size
	}
	files := collectFileLists(kept, session.FileLists{})
	if len(kept) == 0 {
		return "No content to summarize", nil, nil
	}
	instructions := branchSummaryPrompt
	if o.CustomInstructions != "" {
		instructions += "\n\nAdditional focus: " + o.CustomInstructions
	}
	modelID := r.Config.FullID()
	var usage *session.Usage
	text, err := r.requestSummary(ctx, "<conversation>\n"+serializeConversation(kept)+"\n</conversation>\n\n"+instructions,
		func(u session.Usage) {
			usage = sumUsage(usage, u)
			if o.Tracker != nil {
				o.Tracker.Add(modelID, u, BillContext{Source: session.SourceBranchSummary, CWD: r.Config.CWD, SessionPub: r.Session.ID})
			}
		})
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, ErrBranchSummaryAborted
		}
		return "", nil, err
	}
	if usage != nil {
		stamped := StampUsage(modelID, *usage)
		usage = &stamped
	}
	return text + formatFileLists(files), usage, nil
}
