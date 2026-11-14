package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
)

const defaultFindLimit = 1000

type findArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Limit   int    `json:"limit,omitzero"`
}

// Find returns the find tool: files by glob pattern.
func Find(t *Context) ai.Tool {
	return LoopTool("find", func(ctx context.Context, a findArgs) (ai.ToolResult, error) {
		if aborted(ctx) {
			return fail(errAborted)
		}
		root := Resolve(a.Path, t.CWD)
		if !exists(root) {
			return fail(fmt.Errorf("Path not found: %s", root))
		}
		limit := a.Limit
		if limit <= 0 {
			limit = defaultFindLimit
		}
		paths, err := findPaths(ctx, root, a.Pattern, limit)
		if err != nil {
			return fail(err)
		}
		return ai.ToolText(renderFound(paths, limit)), nil
	})
}

// findPaths returns matches relative to root, with forward slashes.
func findPaths(ctx context.Context, root, pattern string, limit int) ([]string, error) {
	if fd, ok := searchBinary("fd", "fdfind"); ok {
		return findWithFd(ctx, fd, root, pattern, limit)
	}
	return findByWalking(ctx, root, pattern, limit), nil
}

func findWithFd(ctx context.Context, fd, root, pattern string, limit int) ([]string, error) {
	args := []string{"--glob", "--color=never", "--hidden", "--no-require-git", "--max-results", strconv.Itoa(limit)}
	if strings.Contains(pattern, "/") {
		args = append(args, "--full-path")
		if !strings.HasPrefix(pattern, "/") && !strings.HasPrefix(pattern, "**/") && pattern != "**" {
			pattern = "**/" + pattern
		}
	}
	cmd := exec.CommandContext(ctx, fd, append(args, "--", pattern, root)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, errAborted
	}
	if err != nil && len(out) == 0 {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		if _, exited := err.(*exec.ExitError); !exited {
			return nil, fmt.Errorf("Failed to run fd: %v", err)
		}
		return nil, fmt.Errorf("fd exited with code %d", cmd.ProcessState.ExitCode())
	}
	return relativeToRoot(string(out), root), nil
}

// relativeToRoot turns fd's absolute paths into paths under root, keeping the
// trailing slash fd puts on a directory.
func relativeToRoot(output, root string) []string {
	var paths []string
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		rel, err := filepath.Rel(root, line)
		if err != nil {
			rel = line
		}
		if strings.HasSuffix(line, "/") && !strings.HasSuffix(rel, "/") {
			rel += "/"
		}
		paths = append(paths, filepath.ToSlash(rel))
	}
	return paths
}

func findByWalking(ctx context.Context, root, pattern string, limit int) []string {
	var paths []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable directory is skipped, not fatal
		}
		if ctx.Err() != nil || len(paths) >= limit {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if path != root && skipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if rel = filepath.ToSlash(rel); matchGlob(pattern, rel) {
			paths = append(paths, rel)
		}
		return nil
	})
	return paths
}

// matchGlob matches a relative path the way fd does: a pattern with no slash
// is tested against the file name, one with a slash against the whole path,
// and "**" spans any number of directories.
func matchGlob(pattern, rel string) bool {
	if !strings.Contains(pattern, "/") {
		ok, _ := filepath.Match(pattern, filepath.Base(rel))
		return ok
	}
	return matchSegments(strings.Split(strings.TrimPrefix(pattern, "/"), "/"), strings.Split(rel, "/"), true)
}

// matchSegments matches path segments against pattern segments. floating lets
// the pattern start at any depth, which is how fd treats a relative pattern.
func matchSegments(pattern, path []string, floating bool) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	if pattern[0] == "**" {
		for skip := 0; skip <= len(path); skip++ {
			if matchSegments(pattern[1:], path[skip:], false) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	if ok, _ := filepath.Match(pattern[0], path[0]); ok && matchSegments(pattern[1:], path[1:], false) {
		return true
	}
	return floating && matchSegments(pattern, path[1:], true)
}

func renderFound(paths []string, limit int) string {
	if len(paths) == 0 {
		return "No files found matching pattern"
	}
	cut := TruncateHead(strings.Join(paths, "\n"), allLines, DefaultMaxBytes)
	var notices []string
	if len(paths) >= limit {
		notices = append(notices, fmt.Sprintf("%d results limit reached. Use limit=%d for more, or refine pattern", limit, limit*2))
	}
	if cut.Truncated {
		notices = append(notices, FormatSize(DefaultMaxBytes)+" limit reached")
	}
	return withNotices(cut.Content, notices)
}
