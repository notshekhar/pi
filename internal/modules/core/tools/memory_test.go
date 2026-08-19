package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

func callMemory(t *testing.T, c *Context, a memoryArgs) (string, bool) {
	t.Helper()
	input, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Memory(c).Execute(context.Background(), input)
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

func memoryContext(t *testing.T) *Context {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	return &Context{CWD: "/repo", Registry: NewRegistry(), Todos: &TodoList{}}
}

func TestMemoryToolSaveAndGet(t *testing.T) {
	c := memoryContext(t)
	if out, isErr := callMemory(t, c, memoryArgs{
		Action: "save", Name: "build", Description: "needs CGO off", Body: "CGO_ENABLED=0",
	}); isErr {
		t.Fatalf("save failed: %s", out)
	}
	out, isErr := callMemory(t, c, memoryArgs{Action: "get", Name: "build"})
	if isErr {
		t.Fatalf("get failed: %s", out)
	}
	if out != "CGO_ENABLED=0" {
		t.Errorf("get returned %q", out)
	}
}

func TestMemoryToolDelete(t *testing.T) {
	c := memoryContext(t)
	callMemory(t, c, memoryArgs{Action: "save", Name: "x", Description: "d", Body: "b"})
	if out, isErr := callMemory(t, c, memoryArgs{Action: "delete", Name: "x"}); isErr {
		t.Fatalf("delete failed: %s", out)
	}
	if _, isErr := callMemory(t, c, memoryArgs{Action: "get", Name: "x"}); !isErr {
		t.Error("the fact survived deletion")
	}
}

func TestMemoryToolRejectsBadInput(t *testing.T) {
	c := memoryContext(t)
	cases := []memoryArgs{
		{Action: "save", Name: "x"},                                      // no description or body
		{Action: "save", Name: "../escape", Description: "d", Body: "b"}, // traversal
		{Action: "get", Name: "never-saved"},
		{Action: "wat", Name: "x"},
	}
	for _, a := range cases {
		if out, isErr := callMemory(t, c, a); !isErr {
			t.Errorf("%+v should have failed, got %q", a, out)
		}
	}
}

func TestMemoryToolList(t *testing.T) {
	c := memoryContext(t)
	if out, _ := callMemory(t, c, memoryArgs{Action: "list"}); !strings.Contains(out, "nothing") {
		t.Errorf("empty list = %q", out)
	}
	callMemory(t, c, memoryArgs{Action: "save", Name: "a", Description: "first", Body: "b"})
	out, _ := callMemory(t, c, memoryArgs{Action: "list"})
	if !strings.Contains(out, "a — first") {
		t.Errorf("list = %q", out)
	}
}

func TestMemoryToolIsRegistered(t *testing.T) {
	for _, tool := range All(memoryContext(t)) {
		if tool.Name() == "memory" {
			return
		}
	}
	t.Error("the memory tool is not registered")
}
