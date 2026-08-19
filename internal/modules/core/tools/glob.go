package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai"
)

type globArgs struct {
	Pattern string `json:"pattern" jsonschema:"description=Glob pattern, e.g. '*.go' or '**/*.md'"`
	Path    string `json:"path,omitempty" jsonschema:"description=Directory to search"`
	Limit   *int   `json:"limit,omitempty" jsonschema:"description=Maximum results (default 1000)"`
}

// Glob returns the file-find tool (loop's find).
func Glob(t *Context) ai.Tool {
	return ai.NewTool("glob",
		"Search for files by glob pattern. Returns paths relative to the search directory. Respects .gitignore when fd is available.",
		func(ctx context.Context, a globArgs) (ai.ToolResult, error) {
			if aborted(ctx) {
				return ai.ToolError("aborted"), nil
			}
			if a.Pattern == "" {
				return ai.ToolError("pattern is required"), nil
			}
			root := Resolve(a.Path, t.CWD)
			limit := 1000
			if a.Limit != nil && *a.Limit > 0 {
				limit = *a.Limit
			}
			if fd, err := exec.LookPath("fd"); err == nil {
				return globFD(ctx, fd, root, a.Pattern, limit)
			}
			return globWalk(ctx, root, a.Pattern, limit)
		})
}

func globFD(ctx context.Context, fd, root, pattern string, limit int) (ai.ToolResult, error) {
	args := []string{"--glob", "--color=never", "--hidden", "--no-require-git", "--max-results", itoa(limit)}
	if strings.Contains(pattern, "/") && !strings.HasPrefix(pattern, "**/") && pattern != "**" {
		args = append(args, "--full-path")
		pattern = "**/" + pattern
	}
	args = append(args, "--", pattern, root)
	cmd := exec.CommandContext(ctx, fd, args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return ai.ToolText("No files matched."), nil
		}
		return ai.ToolErrorf("fd: %v", err), nil
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	var rels []string
	for _, line := range lines {
		if line == "" {
			continue
		}
		if r, err := filepath.Rel(root, line); err == nil {
			rels = append(rels, r)
		} else {
			rels = append(rels, line)
		}
	}
	if len(rels) == 0 {
		return ai.ToolText("No files matched."), nil
	}
	return ai.ToolText(strings.Join(rels, "\n")), nil
}

func globWalk(ctx context.Context, root, pattern string, limit int) (ai.ToolResult, error) {
	var hits []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil || len(hits) >= limit {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		if matchGlob(pattern, rel) || matchGlob(pattern, d.Name()) {
			hits = append(hits, rel)
		}
		return nil
	})
	if len(hits) == 0 {
		return ai.ToolText("No files matched."), nil
	}
	return ai.ToolText(strings.Join(hits, "\n")), nil
}

// matchGlob supports * and **. ** matches any number of path segments.
func matchGlob(pattern, name string) bool {
	pattern = filepath.ToSlash(pattern)
	name = filepath.ToSlash(name)
	ok, _ := filepath.Match(pattern, name)
	if ok {
		return true
	}
	if !strings.Contains(pattern, "**") {
		return false
	}
	// Reduce ** to a simple walk: match the last segment against the basename
	// when the pattern is like **/*.go.
	if i := strings.LastIndex(pattern, "/"); i >= 0 {
		ok, _ = filepath.Match(pattern[i+1:], filepath.Base(name))
		return ok
	}
	ok, _ = filepath.Match(pattern, filepath.Base(name))
	return ok
}
