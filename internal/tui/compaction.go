package tui

import "strconv"

// The [compaction] box (loop's CompactionSummaryMessageComponent): where a
// compaction happened, in the conversation itself. A filled panel — one
// column and one row of padding on the custom-message surface — with a bold
// label and, collapsed, how big the context was; expanded, the summary (or a
// rollover's recovery record). It is a transcript entry like a thought or a
// call: nav selects it, → or Enter opens it, and the hint shows only while
// it is selected, the only time those keys do anything.

// CompactionState is what one box shows.
type CompactionState struct {
	Summary      string
	Handoff      string // a rollover's recovery record, in place of a summary
	TokensBefore int
	// Focus is what the user asked the summary to keep (/compact <text>).
	Focus    string
	Expanded bool
	Selected bool
	Nav      bool
}

// Compaction is one compaction as the transcript shows it.
type Compaction struct {
	Summary, Handoff, Focus string
	TokensBefore            int
}

// AddCompaction records a compaction in the transcript.
func (a *App) AddCompaction(c Compaction) {
	md := NewMarkdown(a.mdTheme, &DefaultTextStyle{Color: func(s string) string { return a.theme.Fg(SlotCustomMessageText, s) }})
	a.push(&entry{kind: entCompaction, text: c.Summary, handoff: c.Handoff, focus: c.Focus, tokensBefore: c.TokensBefore, md: md})
}

// RenderCompaction draws the box at width.
func RenderCompaction(t *Theme, md *Markdown, st CompactionState, width int) []string {
	label, lead, body := "[compaction]", "Compacted from "+thousands(st.TokensBefore)+" tokens", st.Summary
	if st.Handoff != "" {
		// A rollover has no summary at all — calling it a compaction and
		// drawing an empty summary would read as a lost window.
		label, lead, body = "[fresh context]", "Fresh window from "+thousands(st.TokensBefore)+" tokens", st.Handoff
	}
	inner := max(10, width-2*userPadX)
	rows := []string{t.Fg(SlotCustomMessageLabel, t.Bold(label)), ""}
	var instructions []string
	if st.Focus != "" {
		instructions = wrapPreserveBlanks("Instructions: "+st.Focus, inner)
		for i, l := range instructions {
			instructions[i] = t.Fg(SlotDim, l)
		}
	}
	if st.Expanded {
		rows = append(rows, t.Fg(SlotCustomMessageText, t.Bold(lead)))
		rows = append(rows, instructions...)
		md.SetText(body)
		rows = append(append(rows, ""), trimTrailingBlank(md.Render(inner))...)
	} else {
		hint := ""
		if st.Selected {
			hint = t.Fg(SlotDim, " ("+expandHintFor(st.Nav)+" to expand)")
		}
		rows = append(rows, t.Fg(SlotCustomMessageText, lead)+hint)
		rows = append(rows, instructions...)
	}
	bg := func(line string) string { return t.Bg(SlotCustomMessageBg, line) }
	blank := applyBackgroundToLine("", width, bg)
	out := []string{"", blank}
	for _, r := range rows {
		out = append(out, applyBackgroundToLine(fitRow(pad(userPadX)+r, width), width, bg))
	}
	return append(out, blank)
}

func trimTrailingBlank(lines []string) []string {
	for len(lines) > 0 && stripANSI(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// thousands is n with thousands separators (JS toLocaleString in en-US).
func thousands(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + thousands(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
