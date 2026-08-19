package tui

import "testing"

func TestMatchItemsFiltersValueLabelDescription(t *testing.T) {
	items := []Item{
		{Value: "kimi/k3", Label: "k3", Description: "Kimi K3 (Code plan)"},
		{Value: "kimi/kimi-for-coding", Label: "kimi-for-coding", Description: "Kimi K2.7 Coding"},
		{Value: "anthropic/claude-sonnet-4-5", Label: "claude-sonnet-4-5", Description: "Claude Sonnet 4.5"},
	}
	got := matchItems(items, "k3")
	if len(got) != 1 || got[0].Value != "kimi/k3" {
		t.Fatalf("k3 → %+v", got)
	}
	got = matchItems(items, "coding")
	if len(got) != 1 || got[0].Label != "kimi-for-coding" {
		t.Fatalf("coding → %+v", got)
	}
	// Matches the description, not the id — the whole point of searching all
	// three fields.
	if got = matchItems(items, "sonnet"); len(got) != 1 {
		t.Fatalf("sonnet → %+v", got)
	}
	if n := len(matchItems(items, "zzz")); n != 0 {
		t.Fatalf("zzz matched %d", n)
	}
}

func TestTruncateAndPadAreCellAccurate(t *testing.T) {
	// Both must measure cells, not runes, or a CJK label misaligns a column.
	if got := truncate("日本語です", 4); visibleWidth(got) > 4 {
		t.Errorf("truncate left %d cells: %q", visibleWidth(got), got)
	}
	if got := padRight("日本", 6); visibleWidth(got) != 6 {
		t.Errorf("padRight = %d cells, want 6", visibleWidth(got))
	}
}
