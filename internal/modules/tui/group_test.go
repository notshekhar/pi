package tui

import (
	"strings"
	"testing"
	"time"
)

// settledTool is a finished, foldable tool entry.
func settledTool(name string) *entry {
	return &entry{kind: entTool, tool: ToolState{
		Name: name, Output: "ok", FinishedAt: time.Now().Add(-time.Hour),
	}}
}

// The vocabulary, kind by kind — loop's, and the point of it is that the
// header is a sentence rather than a count.
func TestVerbGroupLabel(t *testing.T) {
	cases := []struct {
		tools []string
		want  string
	}{
		{[]string{"read", "read", "read"}, "Read 3 files"},
		{[]string{"read"}, "Read 1 file"},
		{[]string{"write", "edit"}, "Edited 2 files"},
		{[]string{"ls"}, "Listed 1 dir"},
		{[]string{"bash", "bash"}, "Ran 2 commands"},
		// Two tools, ONE kind: a search is a search however it was spelled.
		{[]string{"grep", "glob", "find"}, "Searched 3 patterns"},
		// One verb, two nouns: these stay apart, because "Read 3 files" would
		// be a lie about what was read.
		{[]string{"read", "read", "skill"}, "Read 2 files, Read 1 skill"},
		// Segment order follows first appearance, not the map.
		{[]string{"ls", "read", "ls"}, "Listed 2 dirs, Read 1 file"},
		{[]string{"task"}, "Ran 1 subagent"},
		{[]string{"todo", "todo"}, "Updated 2 todo lists"},
	}
	for _, c := range cases {
		members := make([]groupMember, 0, len(c.tools))
		for _, tool := range c.tools {
			members = append(members, groupMember{tool: tool})
		}
		if got, _ := verbGroupLabel(members); got != c.want {
			t.Errorf("%v → %q, want %q", c.tools, got, c.want)
		}
	}
}

// A tool we did not write is described by the only thing actually known about
// it: where it came from. Borrowing a builtin's noun off a similar-looking
// name is how a run of Sentry lookups ends up reading "Listed 2 dirs".
func TestUnknownToolsSayOnlyWhereTheyCameFrom(t *testing.T) {
	ClearToolVerbGroups()
	if got, _ := verbGroupLabel([]groupMember{{tool: "sentry__list_errors"}, {tool: "sentry__get_error"}}); got != "Called 2 MCP tools" {
		t.Errorf("MCP tools = %q", got)
	}
	if got, _ := verbGroupLabel([]groupMember{{tool: "lsp_hover"}}); got != "Called 1 extension tool" {
		t.Errorf("extension tool = %q", got)
	}
	// A registration is the ONLY way a third-party tool gets real grammar.
	RegisterToolVerbGroup("lsp_hover", kindSearch)
	defer ClearToolVerbGroups()
	if got, _ := verbGroupLabel([]groupMember{{tool: "lsp_hover"}}); got != "Searched 1 pattern" {
		t.Errorf("registered tool = %q", got)
	}
}

// Any member still running makes the whole run present-tense: the run as a
// whole is still happening.
func TestRunningRunsReadPresentTense(t *testing.T) {
	got, _ := verbGroupLabel([]groupMember{{tool: "read"}, {tool: "read", running: true}})
	if got != "Reading 2 files" {
		t.Errorf("running run = %q", got)
	}
}

// ONE member is enough. "Read 1 file" is already tighter than the row it
// replaces, and folding from the first call means the second JOINS a header
// instead of the row collapsing under you mid-turn.
func TestASingleCallAlreadyFolds(t *testing.T) {
	groups := findGroups([]*entry{settledTool("read")}, nil)
	g, ok := groups[0]
	if !ok {
		t.Fatalf("a lone call did not fold: %v", groups)
	}
	if g.count != 1 || g.label != "Read 1 file" {
		t.Errorf("group = %+v", g)
	}
}

func TestFindGroupsFoldsARun(t *testing.T) {
	entries := []*entry{settledTool("read"), settledTool("read"), settledTool("read")}
	g, ok := findGroups(entries, nil)[0]
	if !ok {
		t.Fatal("no group found")
	}
	if g.count != 3 || g.label != "Read 3 files" {
		t.Errorf("group = %+v", g)
	}
}

// A run is ADJACENCY, not sameness: a mixed stretch is one group whose label
// names each kind, which is how loop reads.
func TestMixedRunsAreOneGroup(t *testing.T) {
	entries := []*entry{
		settledTool("read"), settledTool("read"),
		settledTool("bash"), settledTool("ls"),
	}
	groups := findGroups(entries, nil)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d: %v", len(groups), groups)
	}
	g := groups[0]
	if g.count != 4 || g.label != "Read 2 files, Ran 1 command, Listed 1 dir" {
		t.Errorf("group = %+v", g)
	}
}

// A running call is the one thing the user is waiting on; it must stay
// visible. An opened row must too, and so must the surfaces you have to act
// on.
func TestFindGroupsExcludesCallsThatMustStayVisible(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*entry)
	}{
		{"running", func(e *entry) { e.tool.IsPartial = true }},
		{"interrupted", func(e *entry) { e.tool.Interrupted = true }},
		{"expanded", func(e *entry) { e.expanded = true }},
		{"selected", func(e *entry) { e.selected = true }},
		{"a question", func(e *entry) { e.tool.Name = "ask" }},
		{"a plan", func(e *entry) { e.tool.Name = "plan" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := settledTool("read")
			c.mutate(e)
			if groups := findGroups([]*entry{e}, nil); len(groups) != 0 {
				t.Errorf("%s was folded away: %v", c.name, groups)
			}
		})
	}
}

// A FAILURE folds — but the header says so. Folding must never be a way to
// lose bad news.
func TestAFailureIsReportedOnTheHeader(t *testing.T) {
	bad := settledTool("read")
	bad.tool.IsError = true
	entries := []*entry{settledTool("read"), bad, settledTool("read")}
	g, ok := findGroups(entries, nil)[0]
	if !ok {
		t.Fatal("a run containing a failure did not fold")
	}
	if g.failed != 1 {
		t.Errorf("failures = %d, want 1", g.failed)
	}
	got := plain(renderGroupHeader(testTheme(), g, false, true, 60))
	if !strings.Contains(got, "1 failed") {
		t.Errorf("the header hid the failure: %q", got)
	}
}

func TestFindGroupsRespectsOpened(t *testing.T) {
	entries := []*entry{settledTool("read"), settledTool("read"), settledTool("read")}
	groups := findGroups(entries, map[int]bool{0: true})
	if !groups[0].opened {
		t.Error("an opened group should report itself opened")
	}
}

// Openness is held against EVERY member, not just the head.
//
// The head is not stable: opening the first call drops it out of the run,
// which promotes the second to head. Keyed on the head alone that lookup
// misses, and the rest snap shut into a fresh header — the group appearing to
// re-collapse itself the moment you opened something inside it.
func TestOpennessSurvivesTheHeadChanging(t *testing.T) {
	entries := []*entry{settledTool("read"), settledTool("read"), settledTool("read")}
	opened := map[int]bool{0: true, 1: true, 2: true}
	// The user opens the first call, so it leaves the run.
	entries[0].expanded = true
	groups := findGroups(entries, opened)
	g, ok := groups[1]
	if !ok {
		t.Fatalf("the remaining calls did not form a group: %v", groups)
	}
	if !g.opened {
		t.Error("the group snapped shut when its head changed")
	}
}

func TestGroupHeaderRendersAndFits(t *testing.T) {
	g := groupOf("read", "read", "read", "read", "read")
	lines := renderGroupHeader(testTheme(), g, false, true, 60)
	got := plain(lines)
	if !strings.Contains(got, "Read 5 files") {
		t.Errorf("header = %q", got)
	}
	for _, line := range lines {
		if w := visibleWidth(line); w > 60 {
			t.Errorf("%d-cell header line: %q", w, line)
		}
	}
}

func TestGroupHeaderOffersHintWhenSelected(t *testing.T) {
	g := groupOf("bash", "bash", "bash", "bash")
	got := plain(renderGroupHeader(testTheme(), g, true, true, 60))
	if !strings.Contains(got, "to open") {
		t.Errorf("no hint on a selected group: %q", got)
	}
}

// A row cached during its finish flash must be drawn once more when the flash
// expires, or it keeps the flash's heavy rail forever instead of settling.
func TestRowSettlesAfterTheFinishFlash(t *testing.T) {
	th := testTheme()
	e := &entry{kind: entTool, tool: ToolState{
		Name: "bash", Output: "ok", FinishedAt: time.Now(),
	}}

	flashing := strings.Join(e.render(th, 60, 0, false, false), "\n")
	if !e.animated() {
		t.Fatal("a just-finished row should still be animating")
	}
	if !strings.Contains(stripANSI(flashing), railGlyph) {
		t.Errorf("flash frame should carry the heavy rail: %q", stripANSI(flashing))
	}

	// Move past the flash without touching the entry, as the clock does.
	e.tool.FinishedAt = time.Now().Add(-2 * FinishFlash)
	settled := stripANSI(strings.Join(e.render(th, 60, 0, false, false), "\n"))
	if !strings.Contains(settled, railCollapsed) {
		t.Errorf("row did not settle to the light rail: %q", settled)
	}
}

// Live is a STATE of noir, not a mode: with it on, a finished run stays
// folded while you are typing, which outside it only navigation does.
func TestLiveVariantFoldsOutsideNav(t *testing.T) {
	a := &App{entries: []*entry{settledTool("read"), settledTool("read"), settledTool("read")}}

	if len(a.groups()) != 0 {
		t.Fatal("the default transcript folded a run outside navigation")
	}
	a.SetLiveVariant(true)
	if len(a.groups()) != 1 {
		t.Fatalf("live did not fold the run: %v", a.groups())
	}
	// Navigation folds either way, so entering and leaving it must not
	// disturb the state the user chose. loop had to fix this twice: leaving
	// nav turned live off unconditionally, which reads as the transcript
	// un-folding itself.
	a.enterNav()
	a.exitNav()
	if !a.LiveVariant() || len(a.groups()) != 1 {
		t.Fatal("leaving navigation dropped the live variant")
	}
	// And "show me everything" still means everything.
	a.expandAll = true
	if len(a.groups()) != 0 {
		t.Fatal("expand-all did not dissolve the folds")
	}
}
