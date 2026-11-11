package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/core/shells"
)

// `shells` — read, list and kill the background shells bash started. One
// tool with three actions rather than three tools: every tool's schema rides
// every request, and these are one small surface over one registry.

type shellsArgs struct {
	Action string `json:"action"`
	ID     string `json:"id,omitempty"`
	Filter string `json:"filter,omitempty"`
}

// Shells returns the shells tool.
func Shells(t *Context) ai.Tool {
	return LoopTool("shells", func(_ context.Context, a shellsArgs) (ai.ToolResult, error) {
		text, err := shellsAction(t.SessionID, a)
		if err != nil {
			return fail(err)
		}
		return ai.ToolText(text), nil
	})
}

func shellsAction(sessionID string, a shellsArgs) (string, error) {
	if a.Action == "list" {
		return listShells(sessionID), nil
	}
	if a.ID == "" {
		return "", fmt.Errorf("shells: `id` is required for action %q (e.g. bash_1).", a.Action)
	}
	if a.Action == "kill" {
		info, ok := shells.Kill(sessionID, a.ID)
		if !ok {
			return "", unknownShell(sessionID, a.ID)
		}
		return fmt.Sprintf("%s killed (%s).", a.ID, shells.FirstLine(info.Command)), nil
	}
	result, ok := shells.Read(sessionID, a.ID, a.Filter, false)
	if !ok {
		return "", unknownShell(sessionID, a.ID)
	}
	return renderShellOutput(result), nil
}

func listShells(sessionID string) string {
	all := shells.List(sessionID)
	if len(all) == 0 {
		return "No background shells in this session."
	}
	lines := make([]string, len(all))
	for i, s := range all {
		lines[i] = fmt.Sprintf("%s  %s  %s", s.ID, shells.StatusText(s), shells.FirstLine(s.Command))
		if unread := s.Bytes - s.Cursor; unread > 0 && s.Status != "running" {
			lines[i] += fmt.Sprintf(" · %dB unread", unread)
		}
	}
	return strings.Join(lines, "\n")
}

func renderShellOutput(r shells.ReadResult) string {
	var notes []string
	if r.SkippedBytes > 0 {
		notes = append(notes, fmt.Sprintf("%dB of older output skipped — full log: %s", r.SkippedBytes, r.Info.LogPath))
	}
	if r.FilteredOut > 0 {
		notes = append(notes, fmt.Sprintf("%d lines hidden by filter", r.FilteredOut))
	}
	status := fmt.Sprintf("[%s %s]", r.Info.ID, shells.StatusText(r.Info))
	if strings.TrimSpace(r.Text) != "" {
		return strings.Join(append(append([]string{status}, notes...), "", r.Text), "\n")
	}
	why := "no new output"
	if r.FilteredOut > 0 {
		why = "no matching output"
	}
	// The first note would only restate why the read is empty.
	rest := notes
	if len(rest) > 0 {
		rest = rest[1:]
	}
	return strings.Join(append([]string{status + " " + why + " since last read."}, rest...), "\n")
}

func unknownShell(sessionID, id string) error {
	all := shells.List(sessionID)
	if len(all) == 0 {
		return fmt.Errorf("shells: no shell %s — nothing is running in this session.", id)
	}
	ids := make([]string, len(all))
	for i, s := range all {
		ids[i] = s.ID
	}
	return fmt.Errorf("shells: no shell %s. Known: %s.", id, strings.Join(ids, ", "))
}
