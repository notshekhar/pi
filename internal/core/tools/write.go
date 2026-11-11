package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
)

// maxWriteDiffLines caps the diff a write reports to the UI. An overwrite can
// rewrite a whole file, so unlike edit's this diff has no natural ceiling.
const maxWriteDiffLines = 200

func capDiff(diff string) string {
	lines := strings.Split(diff, "\n")
	if len(lines) <= maxWriteDiffLines {
		return diff
	}
	return fmt.Sprintf("%s\n… %d more diff lines", strings.Join(lines[:maxWriteDiffLines], "\n"), len(lines)-maxWriteDiffLines)
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Write returns the write tool.
func Write(t *Context) ai.Tool {
	return LoopTool("write", func(ctx context.Context, a writeArgs) (ai.ToolResult, error) {
		if aborted(ctx) {
			return fail(errAborted)
		}
		if t.planMode() {
			return fail(errors.New(planModeRefusal))
		}
		path := Resolve(a.Path, t.CWD)
		output, err := withFileLock(path, func() (string, error) { return writeFile(t, path, a) })
		if err != nil {
			return fail(err)
		}
		return mutationResult(output), nil
	})
}

func writeFile(t *Context, path string, a writeArgs) (string, error) {
	// Overwriting an existing file needs all of it read this session; a new
	// file passes freely.
	previous, existed := "", exists(path)
	if existed {
		if msg := t.Registry.CheckWrite(path, a.Path); msg != "" {
			return "", fmt.Errorf("%s (write would overwrite the existing file)", msg)
		}
		// Captured before the write, the only moment it exists. A file that
		// cannot be read simply reports no diff.
		raw, err := os.ReadFile(path)
		existed = err == nil
		previous = string(raw)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(a.Content), 0o644); err != nil {
		return "", err
	}
	t.Registry.RecordModified(path)
	summary := fmt.Sprintf("Successfully wrote %d bytes to %s", len(a.Content), a.Path)
	switch {
	case !existed:
		// Nothing to diff against: the UI renders a new file from the call's input.
		return summary, nil
	case previous == a.Content:
		return summary + " (content unchanged)", nil
	}
	return summary + diffSeparator + capDiff(generateDiff(previous, a.Content)), nil
}
