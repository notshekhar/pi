package tui

import (
	"strings"
	"time"
)

// Dev entry points for eyeballing the renderers outside a live session.
// Not reachable from the agent itself — see cmd/mddemo.

// DemoRender renders markdown with the night theme.
func DemoRender(src string, width int) string {
	theme := NewTheme(NightPalette)
	m := NewMarkdown(MarkdownThemeFor(theme), nil)
	m.SetText(src)
	return strings.Join(m.Render(width), "\n") + "\n"
}

// DemoTranscript renders one of every block type, so the row grammar can be
// checked at a glance: prompt, thinking, prose, and tool rows in each state.
func DemoTranscript(p Palette, width int, tick int64) string {
	t := NewTheme(p)
	var out []string
	add := func(lines []string) { out = append(out, lines...) }

	add(RenderUser(t, "add a rail to the transcript and show me how it looks", time.Now(), width))

	add(RenderThinking(t, ThinkingState{
		Text:     "The rail is one column plus two of padding.\nHeader and body share it so a block reads as one object.",
		Duration: 4200 * time.Millisecond,
	}, width, tick))

	md := NewMarkdown(MarkdownThemeFor(t), nil)
	md.SetText("Here's the plan. The rail carries state as **motion** rather than more text:\n\n" +
		"- running waves\n- blocked pulses\n- settled holds its outcome colour\n\n" +
		"```go\nfunc RailFor(state BlockState) *RailSpec {\n\treturn &RailSpec{Motion: MotionWave}\n}\n```\n")
	add(assistantBlock(t, md.Render(max(1, width-ContentIndent)), time.Now(), width))

	add(RenderTool(t, ToolState{
		Name: "read", Args: map[string]any{"path": "rail.go", "offset": 120, "limit": 61},
		Output: "package tui\n\nconst RailWidth = 3", GroupLead: true,
	}, width, tick))

	add(RenderTool(t, ToolState{
		Name: "edit", Args: map[string]any{"path": "noir.go"},
		Output: "-old line\n+new line\n context", Expanded: true,
	}, width, tick))

	add(RenderTool(t, ToolState{
		Name: "bash", Args: map[string]any{"command": "go test ./..."},
		Output: "FAIL\texit status 1", IsError: true, Selected: true,
	}, width, tick))

	add(RenderTool(t, ToolState{
		Name: "write", Args: map[string]any{"path": "demo.go"}, IsPartial: true,
		StreamingContent: "one\ntwo\nthree\nfour\nfive",
	}, width, tick))

	// A run of finished calls folds to one row — the second fold level, above
	// each call's own expand.
	add(renderGroupHeader(t, groupOf("read", "read", "read", "skill"), false, true, width))
	add(renderGroupHeader(t, groupOf("bash", "bash", "grep"), true, true, width))

	add(RenderTurnSummary(t, 12400*time.Millisecond, width))

	// The pinned plan sits above the editor, so it never scrolls away.
	add(renderTodos(t, []TodoItem{
		{Content: "port the rail from noir-mode.ts", Status: TodoCompleted},
		{Content: "wire grouping into nav", Status: TodoCompleted},
		{Content: "drop the geometric glyphs", Status: TodoCancelled},
		{Content: "add the todo tool and its panel", ActiveForm: "adding the todo tool and its panel",
			Status: TodoInProgress},
		{Content: "update PARITY.md", Status: TodoPending},
	}, width))
	return strings.Join(out, "\n") + "\n"
}
