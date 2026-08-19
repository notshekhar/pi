package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// callTodo invokes the tool the way the model would.
func callTodo(t *testing.T, c *Context, todos []Todo) (string, bool) {
	t.Helper()
	tool := Todos(c)
	input, err := json.Marshal(todoArgs{Todos: todos})
	if err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	switch out := res.Output().(type) {
	case provider.ToolOutputText:
		return out.Value, false
	case provider.ToolOutputErrorText:
		return out.Value, true
	}
	t.Fatalf("unexpected output %T", res.Output())
	return "", false
}

func newContext() *Context {
	return &Context{CWD: "/repo", Registry: NewRegistry(), Todos: &TodoList{}}
}

func TestTodoStoresTheList(t *testing.T) {
	c := newContext()
	out, isErr := callTodo(t, c, []Todo{
		{Content: "read the file", Status: TodoCompleted},
		{Content: "make the change", Status: TodoInProgress},
		{Content: "run the tests", Status: TodoPending},
	})
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	items := c.Todos.Items()
	if len(items) != 3 {
		t.Fatalf("stored %d items, want 3", len(items))
	}
	if items[1].Content != "make the change" {
		t.Errorf("items = %+v", items)
	}
	// The model gets a summary, not the list echoed back into its context.
	if !strings.Contains(out, "1/3 done") || !strings.Contains(out, "make the change") {
		t.Errorf("summary = %q", out)
	}
}

// Each call replaces the list; patching would need stable ids, and an agent
// that miscounts an index silently corrupts its own plan.
func TestTodoReplacesRatherThanAppends(t *testing.T) {
	c := newContext()
	callTodo(t, c, []Todo{{Content: "first", Status: TodoPending}})
	callTodo(t, c, []Todo{{Content: "second", Status: TodoPending}})

	items := c.Todos.Items()
	if len(items) != 1 || items[0].Content != "second" {
		t.Errorf("items = %+v, want only the second list", items)
	}
}

func TestTodoRejectsBadInput(t *testing.T) {
	cases := []struct {
		name  string
		todos []Todo
		want  string
	}{
		{"empty content", []Todo{{Content: "  ", Status: TodoPending}}, "no content"},
		{"unknown status", []Todo{{Content: "x", Status: "started"}}, "started"},
		{
			"two in progress",
			[]Todo{
				{Content: "a", Status: TodoInProgress},
				{Content: "b", Status: TodoInProgress},
			},
			"in_progress",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := newContext()
			out, isErr := callTodo(t, ctx, c.todos)
			if !isErr {
				t.Fatalf("expected an error, got %q", out)
			}
			if !strings.Contains(out, c.want) {
				t.Errorf("error %q does not mention %q", out, c.want)
			}
			if len(ctx.Todos.Items()) != 0 {
				t.Error("a rejected call still wrote the list")
			}
		})
	}
}

func TestTodoAllowsExactlyOneInProgress(t *testing.T) {
	c := newContext()
	if out, isErr := callTodo(t, c, []Todo{
		{Content: "a", Status: TodoInProgress},
		{Content: "b", Status: TodoPending},
	}); isErr {
		t.Fatalf("one in_progress should be fine: %s", out)
	}
}

func TestTodoListDone(t *testing.T) {
	l := &TodoList{}
	// An empty list is "done" — there is nothing outstanding to show.
	if !l.Done() {
		t.Error("an empty list should report done")
	}
	l.Set([]Todo{{Content: "a", Status: TodoCompleted}, {Content: "b", Status: TodoPending}})
	if l.Done() {
		t.Error("a list with a pending item is not done")
	}
	l.Set([]Todo{{Content: "a", Status: TodoCompleted}})
	if !l.Done() {
		t.Error("an all-completed list is done")
	}
}

func TestTodoListClear(t *testing.T) {
	l := &TodoList{}
	l.Set([]Todo{{Content: "a", Status: TodoPending}})
	l.Clear()
	if len(l.Items()) != 0 {
		t.Error("Clear left items behind")
	}
}

// Items must hand back a copy, or a caller ranging over it while the model
// writes a new plan races the tool.
func TestTodoListItemsIsACopy(t *testing.T) {
	l := &TodoList{}
	l.Set([]Todo{{Content: "original", Status: TodoPending}})
	items := l.Items()
	items[0].Content = "mutated"
	if l.Items()[0].Content != "original" {
		t.Error("Items exposed the internal slice")
	}
}

func TestTodoIsRegistered(t *testing.T) {
	c := newContext()
	for _, tool := range All(c) {
		if tool.Name() == "todo" {
			return
		}
	}
	t.Error("the todo tool is not in the built-in set")
}

// A Context built without a list must still work — All fills it in.
func TestAllInitialisesTodos(t *testing.T) {
	c := &Context{CWD: "/repo"}
	All(c)
	if c.Todos == nil {
		t.Fatal("All left Todos nil")
	}
	c.Todos.Set([]Todo{{Content: "x", Status: TodoPending}})
}
