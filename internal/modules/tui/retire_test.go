package tui

import (
	"strings"
	"testing"
	"time"
)

func retireApp(texts ...string) *App {
	a := &App{
		size:       Size{Cols: 40, Rows: 24},
		theme:      testTheme(),
		tools:      map[string]*entry{},
		groupsOpen: map[int]bool{},
		sel:        -1,
	}
	for _, txt := range texts {
		a.entries = append(a.entries, &entry{kind: entUser, text: txt, at: time.Now()})
	}
	return a
}

// Nothing is retired while everything still fits: the whole point is to
// commit what has scrolled away, not to shed blocks that are on screen.
func TestNothingRetiresWhileItFits(t *testing.T) {
	a := retireApp("one", "two")
	if got := a.retire(100); got != nil {
		t.Fatalf("retired %d lines while everything fit", len(got))
	}
	if len(a.entries) != 2 {
		t.Fatalf("entries = %d; want 2", len(a.entries))
	}
}

// The committed lines must be exactly the block's own, and the block must
// then be gone — a block that is both printed above the region and still in
// the model renders twice.
func TestRetiredBlockIsCommittedAndDropped(t *testing.T) {
	a := retireApp("one", "two", "three")
	first := a.entries[0].render(a.theme, a.size.Cols, AnimTick(), false, false)

	commit := a.retire(a.transcriptLen() - len(first))
	if len(commit) != len(first) {
		t.Fatalf("committed %d lines; want %d", len(commit), len(first))
	}
	if !strings.Contains(strings.Join(commit, "\n"), "one") {
		t.Fatalf("committed the wrong block: %q", commit)
	}
	if len(a.entries) != 2 {
		t.Fatalf("entries = %d; want 2", len(a.entries))
	}
	if rest := strings.Join(a.transcript(), "\n"); strings.Contains(rest, "one") {
		t.Fatal("the retired block is still in the transcript — it would render twice")
	}
}

// A block only half above the fold must stay whole. Committing it would
// print all of it above a region that then draws none of it, so the half
// still on screen would appear twice.
func TestStraddlingBlockIsNotRetired(t *testing.T) {
	a := retireApp("one", "two", "three")
	h := len(a.entries[0].render(a.theme, a.size.Cols, AnimTick(), false, false))
	if h < 2 {
		t.Skip("block too short to straddle")
	}
	// One row short of clearing the fold.
	if got := a.retire(a.transcriptLen() - (h - 1)); got != nil {
		t.Fatalf("retired a straddling block: %q", got)
	}
	if len(a.entries) != 3 {
		t.Fatal("a straddling block was dropped")
	}
}

// Nav and scroll are the user reading back through the transcript. Retiring
// under either deletes what they are looking at.
func TestNavAndScrollSuspendRetirement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*App)
	}{
		{"nav", func(a *App) { a.nav = true }},
		{"scroll", func(a *App) { a.scroll = 3 }},
		{"expandAll", func(a *App) { a.expandAll = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := retireApp("one", "two", "three")
			tc.apply(a)
			if got := a.retire(1); got != nil {
				t.Fatalf("retired %d lines while %s was active", len(got), tc.name)
			}
		})
	}
}

// The last block is never retired: it is where the live turn is written.
func TestTheLastBlockIsNeverRetired(t *testing.T) {
	a := retireApp("only")
	if got := a.retire(1); got != nil {
		t.Fatalf("retired the only block: %q", got)
	}
}

// A streaming block's final height is not known yet, so it cannot be handed
// to the terminal.
func TestStreamingBlocksAreNotRetired(t *testing.T) {
	a := retireApp("one", "two", "three")
	a.stream = a.entries[0]
	if got := a.retire(1); got != nil {
		t.Fatalf("retired the streaming block: %q", got)
	}
}

// groupsOpen is keyed by position, so dropping the front block has to slide
// every key down or the wrong runs come back open.
func TestDroppingAnEntryShiftsGroupKeys(t *testing.T) {
	a := retireApp("one", "two", "three")
	a.groupsOpen = map[int]bool{0: true, 2: true}
	a.dropFirstEntry()
	if a.groupsOpen[0] != false && len(a.groupsOpen) != 1 {
		t.Fatalf("group keys = %v; want only the shifted 1", a.groupsOpen)
	}
	if !a.groupsOpen[1] {
		t.Fatalf("group at 2 did not shift to 1: %v", a.groupsOpen)
	}
}

// A retired tool must leave the id map, or a late result is written into a
// block nothing renders.
func TestRetiringAToolClearsItsID(t *testing.T) {
	a := retireApp("one", "two")
	a.tools["call_1"] = a.entries[0]
	a.dropFirstEntry()
	if _, ok := a.tools["call_1"]; ok {
		t.Fatal("the retired tool is still reachable by id")
	}
}
