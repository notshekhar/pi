package tools

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai"
)

type grepArgs struct {
	Pattern    string `json:"pattern" jsonschema:"description=Search pattern (regex, or literal if literal=true)"`
	Path       string `json:"path,omitempty" jsonschema:"description=Directory or file to search"`
	Glob       string `json:"glob,omitempty" jsonschema:"description=Filter files by glob, e.g. '*.go'"`
	IgnoreCase bool   `json:"ignoreCase,omitempty" jsonschema:"description=Case-insensitive search"`
	Literal    bool   `json:"literal,omitempty" jsonschema:"description=Treat pattern as a literal string"`
	Limit      *int   `json:"limit,omitempty" jsonschema:"description=Maximum matches (default 100)"`
}

// Grep returns the grep tool. Uses rg when installed, otherwise walks in Go.
func Grep(t *Context) ai.Tool {
	return ai.NewTool("grep",
		"Search file contents for a pattern. Returns path:line:text. Respects .gitignore when rg is available. Default 100 matches.",
		func(ctx context.Context, a grepArgs) (ai.ToolResult, error) {
			if aborted(ctx) {
				return ai.ToolError("aborted"), nil
			}
			if a.Pattern == "" {
				return ai.ToolError("pattern is required"), nil
			}
			root := Resolve(a.Path, t.CWD)
			limit := 100
			if a.Limit != nil && *a.Limit > 0 {
				limit = *a.Limit
			}
			if rg, err := exec.LookPath("rg"); err == nil {
				return grepRG(ctx, rg, root, a, limit)
			}
			return grepWalk(ctx, root, a, limit)
		})
}

func grepRG(ctx context.Context, rg, root string, a grepArgs, limit int) (ai.ToolResult, error) {
	args := []string{"--line-number", "--no-heading", "--color=never", "--hidden", "--max-count", "1"}
	if a.IgnoreCase {
		args = append(args, "-i")
	}
	if a.Literal {
		args = append(args, "-F")
	}
	if a.Glob != "" {
		args = append(args, "--glob", a.Glob)
	}
	args = append(args, "-m", itoa(limit), "--", a.Pattern, root)
	cmd := exec.CommandContext(ctx, rg, args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return ai.ToolText("No matches."), nil
		}
		return ai.ToolErrorf("rg: %v", err), nil
	}
	text := strings.TrimRight(string(out), "\n")
	if text == "" {
		return ai.ToolText("No matches."), nil
	}
	return ai.ToolText(text), nil
}

func grepWalk(ctx context.Context, root string, a grepArgs, limit int) (ai.ToolResult, error) {
	pat := a.Pattern
	if a.Literal {
		pat = regexp.QuoteMeta(pat)
	}
	if a.IgnoreCase {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return ai.ToolErrorf("invalid pattern: %v", err), nil
	}

	info, err := os.Stat(root)
	if err != nil {
		return ai.ToolErrorf("path not found: %s", a.Path), nil
	}

	var hits []string
	add := func(path string, lineNo int, line string) {
		if len(line) > 200 {
			line = line[:200] + "…"
		}
		rel := path
		if r, err := filepath.Rel(root, path); err == nil {
			rel = r
		}
		hits = append(hits, rel+":"+itoa(lineNo)+":"+line)
	}

	scanFile := func(path string) {
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		scan := bufio.NewScanner(f)
		scan.Buffer(nil, 1<<20)
		n := 0
		for scan.Scan() {
			n++
			if re.Match(scan.Bytes()) {
				add(path, n, scan.Text())
				if len(hits) >= limit {
					return
				}
			}
		}
	}

	if !info.IsDir() {
		scanFile(root)
	} else {
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
			if a.Glob != "" {
				ok, _ := filepath.Match(a.Glob, d.Name())
				if !ok {
					return nil
				}
			}
			if !utf8SafeFile(path) {
				return nil
			}
			scanFile(path)
			return nil
		})
	}
	if len(hits) == 0 {
		return ai.ToolText("No matches."), nil
	}
	return ai.ToolText(strings.Join(hits, "\n")), nil
}
