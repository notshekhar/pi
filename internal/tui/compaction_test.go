package tui

import (
	"strings"
	"testing"
)

func renderedCompaction(st CompactionState) string {
	theme := NewTheme(NightPalette)
	md := NewMarkdown(MarkdownThemeFor(theme), nil)
	return stripANSI(strings.Join(RenderCompaction(theme, md, st, 80), "\n"))
}

// Collapsed, the box says how big the context was and offers → only while
// selected — the only time → does anything.
func TestCompactionBoxCollapsed(t *testing.T) {
	out := renderedCompaction(CompactionState{Summary: "## Goal\nSECRET_SUMMARY", TokensBefore: 84_185})
	if !strings.Contains(out, "[compaction]") || !strings.Contains(out, "Compacted from 84,185 tokens") {
		t.Errorf("collapsed box:\n%s", out)
	}
	if strings.Contains(out, "SECRET_SUMMARY") || strings.Contains(out, "to expand") {
		t.Errorf("collapsed, unselected box shows the summary or a hint:\n%s", out)
	}
	selected := renderedCompaction(CompactionState{Summary: "x", TokensBefore: 1, Selected: true, Nav: true})
	if !strings.Contains(selected, "to expand") {
		t.Errorf("selected box has no hint:\n%s", selected)
	}
}

func TestCompactionBoxExpandedAndRollover(t *testing.T) {
	out := renderedCompaction(CompactionState{Summary: "SECRET_SUMMARY", TokensBefore: 1_000, Expanded: true})
	if !strings.Contains(out, "SECRET_SUMMARY") {
		t.Errorf("expanded box lacks the summary:\n%s", out)
	}
	roll := renderedCompaction(CompactionState{Handoff: "RECORD", TokensBefore: 10, Expanded: true})
	if !strings.Contains(roll, "[fresh context]") || !strings.Contains(roll, "RECORD") {
		t.Errorf("rollover box:\n%s", roll)
	}
}

func TestCompactionIsANavEntry(t *testing.T) {
	if !(&entry{kind: entCompaction}).selectable() {
		t.Error("the compaction box is not selectable")
	}
}

func TestThousands(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1_000: "1,000", 84_185: "84,185", 1_234_567: "1,234,567"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q", n, got)
		}
	}
}
