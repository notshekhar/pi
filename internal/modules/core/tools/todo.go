package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/modules/ai"
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

// Valid reports whether a status is one this tool understands.
func (s TodoStatus) Valid() bool {
	switch s {
	case TodoPending, TodoInProgress, TodoCompleted, TodoCancelled:
		return true
	}
	return false
}

// Todo is one item of the plan.
type Todo struct {
	Content string     `json:"content" jsonschema:"description=What the step is; imperative and specific"`
	Status  TodoStatus `json:"status" jsonschema:"description=One of pending, in_progress, completed, or cancelled"`
	// ActiveForm is what the pinned panel shows while the step is running.
	// The panel is a statement about what is happening NOW, and an
	// imperative there reads as an instruction nobody has picked up.
	ActiveForm string `json:"activeForm,omitempty" jsonschema:"description=Present-continuous label shown while in progress, e.g. 'Adding session middleware'"`
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
	Todos []Todo `json:"todos" jsonschema:"description=The complete list; every call replaces the previous one"`
}

// Todos returns the todo tool.
func Todos(t *Context) ai.Tool {
	return ai.NewTool("todo",
		"Maintain your visible task checklist for the current job. Each call REPLACES "+
			"the whole list — resend every item, updated. An empty list clears it.\n\n"+
			"Use it when the work has 3+ distinct steps, the user gives multiple tasks, "+
			"or new instructions arrive mid-task. Skip it for single-step or purely "+
			"informational requests. When in doubt, use it.\n\n"+
			"Rules:\n"+
			"- Keep exactly ONE item in_progress while work remains; mark it before starting the step.\n"+
			"- Give each item an activeForm — the present-continuous label shown while it runs.\n"+
			"- Mark a step completed the moment it is actually done and verified — never on intent, "+
			"never batched at the end.\n"+
			"- If a step is blocked, keep it in_progress and add a follow-up todo describing the blocker.\n"+
			"- Add follow-ups discovered during the work; mark abandoned steps cancelled instead of "+
			"deleting or fake-completing them.\n"+
			"- Preserve user-provided commands verbatim (flags, args, order).\n"+
			"- Items should be specific and actionable; break large work into smaller steps.",
		func(ctx context.Context, a todoArgs) (ai.ToolResult, error) {
			if aborted(ctx) {
				return ai.ToolError("aborted"), nil
			}
			for i, item := range a.Todos {
				if strings.TrimSpace(item.Content) == "" {
					return ai.ToolErrorf("todo %d has no content", i+1), nil
				}
				if !item.Status.Valid() {
					return ai.ToolErrorf(
						"todo %d has status %q; use pending, in_progress, completed, or cancelled",
						i+1, item.Status), nil
				}
			}
			// More than one in-progress item means the plan has stopped
			// describing what is actually happening.
			if n := countStatus(a.Todos, TodoInProgress); n > 1 {
				return ai.ToolErrorf("%d items are in_progress; exactly one may be", n), nil
			}

			t.Todos.Set(a.Todos)
			return ai.ToolText(summarize(a.Todos)), nil
		})
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

// summarize is what the model gets back: enough to confirm the write landed,
// not the whole list echoed into the context on every call.
func summarize(items []Todo) string {
	if len(items) == 0 {
		return "todo list cleared"
	}
	done := countStatus(items, TodoCompleted)
	var current string
	for _, item := range items {
		if item.Status == TodoInProgress {
			current = item.Content
			break
		}
	}
	s := fmt.Sprintf("%d/%d done", done, len(items))
	if n := countStatus(items, TodoCancelled); n > 0 {
		s += fmt.Sprintf(" · %d cancelled", n)
	}
	if current != "" {
		s += " · now: " + current
	}
	return s
}
