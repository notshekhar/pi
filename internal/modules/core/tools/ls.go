package tools

import (
	"context"
	"os"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai"
)

type lsArgs struct {
	Path  string `json:"path,omitempty" jsonschema:"description=Directory to list; defaults to the working directory"`
	Limit *int   `json:"limit,omitempty" jsonschema:"description=Maximum entries (default 500)"`
}

// Ls returns the ls tool.
func Ls(t *Context) ai.Tool {
	return ai.NewTool("ls",
		"List directory contents, sorted alphabetically. Directories end with /. Includes dotfiles.",
		func(ctx context.Context, a lsArgs) (ai.ToolResult, error) {
			if aborted(ctx) {
				return ai.ToolError("aborted"), nil
			}
			path := Resolve(a.Path, t.CWD)
			info, err := os.Stat(path)
			if err != nil {
				return ai.ToolErrorf("path not found: %s", a.Path), nil
			}
			if !info.IsDir() {
				return ai.ToolErrorf("not a directory: %s", a.Path), nil
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				return ai.ToolErrorf("cannot read directory: %v", err), nil
			}
			sort.Slice(entries, func(i, j int) bool {
				return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
			})
			limit := 500
			if a.Limit != nil && *a.Limit > 0 {
				limit = *a.Limit
			}
			var b strings.Builder
			n := 0
			for _, e := range entries {
				if n >= limit {
					b.WriteString("\n… more entries")
					break
				}
				name := e.Name()
				if e.IsDir() {
					name += "/"
				}
				if n > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(name)
				n++
			}
			if b.Len() == 0 {
				return ai.ToolText("(empty)"), nil
			}
			return ai.ToolText(b.String()), nil
		})
}
