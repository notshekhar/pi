package tools

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/jsonopt"
)

const (
	defaultGrepLimit = 100
	maxGrepContext   = 20
)

type grepArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	IgnoreCase bool   `json:"ignoreCase,omitzero"`
	Literal    bool   `json:"literal,omitzero"`
	Context    int    `json:"context,omitzero"`
	Limit      int    `json:"limit,omitzero"`
}

// grepMatch is one matching line.
type grepMatch struct {
	file string
	line int
	// text is the line as the searcher saw it. hasText is false when ripgrep
	// left it out (a non-UTF-8 line), and the line is read back from the file.
	text    string
	hasText bool
}

// Grep returns the grep tool.
func Grep(t *Context) ai.Tool {
	return LoopTool("grep", func(ctx context.Context, a grepArgs) (ai.ToolResult, error) {
		if aborted(ctx) {
			return fail(errAborted)
		}
		root := Resolve(a.Path, t.CWD)
		info, err := os.Stat(root)
		if err != nil {
			return fail(fmt.Errorf("Path not found: %s", root))
		}
		limit := a.Limit
		if limit <= 0 {
			limit = defaultGrepLimit
		}
		matches, err := grepMatches(ctx, root, a, limit)
		if err != nil {
			return fail(err)
		}
		if len(matches) == 0 {
			return ai.ToolText("No matches found"), nil
		}
		report := grepReport{root: root, rootIsDir: info.IsDir(), context: min(max(a.Context, 0), maxGrepContext), files: map[string][]string{}}
		return ai.ToolText(report.render(matches, limit)), nil
	})
}

func grepMatches(ctx context.Context, root string, a grepArgs, limit int) ([]grepMatch, error) {
	if rg, ok := searchBinary("rg"); ok {
		return grepWithRipgrep(ctx, rg, root, a, limit)
	}
	return grepByWalking(ctx, root, a, limit)
}

// rgEvent is one line of ripgrep's --json output.
type rgEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		LineNumber int `json:"line_number"`
		Lines      struct {
			Text *string `json:"text"`
		} `json:"lines"`
	} `json:"data"`
}

func grepWithRipgrep(ctx context.Context, rg, root string, a grepArgs, limit int) ([]grepMatch, error) {
	args := []string{"--json", "--line-number", "--color=never", "--hidden"}
	if a.IgnoreCase {
		args = append(args, "--ignore-case")
	}
	if a.Literal {
		args = append(args, "--fixed-strings")
	}
	if a.Glob != "" {
		args = append(args, "--glob", a.Glob)
	}
	// Its own context, so reaching the limit can stop ripgrep without
	// reading as the turn being cancelled.
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(runCtx, rg, append(args, "--", a.Pattern, root)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("Failed to run ripgrep: %v", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("Failed to run ripgrep: %v", err)
	}
	matches := readRipgrepMatches(stdout, limit)
	stoppedAtLimit := len(matches) >= limit
	if stoppedAtLimit {
		stop()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, errAborted
	}
	// Exit 1 is ripgrep's "no matches"; anything else that is not our own stop is a failure.
	if code := cmd.ProcessState.ExitCode(); waitErr != nil && !stoppedAtLimit && code != 1 {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, fmt.Errorf("ripgrep exited with code %d", code)
	}
	return matches, nil
}

func readRipgrepMatches(stdout interface{ Read([]byte) (int, error) }, limit int) []grepMatch {
	var matches []grepMatch
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(nil, 16<<20)
	for len(matches) < limit && scanner.Scan() {
		var event rgEvent
		if json.Unmarshal(scanner.Bytes(), &event, jsonopt.Read) != nil || event.Type != "match" || event.Data.Path.Text == "" {
			continue
		}
		match := grepMatch{file: event.Data.Path.Text, line: event.Data.LineNumber}
		if event.Data.Lines.Text != nil {
			match.text, match.hasText = *event.Data.Lines.Text, true
		}
		matches = append(matches, match)
	}
	return matches
}

func grepByWalking(ctx context.Context, root string, a grepArgs, limit int) ([]grepMatch, error) {
	pattern := a.Pattern
	if a.Literal {
		pattern = regexp.QuoteMeta(pattern)
	}
	if a.IgnoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("regex parse error: %v", err)
	}
	var matches []grepMatch
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil || len(matches) >= limit {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if path != root && skipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if a.Glob != "" && !matchesGrepGlob(a.Glob, root, path) {
			return nil
		}
		matches = append(matches, matchesInFile(path, re, limit-len(matches))...)
		return nil
	})
	if ctx.Err() != nil {
		return nil, errAborted
	}
	return matches, nil
}

func matchesGrepGlob(glob, root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	return matchGlob(glob, filepath.ToSlash(rel))
}

func matchesInFile(path string, re *regexp.Regexp, room int) []grepMatch {
	if binary, err := looksBinary(path); err != nil || binary {
		return nil
	}
	var matches []grepMatch
	for i, line := range fileLines(path) {
		if len(matches) >= room {
			break
		}
		if re.MatchString(line) {
			matches = append(matches, grepMatch{file: path, line: i + 1, text: line, hasText: true})
		}
	}
	return matches
}

// fileLines is a file's lines with the line endings removed. A final newline
// terminates the last line; it does not start an empty one.
func fileLines(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	text := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// grepReport renders matches as "path:line: text", with context lines as
// "path-line- text" the way ripgrep prints them.
type grepReport struct {
	root           string
	rootIsDir      bool
	context        int
	files          map[string][]string
	linesTruncated bool
}

func (r *grepReport) displayPath(file string) string {
	if r.rootIsDir {
		if rel, err := filepath.Rel(r.root, file); err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(file)
}

func (r *grepReport) linesOf(file string) []string {
	lines, cached := r.files[file]
	if !cached {
		lines = fileLines(file)
		r.files[file] = lines
	}
	return lines
}

func (r *grepReport) clip(line string) string {
	text, truncated := TruncateLine(strings.ReplaceAll(line, "\r", ""), GrepMaxLineLength)
	r.linesTruncated = r.linesTruncated || truncated
	return text
}

func (r *grepReport) render(matches []grepMatch, limit int) string {
	var lines []string
	if r.context > 0 {
		lines = r.withContext(matches)
	} else {
		for _, m := range matches {
			lines = append(lines, r.matchLine(m))
		}
	}
	cut := TruncateHead(strings.Join(lines, "\n"), allLines, DefaultMaxBytes)
	var notices []string
	if len(matches) >= limit {
		notices = append(notices, fmt.Sprintf("%d matches limit reached. Use limit=%d for more, or refine pattern", limit, limit*2))
	}
	if cut.Truncated {
		notices = append(notices, FormatSize(DefaultMaxBytes)+" limit reached")
	}
	if r.linesTruncated {
		notices = append(notices, fmt.Sprintf("Some lines truncated to %d chars. Use read tool to see full lines", GrepMaxLineLength))
	}
	return withNotices(cut.Content, notices)
}

func (r *grepReport) matchLine(m grepMatch) string {
	rel := r.displayPath(m.file)
	if m.hasText {
		return fmt.Sprintf("%s:%d: %s", rel, m.line, r.clip(strings.TrimSuffix(strings.ReplaceAll(m.text, "\r\n", "\n"), "\n")))
	}
	lines := r.linesOf(m.file)
	if m.line < 1 || m.line > len(lines) {
		return fmt.Sprintf("%s:%d: (unable to read file)", rel, m.line)
	}
	return fmt.Sprintf("%s:%d: %s", rel, m.line, r.clip(lines[m.line-1]))
}

// withContext prints each file's matches with their surrounding lines. The
// windows around nearby matches are merged into one run, and runs are
// separated by "--": printing each match's window on its own would repeat
// the shared lines, and show a match caught in a neighbour's window as context.
func (r *grepReport) withContext(matches []grepMatch) []string {
	var order []string
	byFile := map[string][]int{}
	for _, m := range matches {
		if _, seen := byFile[m.file]; !seen {
			order = append(order, m.file)
		}
		byFile[m.file] = append(byFile[m.file], m.line)
	}
	var out []string
	for _, file := range order {
		out = r.appendFileContext(out, file, byFile[file])
	}
	return out
}

func (r *grepReport) appendFileContext(out []string, file string, matchLines []int) []string {
	rel := r.displayPath(file)
	lines := r.linesOf(file)
	if len(lines) == 0 {
		for _, n := range matchLines {
			out = append(out, fmt.Sprintf("%s:%d: (unable to read file)", rel, n))
		}
		return out
	}
	isMatch := map[int]bool{}
	for _, n := range matchLines {
		isMatch[n] = true
	}
	for _, window := range mergedWindows(matchLines, r.context, len(lines)) {
		if len(out) > 0 {
			out = append(out, "--")
		}
		for n := window[0]; n <= window[1]; n++ {
			if isMatch[n] {
				out = append(out, fmt.Sprintf("%s:%d: %s", rel, n, r.clip(lines[n-1])))
			} else {
				out = append(out, fmt.Sprintf("%s-%d- %s", rel, n, r.clip(lines[n-1])))
			}
		}
	}
	return out
}

// mergedWindows is the line ranges to print: each match widened by the
// context, with ranges that overlap or sit back to back joined into one.
func mergedWindows(matchLines []int, context, lineCount int) [][2]int {
	sorted := append([]int(nil), matchLines...)
	sort.Ints(sorted)
	var windows [][2]int
	for _, n := range sorted {
		start, end := max(1, n-context), min(lineCount, n+context)
		if last := len(windows) - 1; last >= 0 && start <= windows[last][1]+1 {
			windows[last][1] = max(windows[last][1], end)
			continue
		}
		windows = append(windows, [2]int{start, end})
	}
	return windows
}
