package tui

import "strings"

// Item is one row of a selectable list — a model, a provider, a completion.
type Item struct {
	Value       string
	Label       string
	Description string
}

// matchItems is loop's default search predicate: case-insensitive substring
// over value, label, and description, so `/model son` finds a Sonnet by its
// description even when the id says nothing about it.
func matchItems(items []Item, query string) []Item {
	if query == "" {
		return append([]Item{}, items...)
	}
	q := strings.ToLower(query)
	var out []Item
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.Value), q) ||
			strings.Contains(strings.ToLower(it.Label), q) ||
			strings.Contains(strings.ToLower(it.Description), q) {
			out = append(out, it)
		}
	}
	return out
}

// truncate cuts s to width cells, marking the cut with an ellipsis.
func truncate(s string, width int) string {
	if visibleWidth(s) <= width {
		return s
	}
	if width <= 1 {
		return truncateToWidth(s, width, "")
	}
	return truncateToWidth(s, width-1, "") + "…"
}

// padRight pads s out to width cells.
func padRight(s string, width int) string {
	if n := width - visibleWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// matchCommands filters a command list by name.
//
// Name only, and prefix-first: matching descriptions too means typing `co`
// offers `help` because its description happens to contain "commands", which
// is worse than useless — the list you are looking at stops corresponding to
// what you typed.
func matchCommands(items []Item, query string) []Item {
	if query == "" {
		return append([]Item{}, items...)
	}
	q := strings.ToLower(query)
	var exact, prefix, contains []Item
	for _, it := range items {
		name := strings.ToLower(it.Label)
		switch {
		// An EXACT name always wins. Without this, typing a command's full
		// name and pressing Enter runs a different command: `/session` sorts
		// after `/sessions` in the table, so the longer one was highlighted
		// and Enter ran it.
		case name == q:
			exact = append(exact, it)
		case strings.HasPrefix(name, q):
			prefix = append(prefix, it)
		case strings.Contains(name, q):
			contains = append(contains, it)
		}
	}
	return append(append(exact, prefix...), contains...)
}

// PadRight pads s to width CELLS, for callers outside this package building
// aligned columns.
//
// Exported because every column in the app was padding by byte length, which
// silently misaligns the moment a label contains anything non-ASCII — an
// arrow, a multiplication sign, a CJK character.
func PadRight(s string, width int) string { return padRight(s, width) }

// Truncate cuts s to width cells with an ellipsis.
func Truncate(s string, width int) string { return truncate(s, width) }

// VisibleWidth is the cell width of a string, for callers sizing columns.
func VisibleWidth(s string) int { return visibleWidth(s) }
