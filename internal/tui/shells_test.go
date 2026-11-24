package tui

import (
	"strings"
	"testing"
	"time"
)

// The panel ends with where to go to stop a shell, and that row never
// widens the panel past the terminal.
func TestShellsPanelSaysHowToStopOne(t *testing.T) {
	th := testTheme()
	rows := renderShells(th, []ShellRow{{ID: "bash_1", Command: "sleep 30", Status: "running", Started: time.Now()}}, 100, time.Now())
	if len(rows) != 2 || !strings.Contains(stripANSI(rows[1]), ShellsHint) {
		t.Fatalf("rows = %q", rows)
	}
	for _, width := range []int{20, 40, 80} {
		for _, row := range renderShells(th, []ShellRow{{ID: "bash_1", Command: "sleep 30", Status: "running"}}, width, time.Now()) {
			if w := visibleWidth(row); w > width {
				t.Errorf("width %d: row is %d wide: %q", width, w, stripANSI(row))
			}
		}
	}
	if rows := renderShells(th, nil, 100, time.Now()); rows != nil {
		t.Errorf("an empty panel costs rows: %q", rows)
	}
}
