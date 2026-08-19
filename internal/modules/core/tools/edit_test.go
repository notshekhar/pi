package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

func TestEditToolPreservesUntouchedQuotes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.go")
	content := "title = “keep me”\nhello “world”\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tc := &Context{CWD: dir, Registry: NewRegistry()}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	tc.Registry.RecordRead(path, info.ModTime().UnixNano(), 1, 2, true)

	tool := Edit(tc)
	input := `{"path":"f.go","edits":[{"oldText":"hello \"world\"","newText":"hello there"}]}`
	res, err := tool.Execute(context.Background(), json.RawMessage(input))
	if err != nil {
		t.Fatal(err)
	}
	out, ok := res.Output().(provider.ToolOutputText)
	if !ok {
		t.Fatalf("result = %T", res.Output())
	}
	if !strings.Contains(out.Value, "Successfully replaced") {
		t.Fatalf("result: %s", out.Value)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	next := string(got)
	if !strings.Contains(next, "“keep me”") {
		t.Fatalf("untouched curly quotes were rewritten:\n%s", next)
	}
	if !strings.Contains(next, "hello there") {
		t.Fatalf("replacement missing:\n%s", next)
	}
}

func TestEditToolRequiresRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.go")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := Edit(&Context{CWD: dir, Registry: NewRegistry()})
	res, err := tool.Execute(context.Background(), json.RawMessage(
		`{"path":"f.go","edits":[{"oldText":"hello","newText":"hi"}]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	out, ok := res.Output().(provider.ToolOutputErrorText)
	if !ok {
		t.Fatalf("want error, got %T %v", res.Output(), resultText(res))
	}
	if !strings.Contains(out.Value, "has not been read") {
		t.Fatalf("err = %s", out.Value)
	}
}

func resultText(res ai.ToolResult) string {
	switch out := res.Output().(type) {
	case provider.ToolOutputText:
		return out.Value
	case provider.ToolOutputErrorText:
		return out.Value
	default:
		return ""
	}
}
