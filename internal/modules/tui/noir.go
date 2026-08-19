package tui

import (
	"fmt"
	"strings"
	"time"
)

// Noir — the look. Ported from loop's noir-mode.ts, minus the mode registry:
// there is no other mode to switch to, so this IS the renderer.
//
// A dark-canvas experience in the spirit of terminal cockpits: one-line
// diamond tool rows that grey out when folded, thinking as its own block that
// streams a short tail and collapses when the turn moves on, `❯` user
// prompts, and a turn summary line.

// LiveTailLines is how many lines of a still-streaming block stay visible.
// Enough to show it is moving, few enough that it does not shove the prompt
// off the screen.
const LiveTailLines = 3

// The glyphs. No emoji anywhere — box-drawing and geometric shapes only, so
// every one is a predictable single cell in every font.
const (
	bulletGlyph = "◆"
	promptGlyph = "❯"
)

// The "(… to expand)" hints, which depend on WHO HAS THE KEYBOARD.
//
// In nav mode the transcript has it, so the route to hidden content is just
// the arrow. Outside nav the composer has it, and `→` there moves the text
// cursor — telling the user to press it is telling them to do nothing. The
// hint has to name the way in first.
const (
	navExpandHint    = "→"
	promptExpandHint = "ctrl+e then →"
)

// expandHintFor is the hint to show, given whether nav has the keyboard.
func expandHintFor(nav bool) string {
	if nav {
		return navExpandHint
	}
	return promptExpandHint
}

// fmtDuration renders an elapsed time: `0.5s` under ten seconds, `41s` under
// a minute, `1m23s` beyond.
//
// Branch on the ROUNDED value — branching on the raw one prints "60s" and
// "1m00s" for 119.7s, because the minutes floor the raw value while the
// seconds round up past the boundary.
func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := d.Seconds()
	if tenth := float64(int(secs*10+0.5)) / 10; tenth < 10 {
		return fmt.Sprintf("%.1fs", tenth)
	}
	r := int(secs + 0.5)
	if r < 60 {
		return fmt.Sprintf("%ds", r)
	}
	return fmt.Sprintf("%dm%02ds", r/60, r%60)
}

// railColors resolves the rail palette for a theme.
//
// NOT the thinking/tool accent slots: those resolve to the palette's greys
// (they are body-gutter colours, meant to recede). A LIVE rail has the
// opposite job — it is the one thing on screen that should catch the eye — so
// running rides `warning`, the same yellow the running diamond wears.
func railColors(t *Theme) RailColors {
	return RailColors{
		Running: t.Hex(SlotWarning),
		Success: t.Hex(SlotSuccess),
		Error:   t.Hex(SlotToolError),
		Quiet:   t.Hex(SlotBorder),
	}
}

// ThinkingState is a reasoning block's display state.
type ThinkingState struct {
	Text      string
	Streaming bool
	Expanded  bool
	Selected  bool
	// Nav is whether the transcript has the keyboard, which decides how the
	// expand hint is worded — see expandHintFor.
	Nav bool
	// Duration is the wall-clock thinking time, once known.
	Duration time.Duration
}

// RenderThinking draws reasoning as a foldable one-line row: `◆ Thinking…`
// with a short live tail while it streams, `◆ Thought for 0.5s` once done.
func RenderThinking(t *Theme, st ThinkingState, width int, tick int64) []string {
	bg := t.Hex(SlotBgBase)
	bodyWidth := bodyWidthFor(width)

	label := "Thought"
	switch {
	case st.Duration > 0:
		label = "Thought for " + fmtDuration(st.Duration)
	case st.Streaming:
		label = "Thinking…"
	}

	// Thinking rides its own hue rather than the tool accent — it is the one
	// block whose rail should read as "the model, not the machine". A settled
	// thought keeps that hue too: it has no success/failure outcome to
	// report, so mapping it onto the green/red pair would be a lie.
	think := t.Hex(SlotThinkingPeak)
	colors := railColors(t)
	colors.Running, colors.Success, colors.Error = think, think, think
	spec := RailFor(BlockState{
		IsPartial: st.Streaming,
		Expanded:  st.Expanded,
		Selected:  st.Selected,
	}, colors, bg)

	diamond := t.FgHex(BulletColor(spec, bg, think, tick), bulletGlyph)
	header := fitRow(diamond+" "+t.Fg(SlotMuted, t.Italic(label)), width-RailWidth)

	// Every block owns exactly ONE leading blank, so spacing is the same
	// whether the transcript streamed live or was replayed.
	if !st.Streaming && !st.Expanded {
		if st.Selected && st.Text != "" {
			header = fitRow(header+t.Fg(SlotDim, " ("+expandHintFor(st.Nav)+" to expand)"), width-RailWidth)
		}
		return append([]string{""}, WithRail(t, []string{header}, spec, bg, tick)...)
	}

	wrapped := wrapPreserveBlanks(st.Text, bodyWidth)
	body := wrapped
	if st.Streaming && !st.Expanded && len(wrapped) > LiveTailLines {
		body = wrapped[len(wrapped)-LiveTailLines:]
	}
	lines := []string{header}
	for _, l := range body {
		lines = append(lines, t.Fg(SlotThinkingText, t.Italic(l)))
	}
	return append([]string{""}, WithRail(t, lines, spec, bg, tick)...)
}

// ToolState is a tool call's display state.
type ToolState struct {
	Name string
	Args map[string]any
	// Output is the flattened result text ("" while pending).
	Output      string
	IsError     bool
	IsPartial   bool
	Interrupted bool
	Expanded    bool
	Selected    bool
	// Nav is whether the transcript has the keyboard — see expandHintFor.
	Nav bool
	// GroupLead marks the first call of a consecutive run — a lead blank
	// reads better after text, while rows inside a group stay tight.
	GroupLead  bool
	StatusText string
	// StreamingContent is the tool's input as it arrives (write/edit bodies).
	StreamingContent string
	// rawInput is the tool arguments as they stream in, still invalid JSON.
	rawInput   string
	FinishedAt time.Time
	CWD        string
}

// RenderTool draws a tool call as a flat one-line row: `◆ name summary` — no
// box, no background. Muted once done and folded, theme text when selected,
// red on failure. Expanded rows show the output under the rail.
func RenderTool(t *Theme, st ToolState, width int, tick int64) []string {
	bg := t.Hex(SlotBgBase)
	rowWidth := width - RailWidth
	bodyWidth := max(1, rowWidth-1)

	failed := st.IsError && !st.IsPartial
	spec := RailFor(BlockState{
		IsPartial:   st.IsPartial,
		IsError:     st.IsError,
		Interrupted: st.Interrupted,
		Expanded:    st.Expanded,
		Selected:    st.Selected,
		FinishedAt:  st.FinishedAt,
	}, railColors(t), bg)

	// The diamond carries the state: yellow while running, green on success,
	// red on failure. While running it rides the head of its own rail's wave,
	// so bullet and line pulse as one mark.
	var diamond string
	if st.IsPartial {
		diamond = t.FgHex(BulletColor(spec, bg, t.Hex(SlotWarning), tick), bulletGlyph)
	} else {
		slot := SlotSuccess
		switch {
		case failed:
			slot = SlotToolError
		case st.Interrupted:
			slot = SlotMuted
		}
		diamond = t.Fg(slot, bulletGlyph)
	}

	titleSlot := SlotMuted
	switch {
	case failed:
		titleSlot = SlotToolError
	case st.IsPartial || st.Selected:
		titleSlot = SlotText
	}

	detail := ""
	if summary := FormatToolArgs(st.Name, st.Args, st.CWD); summary != "" {
		detail = " " + t.Fg(SlotMuted, summary)
	}
	// `read src/app.ts:120-180` — the range rides the row dim, the way the
	// default box shows it.
	if st.Name == "read" {
		if r := ReadLineRangeText(st.Args); r != "" {
			detail += t.Fg(SlotDim, r)
		}
	}

	status := ""
	switch {
	case st.IsPartial:
		s := st.StatusText
		if s == "" {
			s = "running"
		}
		status = t.Fg(SlotDim, " · "+s)
	case st.Interrupted:
		status = t.Fg(SlotDim, " · interrupted")
	}

	header := fitRow(diamond+" "+t.Fg(titleSlot, t.Bold(st.Name))+detail+status, rowWidth)
	lines := []string{header}

	// A block's lead gap is its own; rows inside a group stay tight.
	finish := func(content []string) []string {
		railed := WithRail(t, content, spec, bg, tick)
		if st.GroupLead {
			return append([]string{""}, railed...)
		}
		return railed
	}

	// Streaming input — the live tail of a write/edit body as it arrives.
	if st.IsPartial && st.StreamingContent != "" && st.Output == "" {
		tail := strings.Split(st.StreamingContent, "\n")
		if len(tail) > LiveTailLines {
			tail = tail[len(tail)-LiveTailLines:]
		}
		for _, raw := range tail {
			for _, l := range wrapTextWithAnsi(raw, bodyWidth) {
				lines = append(lines, t.Fg(SlotToolOutput, l))
			}
		}
		return finish(lines)
	}

	// Output is hidden while folded — that is the whole point of the row
	// form. Failures fold too: the red diamond carries the signal, and the
	// error is one keystroke away.
	if st.Output == "" || !st.Expanded {
		if st.Output != "" && st.Selected {
			lines[0] = fitRow(lines[0]+t.Fg(SlotDim, " ("+expandHintFor(st.Nav)+" to expand)"), rowWidth)
		}
		return finish(lines)
	}

	lines = append(lines, renderToolOutput(t, st, bodyWidth)...)
	return finish(lines)
}

// renderToolOutput draws an expanded call's result: line-numbered for read,
// diff-coloured for edit and write, plain otherwise.
func renderToolOutput(t *Theme, st ToolState, bodyWidth int) []string {
	raw := strings.Split(st.Output, "\n")

	// `read` bodies carry absolute line numbers; every other tool's output is
	// its own text and gets none.
	var gutters []string
	if st.Name == "read" && !st.IsError {
		gutters = ReadGutterPrefixes(raw, st.Args)
	} else {
		gutters = make([]string, len(raw))
	}

	isDiff := !st.IsError && (st.Name == "edit" || st.Name == "write")
	outSlot := SlotToolOutput
	if st.IsError {
		outSlot = SlotToolError
	}

	var lines []string
	for i, line := range raw {
		num := ""
		if i < len(gutters) {
			num = gutters[i]
		}
		// Only the first visual row of a wrapped source line is numbered;
		// continuations indent to stay under the same column.
		first := true
		for _, l := range wrapTextWithAnsi(line, max(1, bodyWidth-len(num))) {
			prefix := ""
			if num != "" {
				if first {
					prefix = t.Fg(SlotDim, num)
				} else {
					prefix = strings.Repeat(" ", len(num))
				}
			}
			first = false

			slot := outSlot
			if isDiff {
				switch {
				case strings.HasPrefix(l, "+"):
					slot = SlotToolDiffAdded
				case strings.HasPrefix(l, "-"):
					slot = SlotToolDiffRemoved
				default:
					slot = SlotToolDiffContext
				}
			}
			lines = append(lines, prefix+t.Fg(slot, l))
		}
	}
	return lines
}

// RenderUser draws a submitted prompt: `❯ text` with a right-aligned
// timestamp on the first line.
// ContentIndent is the left padding for blocks that carry NO rail — user
// prompts, assistant prose, notices, the turn summary.
//
// One column, not the rail's three. The rail's width belongs to blocks that
// actually draw one; indenting prose to match them pushes every word two
// columns right of where loop puts it, and makes the transcript read as
// though everything hangs off a rail that is not there.
const ContentIndent = 1

func RenderUser(t *Theme, text string, at time.Time, width int) []string {
	// A filled PANEL, not bare text: one column of padding, one ROW of
	// padding above and below, and the raised surface painted across the full
	// width of all three. That band is what separates a prompt from the
	// agent's prose at a glance — without it the two read as one column of
	// text and the eye has to parse the glyph to tell them apart.
	stamp := ""
	if !at.IsZero() {
		stamp = at.Format("3:04 PM")
	}
	bg := func(line string) string { return t.Bg(SlotBgRaised, line) }
	bodyWidth := max(10, width-userPadX*2)

	blank := applyBackgroundToLine("", width, bg)
	lines := []string{"", blank}

	for i, l := range wrapPreserveBlanks(promptGlyph+" "+text, bodyWidth) {
		row := pad(userPadX) + t.Fg(SlotText, l)
		if i == 0 && stamp != "" {
			// The timestamp sits inside the filled row, replacing the tail of
			// its padding, so the band stays unbroken to the edge.
			gap := width - visibleWidth(row) - visibleWidth(stamp) - userPadX
			if gap > 0 {
				row += strings.Repeat(" ", gap) + t.Fg(SlotDim, stamp)
			}
		}
		lines = append(lines, applyBackgroundToLine(fitRow(row, width), width, bg))
	}
	return append(lines, blank)
}

// userPadX is the panel's horizontal padding.
const userPadX = 1

// withStamp right-aligns a timestamp on a row, or leaves the row alone when
// there is no room — a timestamp that pushes the text is worse than none.
func withStamp(t *Theme, row, stamp string, width int) string {
	gap := width - visibleWidth(row) - visibleWidth(stamp) - 1
	if gap < 1 {
		return row
	}
	return row + strings.Repeat(" ", gap) + t.Fg(SlotDim, stamp)
}

func pad(n int) string { return strings.Repeat(" ", n) }

// RenderTurnSummary is the line closing a turn.
//
// Just the duration. The token and cost totals live in the status line, which
// is always on screen — repeating them after every turn is noise that scrolls.
func RenderTurnSummary(t *Theme, d time.Duration, width int) []string {
	row := pad(ContentIndent) + t.Fg(SlotDim, "Turn completed in "+fmtDuration(d)+".")
	return []string{"", fitRow(row, width)}
}

// RenderNotice draws a system line — startup banner, command output, errors.
func RenderNotice(t *Theme, lines []string, width int) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		for _, w := range wrapTextWithAnsi(l, max(1, width-ContentIndent)) {
			out = append(out, pad(ContentIndent)+w)
		}
	}
	return out
}

// wrapPreserveBlanks wraps every line, keeping empty lines as empty lines
// rather than letting the wrapper swallow them.
func wrapPreserveBlanks(text string, width int) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapTextWithAnsi(line, width)...)
	}
	return out
}

// bodyWidthFor is the space a block's body has once the rail has taken its
// column.
//
// The floor is 1, not some comfortable minimum: a floor wider than the
// terminal produces lines that overflow, wrap, and push the whole grid down —
// the exact failure fitRow exists to prevent. A cramped block at width 20 is
// ugly; a desynchronised grid is broken.
func bodyWidthFor(width int) int { return max(1, width-RailWidth-1) }

// markSelected draws the navigation spine down a selected block.
//
// Every line gets the bar, blank separators included, so it reads as one
// connected spine rather than dashes with gaps. The bar REPLACES the line's
// first cell instead of being prepended: a selection that shifted its block
// sideways would make the transcript jump every time the cursor moved.
func markSelected(t *Theme, lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		// Any escapes opening the line are kept in front of the bar, or the
		// bar's own colour would be overwritten by them.
		prefix, rest := splitLeadingANSI(line)
		out[i] = prefix + t.Fg(SlotSelection, selectionBar) + dropFirstCell(rest)
	}
	return out
}

// selectionBar is the spine glyph.
const selectionBar = "▌"

// splitLeadingANSI separates the escape sequences at the start of a line from
// the text that follows.
func splitLeadingANSI(line string) (prefix, rest string) {
	i := 0
	for i < len(line) {
		_, n := extractAnsiCode(line, i)
		if n == 0 {
			break
		}
		i += n
	}
	return line[:i], line[i:]
}

// dropFirstCell removes one visible cell from the front, keeping any escapes
// that precede it — so the bar takes the cell rather than adding one.
func dropFirstCell(s string) string {
	i := 0
	var kept strings.Builder
	for i < len(s) {
		if code, n := extractAnsiCode(s, i); n > 0 {
			kept.WriteString(code)
			i += n
			continue
		}
		// One grapheme is the cell the bar replaces.
		var width int
		forEachGrapheme(s[i:], func(g string) {
			if width == 0 {
				width = len(g)
			}
		})
		if width == 0 {
			width = 1
		}
		return kept.String() + s[i+width:]
	}
	return kept.String()
}
