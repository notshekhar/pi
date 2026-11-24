package main

import (
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/core/shells"
)

// The /shells picker lists running shells first, and offers stopping
// everything only when there is more than one thing to stop.
func TestShellPickerItems(t *testing.T) {
	exited := 0
	list := []shells.Info{
		{ID: "bash_1", Command: "make", Status: "exited", ExitCode: &exited},
		{ID: "bash_2", Command: "bun run dev\nsecond line", Status: "running"},
	}
	items := shellPickerItems(list)
	if items[0].Value != "bash_2" || items[1].Value != "bash_1" {
		t.Fatalf("running shells must come first: %+v", items)
	}
	if items[0].Label != "bash_2  bun run dev" || !strings.Contains(items[0].Description, "running") {
		t.Errorf("row = %+v", items[0])
	}

	list = append(list, shells.Info{ID: "bash_3", Command: "sleep 9", Status: "running"})
	items = shellPickerItems(list)
	if items[0].Value != stopAllShells || items[0].Label != "Stop all 2 running shells" {
		t.Errorf("first row = %+v", items[0])
	}
}
