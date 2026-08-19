package tui

import (
	"strings"
	"testing"
	"time"
)

func testTheme() *Theme {
	t := NewTheme(NightPalette)
	t.TrueColor = true
	return t
}

// plain strips styling so a test asserts on structure, not escapes.
func plain(lines []string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(stripANSI(l), " ")
	}
	return strings.Join(out, "\n")
}

func TestFmtDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0.0s"},
		{500 * time.Millisecond, "0.5s"},
		{9900 * time.Millisecond, "9.9s"},
		{41 * time.Second, "41s"},
		{59 * time.Second, "59s"},
		// The trap loop documented: branching on the RAW value prints "60s"
		// here, because minutes floor while seconds round up past the edge.
		{119700 * time.Millisecond, "2m00s"},
		{83 * time.Second, "1m23s"},
		{-1 * time.Second, "0.0s"},
	}
	for _, c := range cases {
		if got := fmtDuration(c.d); got != c.want {
			t.Errorf("fmtDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// Every row must fit: an overflowing line wraps and pushes the whole grid
// down, desynchronising every absolute cursor move after it.
func TestRowsNeverExceedWidth(t *testing.T) {
	th := testTheme()
	long := strings.Repeat("git log --oneline --graph ", 20)
	states := []ToolState{
		{Name: "bash", Args: map[string]any{"command": long}, IsPartial: true},
		{Name: "read", Args: map[string]any{"path": "/very/long/" + long}, Output: "x"},
		{Name: "edit", Args: map[string]any{"path": "a.go"}, IsError: true, Output: "boom"},
	}
	for _, width := range []int{20, 40, 80, 120} {
		for _, st := range states {
			for _, line := range RenderTool(th, st, width, 0) {
				if w := visibleWidth(line); w > width {
					t.Errorf("width %d: %d-cell row from %s: %q", width, w, st.Name, line)
				}
			}
		}
		for _, line := range RenderThinking(th, ThinkingState{Text: long, Streaming: true}, width, 0) {
			if w := visibleWidth(line); w > width {
				t.Errorf("width %d: %d-cell thinking line: %q", width, w, line)
			}
		}
		for _, line := range RenderUser(th, long, time.Now(), width) {
			if w := visibleWidth(line); w > width {
				t.Errorf("width %d: %d-cell user line: %q", width, w, line)
			}
		}
	}
}

func TestToolRowShowsNameAndSummary(t *testing.T) {
	got := plain(RenderTool(testTheme(), ToolState{
		Name: "read",
		Args: map[string]any{"path": "/repo/src/app.ts"},
		CWD:  "/repo",
	}, 60, 0))
	if !strings.Contains(got, "◆ read src/app.ts") {
		t.Errorf("row = %q", got)
	}
}

func TestToolRowShowsReadRange(t *testing.T) {
	got := plain(RenderTool(testTheme(), ToolState{
		Name: "read",
		Args: map[string]any{"path": "/repo/a.go", "offset": 120, "limit": 61},
		CWD:  "/repo",
	}, 60, 0))
	if !strings.Contains(got, "a.go:120-180") {
		t.Errorf("range missing: %q", got)
	}
}

// Folding is the whole point of the row form: output stays hidden until the
// row is opened.
func TestToolOutputHiddenUntilExpanded(t *testing.T) {
	st := ToolState{Name: "bash", Args: map[string]any{"command": "ls"}, Output: "SECRETLINE"}

	folded := plain(RenderTool(testTheme(), st, 60, 0))
	if strings.Contains(folded, "SECRETLINE") {
		t.Errorf("folded row leaked its output:\n%s", folded)
	}

	st.Expanded = true
	open := plain(RenderTool(testTheme(), st, 60, 0))
	if !strings.Contains(open, "SECRETLINE") {
		t.Errorf("expanded row hid its output:\n%s", open)
	}
}

// A failure folds too — the red diamond carries the signal, and the error is
// one keystroke away.
func TestFailedToolFoldsButIsMarked(t *testing.T) {
	st := ToolState{Name: "bash", Args: map[string]any{"command": "false"}, Output: "exit 1", IsError: true}
	lines := RenderTool(testTheme(), st, 60, 0)
	if strings.Contains(plain(lines), "exit 1") {
		t.Error("failed row should still fold its output")
	}
	errHex := testTheme().Hex(SlotToolError)
	if !strings.Contains(strings.Join(lines, "\n"), hexToSGR(errHex)) {
		t.Error("failed row is not painted in the error colour")
	}
}

func TestSelectedRowOffersExpandHint(t *testing.T) {
	got := plain(RenderTool(testTheme(), ToolState{
		Name: "ls", Args: map[string]any{"path": "."}, Output: "a\nb", Selected: true,
	}, 60, 0))
	if !strings.Contains(got, "to expand") {
		t.Errorf("no expand hint on a selected row: %q", got)
	}
}

func TestReadOutputGetsLineNumbers(t *testing.T) {
	got := plain(RenderTool(testTheme(), ToolState{
		Name:     "read",
		Args:     map[string]any{"path": "a.go", "offset": 10},
		Output:   "first\nsecond",
		Expanded: true,
	}, 60, 0))
	if !strings.Contains(got, "10  first") || !strings.Contains(got, "11  second") {
		t.Errorf("gutter numbering wrong:\n%s", got)
	}
}

func TestNonReadOutputGetsNoLineNumbers(t *testing.T) {
	got := plain(RenderTool(testTheme(), ToolState{
		Name: "bash", Args: map[string]any{"command": "ls"}, Output: "a\nb", Expanded: true,
	}, 60, 0))
	if strings.Contains(got, "1  a") {
		t.Errorf("bash output should not be numbered:\n%s", got)
	}
}

func TestEditOutputIsDiffColoured(t *testing.T) {
	th := testTheme()
	lines := RenderTool(th, ToolState{
		Name: "edit", Args: map[string]any{"path": "a.go"},
		Output: "-gone\n+added\n context", Expanded: true,
	}, 60, 0)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, hexToSGR(th.Hex(SlotToolDiffAdded))) {
		t.Error("added line is not green")
	}
	if !strings.Contains(joined, hexToSGR(th.Hex(SlotToolDiffRemoved))) {
		t.Error("removed line is not red")
	}
}

func TestStreamingToolShowsInputTail(t *testing.T) {
	got := plain(RenderTool(testTheme(), ToolState{
		Name: "write", Args: map[string]any{"path": "a.go"}, IsPartial: true,
		StreamingContent: "one\ntwo\nthree\nfour\nfive",
	}, 60, 0))
	if strings.Contains(got, "one") || strings.Contains(got, "two") {
		t.Errorf("tail should show only the last %d lines:\n%s", LiveTailLines, got)
	}
	if !strings.Contains(got, "five") {
		t.Errorf("tail missing the newest line:\n%s", got)
	}
}

func TestThinkingFoldsToOneRowWhenSettled(t *testing.T) {
	lines := RenderThinking(testTheme(), ThinkingState{
		Text: "a\nb\nc\nd\ne", Duration: 4200 * time.Millisecond,
	}, 60, 0)
	got := plain(lines)
	if !strings.Contains(got, "Thought for 4.2s") {
		t.Errorf("header = %q", got)
	}
	if strings.Contains(got, "\nc") {
		t.Errorf("settled thinking should fold its body:\n%s", got)
	}
	// One leading blank (the block's own gap) plus the header.
	if len(lines) != 2 {
		t.Errorf("expected gap + header, got %d lines: %q", len(lines), got)
	}
}

func TestThinkingStreamsATail(t *testing.T) {
	got := plain(RenderThinking(testTheme(), ThinkingState{
		Text: "one\ntwo\nthree\nfour\nfive", Streaming: true,
	}, 60, 0))
	if !strings.Contains(got, "Thinking…") {
		t.Errorf("no streaming header: %q", got)
	}
	if strings.Contains(got, "one") {
		t.Errorf("tail should drop old lines:\n%s", got)
	}
	if !strings.Contains(got, "five") {
		t.Errorf("tail missing newest line:\n%s", got)
	}
}

func TestThinkingExpandsFully(t *testing.T) {
	got := plain(RenderThinking(testTheme(), ThinkingState{
		Text: "one\ntwo\nthree\nfour\nfive", Expanded: true, Duration: time.Second,
	}, 60, 0))
	for _, want := range []string{"one", "five"} {
		if !strings.Contains(got, want) {
			t.Errorf("expanded thinking missing %q:\n%s", want, got)
		}
	}
}

func TestUserRowHasPromptGlyphAndTimestamp(t *testing.T) {
	at := time.Date(2026, 8, 17, 14, 35, 0, 0, time.UTC)
	got := plain(RenderUser(testTheme(), "do the thing", at, 60))
	if !strings.Contains(got, "❯ do the thing") {
		t.Errorf("prompt glyph missing: %q", got)
	}
	if !strings.Contains(got, "2:35 PM") {
		t.Errorf("timestamp missing: %q", got)
	}
}

// Unrailed blocks sit one column in, not three: the rail's width belongs to
// blocks that actually draw one.
func TestUnrailedBlocksUseOneColumnOfPadding(t *testing.T) {
	th := testTheme()
	cases := map[string][]string{
		"user":    RenderUser(th, "hello", time.Time{}, 60),
		"summary": RenderTurnSummary(th, time.Second, 60),
		"notice":  RenderNotice(th, []string{"a notice"}, 60),
	}
	for name, lines := range cases {
		for _, line := range lines {
			text := stripANSI(line)
			if strings.TrimSpace(text) == "" {
				continue
			}
			if indent := len(text) - len(strings.TrimLeft(text, " ")); indent != ContentIndent {
				t.Errorf("%s indented %d columns, want %d: %q", name, indent, ContentIndent, text)
			}
		}
	}
}

// A railed block puts its glyph at column 0 and its content at RailWidth.
func TestRailedBlocksKeepTheirLane(t *testing.T) {
	lines := RenderTool(testTheme(), ToolState{Name: "bash", Output: "x"}, 60, 0)
	for _, line := range lines {
		text := stripANSI(line)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if !strings.HasPrefix(text, railGlyph) && !strings.HasPrefix(text, railCollapsed) {
			t.Errorf("railed row does not start with a rail glyph: %q", text)
		}
		if body := strings.TrimLeft(text[len(railGlyph):], " "); text[len(railGlyph):len(railGlyph)+2] != "  " && body != "" {
			t.Errorf("content is not at column %d: %q", RailWidth, text)
		}
	}
}

// Content always starts at the same column, whether or not a rail is drawn —
// otherwise text shifts sideways when a block stops animating.
func TestContentColumnIsStable(t *testing.T) {
	th := testTheme()
	rowWith := plain(RenderTool(th, ToolState{Name: "ls", Args: map[string]any{"path": "."}}, 60, 0))
	rowUser := plain(RenderUser(th, "hi", time.Time{}, 60))
	for _, block := range []string{rowWith, rowUser} {
		for _, line := range strings.Split(block, "\n") {
			if line == "" {
				continue
			}
			if indent := len(line) - len(strings.TrimLeft(line, " ")); indent > RailWidth {
				t.Errorf("content starts at column %d, past the rail: %q", indent, line)
			}
		}
	}
}

func TestRailGlyphs(t *testing.T) {
	th := testTheme()
	running := strings.Join(RenderTool(th, ToolState{Name: "bash", IsPartial: true}, 60, 0), "\n")
	if !strings.Contains(running, railGlyph) {
		t.Error("a running block should carry the heavy rail")
	}
	settled := strings.Join(RenderTool(th, ToolState{Name: "bash", Output: "x"}, 60, 0), "\n")
	if !strings.Contains(settled, railCollapsed) {
		t.Error("a settled folded block should carry the light rail")
	}
}

// The wave has to actually move, or the rail is just a coloured line.
func TestRunningRailAnimates(t *testing.T) {
	th := testTheme()
	st := ToolState{Name: "bash", IsPartial: true, StreamingContent: "a\nb\nc"}
	first := strings.Join(RenderTool(th, st, 60, 0), "\n")
	moved := strings.Join(RenderTool(th, st, 60, 7), "\n")
	if first == moved {
		t.Error("rail did not change between frames")
	}
}

func TestSettledRailDoesNotAnimate(t *testing.T) {
	th := testTheme()
	st := ToolState{Name: "bash", Output: "x", FinishedAt: time.Now().Add(-time.Hour)}
	if a, b := RenderTool(th, st, 60, 0), RenderTool(th, st, 60, 99); strings.Join(a, "") != strings.Join(b, "") {
		t.Error("a settled block must not animate")
	}
}

// --- summaries -------------------------------------------------------------

func TestFormatToolArgs(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"read relative", "read", map[string]any{"path": "/repo/a.go"}, "a.go"},
		{"read outside cwd", "read", map[string]any{"path": "/etc/hosts"}, "/etc/hosts"},
		{"cwd itself", "ls", map[string]any{"path": "/repo"}, "."},
		{"bash first line", "bash", map[string]any{"command": "ls -la\ncd /"}, "ls -la"},
		{"grep with path", "grep", map[string]any{"pattern": "func", "path": "/repo/src"}, "func in src"},
		{"grep bare", "grep", map[string]any{"pattern": "func"}, "func"},
		{"glob", "glob", map[string]any{"pattern": "**/*.go"}, "**/*.go"},
		{"empty", "read", map[string]any{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FormatToolArgs(c.tool, c.args, "/repo"); got != c.want {
				t.Errorf("= %q, want %q", got, c.want)
			}
		})
	}
}

func TestFormatToolArgsClipsLongCommand(t *testing.T) {
	got := FormatToolArgs("bash", map[string]any{"command": strings.Repeat("x", 300)}, "")
	if visibleWidth(got) > 80 {
		t.Errorf("summary is %d cells: %q", visibleWidth(got), got)
	}
}

func TestReadLineRangeText(t *testing.T) {
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, ""},
		{map[string]any{"offset": 120, "limit": 61}, ":120-180"},
		{map[string]any{"offset": 5}, ":5"},
		{map[string]any{"limit": 10}, ":1-10"},
		// JSON decoding yields float64, not int.
		{map[string]any{"offset": float64(3), "limit": float64(2)}, ":3-4"},
	}
	for _, c := range cases {
		if got := ReadLineRangeText(c.args); got != c.want {
			t.Errorf("ReadLineRangeText(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestReadGutterSkipsTrailingNotice(t *testing.T) {
	lines := []string{"one", "two", "", "[12 more lines]"}
	got := ReadGutterPrefixes(lines, map[string]any{"offset": 1})
	if strings.TrimSpace(got[0]) != "1" || strings.TrimSpace(got[1]) != "2" {
		t.Errorf("body not numbered: %q", got)
	}
	if got[2] != "" || got[3] != "" {
		t.Errorf("the notice and its separator must not be numbered: %q", got)
	}
}

func TestReadGutterAlignsToWidestNumber(t *testing.T) {
	lines := make([]string, 12)
	for i := range lines {
		lines[i] = "x"
	}
	got := ReadGutterPrefixes(lines, map[string]any{"offset": 995})
	// 995..1006 — every prefix must share the width of the widest.
	for _, prefix := range got {
		if len(prefix) != len(got[0]) {
			t.Errorf("ragged gutter: %q vs %q", prefix, got[0])
		}
	}
}

func TestStreamingPreviewDecodesPartialJSON(t *testing.T) {
	cases := []struct {
		name string
		tool string
		raw  string
		want string
	}{
		{"partial write", "write", `{"path":"a.go","content":"line one\nline t`, "line one\nline t"},
		{"complete write", "write", `{"content":"done"}`, "done"},
		{"bash command", "bash", `{"command":"echo \"hi\"`, `echo "hi"`},
		{"no key yet", "write", `{"path":"a.`, ""},
		{"tool without a preview", "read", `{"path":"a.go"}`, ""},
		// A half-arrived escape must not print a stray backslash.
		{"dangling escape", "write", `{"content":"a\`, "a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StreamingPreview(c.tool, c.raw); got != c.want {
				t.Errorf("= %q, want %q", got, c.want)
			}
		})
	}
}

// hexToSGR is the foreground escape a hex colour produces, for asserting that
// a line was painted in a particular slot.
func hexToSGR(hex string) string {
	return testTheme().sgr(38, hex)
}
