package tools

import (
	"context"
	"os"
	"path/filepath"

	"github.com/notshekhar/pi/internal/modules/ai"
)

type writeArgs struct {
	Path    string `json:"path" jsonschema:"description=Path to write, relative or absolute"`
	Content string `json:"content" jsonschema:"description=Full file contents to write"`
}

// Write returns the write tool.
func Write(t *Context) ai.Tool {
	return ai.NewTool("write",
		"Write content to a file, overwriting if it exists. Creates parent directories. Overwriting an existing file requires having read all of it first. Use edit for targeted changes.",
		func(ctx context.Context, a writeArgs) (ai.ToolResult, error) {
			if aborted(ctx) {
				return ai.ToolError("aborted"), nil
			}
			path := Resolve(a.Path, t.CWD)
			if msg := t.Registry.CheckWrite(path, a.Path); msg != "" {
				return ai.ToolError(msg), nil
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return ai.ToolErrorf("could not create directories for %s: %v", a.Path, err), nil
			}
			var old string
			if raw, err := os.ReadFile(path); err == nil {
				old = string(raw)
			}
			if err := os.WriteFile(path, []byte(a.Content), 0o644); err != nil {
				return ai.ToolErrorf("could not write %s: %v", a.Path, err), nil
			}
			t.Registry.RecordModified(path)
			if old == "" {
				return ai.ToolTextf("Wrote %s (%d bytes).", a.Path, len(a.Content)), nil
			}
			diff := generateDiff(normalizeToLF(old), normalizeToLF(a.Content), 4)
			if lines := countIn(diff, "\n") + 1; lines > 200 {
				// The model already supplied the content; keep the result short.
				return ai.ToolTextf("Wrote %s (%d bytes).", a.Path, len(a.Content)), nil
			}
			return ai.ToolTextf("Wrote %s.\n\n%s", a.Path, diff), nil
		})
}
