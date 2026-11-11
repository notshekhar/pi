package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The pinned background-shells panel — loop's ShellsPanel, row for row.
//
// Chrome under the todo panel: the work the agent is doing, then the
// processes it left running. It is what pays for backgrounding at all — a
// foreground command is bounded by a timeout because an unbounded one is
// invisible, and a shell listed here is not, which is why promotion on
// timeout is only allowed while this panel is mounted.
//
// Exited shells stay listed until the turn ends: a frame that SHRINKS
// mid-turn is the one change the renderer cannot take back, and a row that
// vanishes the instant its process dies is the easiest way to cause one.

// ShellRow is one shell as the panel shows it. Mirrors the core's shell info
// without importing it, as TodoItem does.
type ShellRow struct {
	ID      string
	Command string
	// Status is running | exited | killed | failed.
	Status   string
	ExitCode *int
	Started  time.Time
	Ended    time.Time
	// LastLine is the most recent non-empty output line.
	LastLine string
}

func (s ShellRow) running() bool { return s.Status == "running" }

// maxShellRows caps the panel, like maxTodoRows.
const maxShellRows = 4

const (
	shellMarkerWidth = 4 // "[~] "
	shellGap         = 2 // between the command, the hint and the status
	// shellMinHint is the least room a trailing output line is worth: half a
	// log line reads as noise.
	shellMinHint = 12
)

// SetShells replaces the listed shells. The registry is the source of
// truth; the panel is a view.
func (a *App) SetShells(rows []ShellRow) { a.shells = rows }

// RetireFinishedShells drops exited shells at a turn boundary.
func (a *App) RetireFinishedShells() {
	kept := a.shells[:0:0]
	for _, s := range a.shells {
		if s.running() {
			kept = append(kept, s)
		}
	}
	a.shells = kept
}

// shellsRunning is whether a listed shell's elapsed time is ticking.
func (a *App) shellsRunning() bool {
	for _, s := range a.shells {
		if s.running() {
			return true
		}
	}
	return false
}

// renderShells is the panel: running first, then the most recently
// finished, so a dead shell never pushes a live one off it.
func renderShells(t *Theme, shells []ShellRow, width int, now time.Time) []string {
	if len(shells) == 0 {
		return nil
	}
	ordered := append([]ShellRow(nil), shells...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].running() != ordered[j].running() {
			return ordered[i].running()
		}
		return lastActive(ordered[i]).After(lastActive(ordered[j]))
	})
	shown := ordered[:min(len(ordered), maxShellRows)]
	rows := make([]string, 0, len(shown)+1)
	for _, s := range shown {
		rows = append(rows, shellRow(t, s, width, now))
	}
	if hidden := len(ordered) - len(shown); hidden > 0 {
		rows = append(rows, t.Fg(SlotDim, fmt.Sprintf("    +%d more", hidden)))
	}
	return rows
}

func lastActive(s ShellRow) time.Time {
	if !s.Ended.IsZero() {
		return s.Ended
	}
	return s.Started
}

// shellRow is `[~] bash_1  bun run dev      ready in 340ms   2m14s`, laid
// out right to left: which shell and what state it is in must never be lost,
// so the status is reserved first, the id and command next, and the last
// output line takes what is left — dropped whole rather than clipped to a
// stub on a narrow terminal.
func shellRow(t *Theme, s ShellRow, width int, now time.Time) string {
	status := shellStatus(s, now)
	head := s.ID + "  " + strings.SplitN(s.Command, "\n", 2)[0]
	forHead := max(1, max(1, width-shellMarkerWidth)-visibleWidth(status)-shellGap)

	var body string
	headWidth := visibleWidth(head)
	hintRoom := forHead - headWidth - shellGap
	if tail := strings.TrimSpace(s.LastLine); tail != "" && headWidth <= forHead && hintRoom >= shellMinHint {
		hint := truncateToWidth(tail, hintRoom, "…")
		body = head + strings.Repeat(" ", max(shellGap, forHead-headWidth-visibleWidth(hint))) + hint
	} else {
		left := truncateToWidth(head, forHead, "…")
		body = left + strings.Repeat(" ", max(0, forHead-visibleWidth(left)))
	}
	text := shellMarker(s) + body + strings.Repeat(" ", shellGap) + status
	switch s.Status {
	case "running":
		return t.Bold(t.Fg(SlotAccent, text))
	case "killed":
		return t.Strike(t.Fg(SlotDim, text))
	default:
		return t.Fg(SlotDim, text)
	}
}

// shellMarker: [~] running · [x] finished cleanly · [-] killed · [!] went wrong.
func shellMarker(s ShellRow) string {
	switch {
	case s.running():
		return "[~] "
	case s.Status == "killed":
		return "[-] "
	case s.Status == "exited" && s.ExitCode != nil && *s.ExitCode == 0:
		return "[x] "
	default:
		return "[!] "
	}
}

func shellStatus(s ShellRow, now time.Time) string {
	switch s.Status {
	case "running":
		return shellElapsed(now.Sub(s.Started))
	case "killed", "failed":
		return s.Status
	}
	if s.ExitCode == nil {
		return "exit ?"
	}
	if *s.ExitCode == 0 {
		return "done"
	}
	return fmt.Sprintf("exit %d", *s.ExitCode)
}

// shellElapsed is loop's formatDuration (8s, 2m14s, 1h03m) — whole seconds,
// unlike the turn summary's tenths, because this one ticks once a second.
func shellElapsed(d time.Duration) string {
	total := max(0, int((d+500*time.Millisecond)/time.Second))
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	m, s := total/60, total%60
	if m < 60 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%dh%02dm", m/60, m%60)
}
