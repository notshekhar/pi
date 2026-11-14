package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
)

const defaultLsLimit = 500

type lsArgs struct {
	Path  string `json:"path,omitempty"`
	Limit int    `json:"limit,omitzero"`
}

// Ls returns the ls tool.
func Ls(t *Context) ai.Tool {
	return LoopTool("ls", func(ctx context.Context, a lsArgs) (ai.ToolResult, error) {
		if aborted(ctx) {
			return fail(errAborted)
		}
		text, err := listDirectory(Resolve(a.Path, t.CWD), a.Limit)
		if err != nil {
			return fail(err)
		}
		return ai.ToolText(text), nil
	})
}

func listDirectory(dir string, limit int) (string, error) {
	if limit <= 0 {
		limit = defaultLsLimit
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("Path not found: %s", dir)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("Not a directory: %s", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("Cannot read directory: %v", err)
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	limitReached := len(names) > limit
	if limitReached {
		names = names[:limit]
	}
	if len(names) == 0 {
		return "(empty directory)", nil
	}
	for i, name := range names {
		names[i] = name + entrySuffix(filepath.Join(dir, name))
	}
	cut := TruncateHead(strings.Join(names, "\n"), allLines, DefaultMaxBytes)
	var notices []string
	if limitReached {
		notices = append(notices, fmt.Sprintf("%d entries limit reached. Use limit=%d for more", limit, limit*2))
	}
	if cut.Truncated {
		notices = append(notices, FormatSize(DefaultMaxBytes)+" limit reached")
	}
	return withNotices(cut.Content, notices), nil
}

// entrySuffix marks a directory with "/" and a broken symlink with "@". An
// entry that cannot be examined keeps its bare name: a name missing from the
// listing reads as "this file does not exist".
func entrySuffix(path string) string {
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return "/"
		}
		return ""
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "@"
	}
	return ""
}
