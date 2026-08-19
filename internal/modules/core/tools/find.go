package tools

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai"
)

// `find` — locate files by name anywhere under a root.
//
// Distinct from `glob`, which matches a shell pattern against one directory
// level and needs you to already know the shape of the tree. `find` walks,
// which is what you want when the question is "where does X live?" rather
// than "what matches this pattern here?".

type findArgs struct {
	Name string `json:"name" jsonschema:"description=Substring or glob to match against file names"`
	Path string `json:"path,omitempty" jsonschema:"description=Directory to search under; defaults to the working directory"`
	Type string `json:"type,omitempty" jsonschema:"description=Restrict to file or dir"`
}

// findLimit caps results. A search that returns two thousand paths has not
// answered the question, it has moved it.
const findLimit = 100

// Find returns the find tool.
func Find(t *Context) ai.Tool {
	return ai.NewTool("find",
		"Find files by name anywhere under a directory. Use it to locate something "+
			"whose path you do not know; use glob when you know the shape of the "+
			"path and ls when you want one directory's contents.",
		func(ctx context.Context, a findArgs) (ai.ToolResult, error) {
			if ctx.Err() != nil {
				return ai.ToolError("aborted"), nil
			}
			needle := strings.TrimSpace(a.Name)
			if needle == "" {
				return ai.ToolError("name is empty"), nil
			}
			root := Resolve(a.Path, t.CWD)

			var hits []string
			truncated := false
			err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					// An unreadable directory is skipped, not fatal: one
					// permission error must not abandon the whole search.
					return nil
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if d.IsDir() && skipDir(d.Name()) && path != root {
					return filepath.SkipDir
				}
				switch a.Type {
				case "file":
					if d.IsDir() {
						return nil
					}
				case "dir":
					if !d.IsDir() {
						return nil
					}
				}
				if !matchName(d.Name(), needle) {
					return nil
				}
				if len(hits) >= findLimit {
					truncated = true
					return filepath.SkipAll
				}
				rel, relErr := filepath.Rel(t.CWD, path)
				if relErr != nil {
					rel = path
				}
				if d.IsDir() {
					rel += "/"
				}
				hits = append(hits, rel)
				return nil
			})
			if err != nil && ctx.Err() != nil {
				return ai.ToolError("aborted"), nil
			}
			if len(hits) == 0 {
				return ai.ToolTextf("no files matching %q under %s", needle, root), nil
			}
			sort.Strings(hits)
			out := strings.Join(hits, "\n")
			if truncated {
				out += fmt.Sprintf("\n\n[stopped at %d matches — narrow the name]", findLimit)
			}
			return ai.ToolText(out), nil
		})
}

// matchName accepts a glob when the needle looks like one, and a
// case-insensitive substring otherwise — which is what people mean when they
// type a bare word.
func matchName(name, needle string) bool {
	if strings.ContainsAny(needle, "*?[") {
		ok, err := filepath.Match(needle, name)
		return err == nil && ok
	}
	return strings.Contains(strings.ToLower(name), strings.ToLower(needle))
}

// skipDir is the set never worth walking. Searching a node_modules is how a
// find takes thirty seconds and returns nothing anyone wanted.
func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".venv", "venv", "__pycache__",
		"dist", "build", "target", ".next", ".cache", ".idea", ".pi-agent":
		return true
	}
	return false
}
