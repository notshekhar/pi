package main

import (
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/core/shells"
	"github.com/notshekhar/pi/internal/tui"
)

// mountShellsPanel pins the background shells under the todo panel and
// keeps it following the registry. Mounting it is also what lets a
// foreground command that outruns its timeout be promoted rather than
// killed: the timeout exists because an unbounded run is invisible, and a
// shell listed here is not. The returned func unmounts it.
func (t *repl) mountShellsPanel() func() {
	shells.SetPanelPresent(true)
	stop := shells.OnChange(func(shells.Info) { t.app.Do(t.refreshShells) })
	return func() {
		stop()
		shells.SetPanelPresent(false)
	}
}

// refreshShells shows the foreground session's shells. Runs on the render
// loop.
func (t *repl) refreshShells() {
	t.mu.Lock()
	id := t.run.Session.ID
	t.mu.Unlock()
	t.app.SetShells(shellRows(shells.List(id)))
}

func shellRows(list []shells.Info) []tui.ShellRow {
	rows := make([]tui.ShellRow, len(list))
	for i, s := range list {
		rows[i] = tui.ShellRow{ID: s.ID, Command: s.Command, Status: s.Status, ExitCode: s.ExitCode,
			Started: time.UnixMilli(s.StartedAt), LastLine: lastOutputLine(s.Preview)}
		if s.EndedAt != nil {
			rows[i].Ended = time.UnixMilli(*s.EndedAt)
		}
	}
	return rows
}

func lastOutputLine(preview []string) string {
	for i := len(preview) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(preview[i]); line != "" {
			return line
		}
	}
	return ""
}
