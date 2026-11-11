package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/ai"
)

// The todo list: the agent's own plan, visible to the user.
//
// It exists for the reader, not the model — a model that can hold a plan in
// its head does not need to write it down. What it buys is that a long
// multi-step turn stops being an opaque wall of tool calls: you can see what
// was planned, what is happening now, and what is left, and notice early when
// the agent is working on the wrong thing.
//
// The whole list is replaced on every call rather than patched. Patching
// needs stable ids, and an agent that miscounts an index silently corrupts
// its own plan; replacing cannot drift.

// TodoStatus is where an item has got to.
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
	// TodoCancelled is a step the plan gave up on. Without it the model's
	// only ways to drop a step are deleting it — which loses the fact that
	// it was ever planned — or marking it completed, which is a lie the
	// user has no way to catch.
	TodoCancelled TodoStatus = "cancelled"
)

// Todo is one item of the plan.
type Todo struct {
	Content string     `json:"content"`
	Status  TodoStatus `json:"status"`
	// ActiveForm is what the pinned panel shows while the step is running.
	// The panel is a statement about what is happening NOW, and an
	// imperative there reads as an instruction nobody has picked up.
	ActiveForm string `json:"activeForm,omitempty"`
}

// TodoList is the current plan, guarded because the tool runs on the model's
// goroutine while the renderer reads it on its own.
type TodoList struct {
	mu    sync.Mutex
	items []Todo
}

// Set replaces the list.
func (l *TodoList) Set(items []Todo) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append([]Todo{}, items...)
}

// Items is a copy of the current list.
func (l *TodoList) Items() []Todo {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Todo{}, l.items...)
}

// Clear empties the list.
func (l *TodoList) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = nil
}

// Done reports whether every item is completed — the signal for retiring the
// panel at the end of a turn.
func (l *TodoList) Done() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.items) == 0 {
		return true
	}
	for _, item := range l.items {
		// Cancelled counts as finished with: there is no work left on it, and
		// a plan held open by a step the agent abandoned never retires.
		if item.Status != TodoCompleted && item.Status != TodoCancelled {
			return false
		}
	}
	return true
}

type todoArgs struct {
	Todos []Todo `json:"todos"`
}

// maxTodos caps the list: past this the plan is tracked at too fine a grain.
const maxTodos = 50

// Todos returns the todo tool.
func Todos(t *Context) ai.Tool {
	return LoopTool("todo", func(_ context.Context, a todoArgs) (ai.ToolResult, error) {
		// A rejection is ordinary text, not an error: the list was understood,
		// it just has to be resent in a better shape.
		if rejection := validateTodos(a.Todos); rejection != "" {
			return ai.ToolText(rejection), nil
		}
		t.Todos.Set(a.Todos)
		// The list is echoed back so the model's memory of it lives in a
		// result it re-reads, not only in an earlier call's arguments.
		if len(a.Todos) == 0 {
			return ai.ToolText(summarizeTodos(a.Todos)), nil
		}
		return ai.ToolText(summarizeTodos(a.Todos) + "\n" + FormatTodoList(a.Todos)), nil
	})
}

func validateTodos(items []Todo) string {
	if len(items) > maxTodos {
		return fmt.Sprintf("REJECTED: %d items is too many (max %d). Track the plan at a coarser grain.", len(items), maxTodos)
	}
	for _, item := range items {
		if strings.TrimSpace(item.Content) == "" {
			return "REJECTED: every todo needs non-empty content. Resend the full list with each item described."
		}
	}
	// More than one in-progress item means the plan has stopped describing
	// what is actually happening.
	if n := countStatus(items, TodoInProgress); n > 1 {
		return fmt.Sprintf("REJECTED: %d items are in_progress — keep exactly one step in progress at a time. Resend the full list.", n)
	}
	return ""
}

func countStatus(items []Todo, status TodoStatus) int {
	n := 0
	for _, item := range items {
		if item.Status == status {
			n++
		}
	}
	return n
}

func summarizeTodos(items []Todo) string {
	if len(items) == 0 {
		return "Todo list cleared."
	}
	summary := fmt.Sprintf("Todo list updated: %d completed, %d in progress, %d pending",
		countStatus(items, TodoCompleted), countStatus(items, TodoInProgress), countStatus(items, TodoPending))
	if cancelled := countStatus(items, TodoCancelled); cancelled > 0 {
		summary += fmt.Sprintf(", %d cancelled", cancelled)
	}
	return summary + "."
}

// FormatTodoList is the list as numbered lines with each item's status.
func FormatTodoList(items []Todo) string {
	lines := make([]string, len(items))
	for i, item := range items {
		lines[i] = fmt.Sprintf("%d. [%s] %s", i+1, item.Status, item.Content)
	}
	return strings.Join(lines, "\n")
}

func hasActiveTodos(items []Todo) bool {
	return countStatus(items, TodoPending)+countStatus(items, TodoInProgress) > 0
}

// How many tool calls go by before the model is reminded about its list: a
// list left untouched this long has probably fallen behind the work, and a
// job this long with no list probably wants one.
const (
	todoNudgeStaleAfter = 10
	todoNudgeStartAfter = 15
)

const staleTodoNudge = "<system-reminder>The todo list has not been updated in a while. If steps are finished, mark them " +
	"completed now (resend the full list); if the plan changed, update or cancel items to match. " +
	"Ignore if the list is still accurate.</system-reminder>"

const startTodoNudge = "<system-reminder>This task has run for many tool calls without a todo list. If the remaining work has " +
	"3+ distinct steps, create one with the todo tool so progress stays visible. Ignore if the work is " +
	"nearly done or genuinely single-step.</system-reminder>"

// TodoNudger decides, step by step, whether to remind the model about its
// todo list. One nudger lives for one turn.
type TodoNudger struct {
	list        *TodoList
	lastNudgeAt int
	startNudged bool
}

// NewTodoNudger returns a nudger over the turn's list.
func NewTodoNudger(list *TodoList) *TodoNudger { return &TodoNudger{list: list} }

// Nudge returns a reminder for this step, or "". toolCalls is the name of
// every tool called so far this turn, in order.
func (n *TodoNudger) Nudge(toolCalls []string) string {
	total := len(toolCalls)
	lastWrite := -1
	for i, name := range toolCalls {
		if name == "todo" {
			lastWrite = i
		}
	}
	sinceWrite := total - lastWrite - 1
	if hasActiveTodos(n.list.Items()) {
		if sinceWrite >= todoNudgeStaleAfter && total-n.lastNudgeAt >= todoNudgeStaleAfter {
			n.lastNudgeAt = total
			return staleTodoNudge
		}
		return ""
	}
	// No list, or nothing left on it: one gentle prompt to start one.
	if !n.startNudged && lastWrite == -1 && total >= todoNudgeStartAfter {
		n.startNudged = true
		n.lastNudgeAt = total
		return startTodoNudge
	}
	return ""
}
