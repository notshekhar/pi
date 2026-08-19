package tui

import (
	"strings"
	"testing"
)

func TestRenderTodosEmptyIsHidden(t *testing.T) {
	if got := renderTodos(testTheme(), nil, 60); got != nil {
		t.Errorf("an empty plan should render nothing, got %q", got)
	}
}

func TestRenderTodosIsLoopsChecklist(t *testing.T) {
	items := []TodoItem{
		{Content: "read the file", Status: TodoCompleted},
		{Content: "make the change", ActiveForm: "making the change", Status: TodoInProgress},
		{Content: "run the tests", Status: TodoPending},
		{Content: "rewrite the parser", Status: TodoCancelled},
	}
	got := stripANSI(strings.Join(renderTodos(testTheme(), items, 60), "\n"))
	// A ruled header carrying the count, drawn to the full width — loop's,
	// and the row the panel is recognised by.
	if !strings.HasPrefix(got, "─ todos (1/4) ─") {
		t.Errorf("header is not loop's ruled title:\n%s", got)
	}
	for _, want := range []string{"[x] read the file", "[>] making the change", "[ ] run the tests", "[-] rewrite the parser"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing row %q:\n%s", want, got)
		}
	}
	// The in-progress row says what is HAPPENING, so its imperative form must
	// not be what is drawn.
	if strings.Contains(got, "[>] make the change") {
		t.Errorf("in-progress row used the imperative, not the active form:\n%s", got)
	}
}

// An in-progress item with no active form still has to say something.
func TestInProgressFallsBackToTheContent(t *testing.T) {
	items := []TodoItem{{Content: "make the change", Status: TodoInProgress}}
	got := stripANSI(strings.Join(renderTodos(testTheme(), items, 60), "\n"))
	if !strings.Contains(got, "[>] make the change") {
		t.Errorf("missing fallback row:\n%s", got)
	}
}

// The retire line is the only trace a plan leaves once the panel is gone.
func TestTodoRetireLine(t *testing.T) {
	done := []TodoItem{{Status: TodoCompleted}, {Status: TodoCancelled}}
	if got := TodoRetireLine(done); got != "todos: all 2 done" {
		t.Errorf("finished plan retired as %q", got)
	}
	open := []TodoItem{{Status: TodoCompleted}, {Status: TodoInProgress}, {Status: TodoPending}}
	if got := TodoRetireLine(open); got != "todos: 2 of 3 open" {
		t.Errorf("unfinished plan retired as %q", got)
	}
}

// A long plan must not push the editor off the screen.
func TestRenderTodosCapsItsHeight(t *testing.T) {
	items := make([]TodoItem, 20)
	for i := range items {
		items[i] = TodoItem{Content: "step", Status: TodoPending}
	}
	lines := renderTodos(testTheme(), items, 60)
	// header + capped rows + the "… N more" line
	if len(lines) > maxTodoRows+2 {
		t.Errorf("panel is %d lines, want at most %d", len(lines), maxTodoRows+2)
	}
	if !strings.Contains(stripANSI(strings.Join(lines, "\n")), "more") {
		t.Error("no overflow indicator")
	}
}

// What survives the clip is chosen by PRIORITY, not by a window: the
// in-progress item, then pending work, then the most recent finished step.
// A sliding window measured from the first unfinished item pushes the
// in-progress row off the panel as soon as the plan finishes out of order.
func TestRenderTodosKeepsTheWorkThatMatters(t *testing.T) {
	items := make([]TodoItem, 12)
	for i := range items {
		items[i] = TodoItem{Content: "done step", Status: TodoCompleted}
	}
	items[0] = TodoItem{Content: "CURRENT WORK", ActiveForm: "CURRENT WORK", Status: TodoInProgress}
	items[11] = TodoItem{Content: "LATER", Status: TodoPending}

	got := stripANSI(strings.Join(renderTodos(testTheme(), items, 60), "\n"))
	if !strings.Contains(got, "CURRENT WORK") {
		t.Errorf("the in-progress item was clipped away:\n%s", got)
	}
	if !strings.Contains(got, "LATER") {
		t.Errorf("pending work was clipped away:\n%s", got)
	}
	// And the rows that survive stay in the plan's own order.
	if strings.Index(got, "CURRENT WORK") > strings.Index(got, "LATER") {
		t.Errorf("clipping reordered the plan:\n%s", got)
	}
	if !strings.Contains(got, "more") {
		t.Errorf("no overflow count:\n%s", got)
	}
}

func TestRenderTodosFitsTheWidth(t *testing.T) {
	items := []TodoItem{
		{Content: strings.Repeat("a very long step description ", 10), Status: TodoInProgress},
	}
	for _, width := range []int{20, 40, 80} {
		for _, line := range renderTodos(testTheme(), items, width) {
			if w := visibleWidth(line); w > width {
				t.Errorf("width %d: %d-cell line %q", width, w, line)
			}
		}
	}
}
