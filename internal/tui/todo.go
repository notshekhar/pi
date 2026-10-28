package tui

import (
	"fmt"
	"strings"
)

// The pinned todo panel — loop's, row for row.
//
// Chrome, not a transcript block: it sits above the editor and shows the
// CURRENT plan, so it must not scroll away with the conversation. A plan you
// have to scroll back to find is a plan you stop reading, which defeats the
// point of the agent writing one down.
//
// The look is loop's `TodoPanel` and it is deliberately a checklist rather
// than a status display: a ruled header carrying the count, then one bracket
// per item — `[ ]`, `[>]`, `[x]`, `[-]` — which is the notation anyone who
// has written a markdown checklist already reads without a legend. Geometric
// status glyphs were the earlier version here and they are worse for exactly
// that reason: ○ ◐ ● is a key you have to learn, and learn per-app.
//
// It is terse on purpose — one line per item, truncated to the width. The
// panel is a glance; the transcript holds the detail.

// TodoStatus mirrors the core status without importing it: `tui` sits beside
// `core`, not under it, and one small enum is a far smaller cost than a
// dependency edge between them.
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
	// TodoCancelled is a step the plan gave up on. It is kept and struck
	// through rather than deleted, because a plan that silently loses a step
	// cannot be told apart from one that finished it.
	TodoCancelled TodoStatus = "cancelled"
)

// terminal reports whether an item has no work left on it.
func (s TodoStatus) terminal() bool { return s == TodoCompleted || s == TodoCancelled }

// TodoItem is one row of the panel.
type TodoItem struct {
	Content string
	// ActiveForm is the present-continuous label shown while the item is in
	// progress — "Adding session middleware" rather than "Add session
	// middleware". The pinned row is a statement about what is happening
	// NOW, and the imperative reads as an instruction nobody has taken up.
	ActiveForm string
	Status     TodoStatus
}

// maxTodoRows caps the panel's height. A plan longer than this is summarised
// rather than allowed to push the editor off the screen.
const maxTodoRows = 6

// SetTodos replaces the pinned plan. An empty list hides the panel.
func (a *App) SetTodos(items []TodoItem) {
	a.todos = items
}

// Todos is the current pinned plan.
func (a *App) Todos() []TodoItem { return a.todos }

// todoRow is one item, styled by status.
func todoRow(t *Theme, item TodoItem) string {
	switch item.Status {
	case TodoCompleted:
		return t.Fg(SlotDim, "[x] "+item.Content)
	case TodoCancelled:
		return t.Fg(SlotDim, t.Strike("[-] "+item.Content))
	case TodoInProgress:
		text := strings.TrimSpace(item.ActiveForm)
		if text == "" {
			text = item.Content
		}
		return t.Bold(t.Fg(SlotAccent, "[>] "+text))
	default:
		return t.Fg(SlotDim, "[ ] "+item.Content)
	}
}

// visibleTodos picks which items survive when the list is taller than the
// panel: the in-progress item always, then pending in list order, then the
// most recently finished — but rendered in the list's OWN order, so the plan
// still reads top to bottom.
//
// Priority rather than a sliding window because the window's answer depends
// on where the finished prefix ends, which is not the same question as what
// is worth showing: a plan whose middle is done would push its in-progress
// item off the panel.
func visibleTodos(items []TodoItem, maxItems int) (shown []TodoItem, hidden int) {
	if len(items) <= maxItems {
		return items, 0
	}
	keep := map[int]bool{}
	var order []int
	for i, item := range items {
		if item.Status == TodoInProgress {
			order = append(order, i)
		}
	}
	for i, item := range items {
		if item.Status == TodoPending {
			order = append(order, i)
		}
	}
	// Newest first: of the finished work, the step just completed is the one
	// still worth seeing.
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Status.terminal() {
			order = append(order, i)
		}
	}
	for _, i := range order {
		if len(keep) >= maxItems {
			break
		}
		keep[i] = true
	}
	for i, item := range items {
		if keep[i] {
			shown = append(shown, item)
		}
	}
	return shown, len(items) - len(keep)
}

// renderTodos draws the panel, or nothing when there is no plan.
func renderTodos(t *Theme, items []TodoItem, width int) []string {
	if len(items) == 0 {
		return nil
	}

	done := 0
	for _, item := range items {
		if item.Status == TodoCompleted {
			done++
		}
	}

	title := fmt.Sprintf("─ todos (%d/%d) ", done, len(items))
	if w := visibleWidth(title); w < width {
		title += strings.Repeat("─", width-w)
	} else {
		title = truncateToWidth(title, width, "")
	}
	lines := []string{t.Fg(SlotDim, title)}

	// The last row is spent on "+N more" when there is one, so the count is
	// never itself the thing that got clipped.
	shown, hidden := visibleTodos(items, maxTodoRows)
	if hidden > 0 {
		shown, hidden = visibleTodos(items, maxTodoRows-1)
	}
	for _, item := range shown {
		lines = append(lines, fitRow(todoRow(t, item), width))
	}
	if hidden > 0 {
		lines = append(lines, t.Fg(SlotDim, fmt.Sprintf("    +%d more", hidden)))
	}
	return lines
}

// TodoRetireLine is the one-line record left in the transcript when the panel
// retires: a finished plan reads "todos: all 5 done", one with work left
// "todos: 3 of 7 open".
//
// The panel is chrome and chrome does not survive the turn, so without this
// line a plan would vanish at the end of the turn that made it and leave no
// trace of what was or was not finished.
func TodoRetireLine(items []TodoItem) string {
	open := 0
	for _, item := range items {
		if !item.Status.terminal() {
			open++
		}
	}
	if open == 0 {
		return fmt.Sprintf("todos: all %d done", len(items))
	}
	return fmt.Sprintf("todos: %d of %d open", open, len(items))
}
