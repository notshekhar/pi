package tui

import (
	"strings"
)

// The approval prompt: a tool call the policy will not run unasked.
//
// Deliberately not the searchable picker. An approval is a decision about a
// specific, already-chosen thing, and the answer is yes or no — a filter box
// would invite typing where the only safe interactions are "read this" and
// "choose". The call's full subject is shown, wrapped, never truncated: a
// command you approve on the strength of its first forty characters is a
// command you did not read.

// Confirm is a pending approval.
type Confirm struct {
	Theme *Theme
	// Tool and Subject describe the call being approved.
	Tool    string
	Subject string
	// Reason is why the policy escalated it.
	Reason string
	// Approved is the default answer — deliberately "no", so a stray Enter
	// declines rather than approves.
	Approved bool
	Result   chan bool
}

// Handle routes one key; it returns true when the prompt closed.
func (c *Confirm) Handle(k Key) (done bool) {
	switch k.Kind {
	case KeyLeft, KeyRight, KeyTab, KeyUp, KeyDown:
		c.Approved = !c.Approved
	case KeyEnter:
		c.Result <- c.Approved
		return true
	case KeyEsc, KeyCtrlC:
		c.Result <- false
		return true
	case KeyRune:
		switch k.Rune {
		case 'y', 'Y':
			c.Result <- true
			return true
		case 'n', 'N':
			c.Result <- false
			return true
		}
	}
	return false
}

// View renders the prompt.
func (c *Confirm) View(width int) []string {
	t := c.Theme
	if t == nil {
		t = NewTheme(NightPalette)
	}
	inner := max(20, width-4)

	lines := []string{
		t.Fg(SlotWarning, t.Bold(" "+bulletGlyph+" approve this call?")),
	}
	if c.Reason != "" {
		lines = append(lines, " "+t.Fg(SlotDim, c.Reason))
	}
	lines = append(lines, "")

	// The whole subject, wrapped. Never elided — see the note above.
	head := " " + t.Fg(SlotText, t.Bold(c.Tool)) + " "
	body := wrapTextWithAnsi(c.Subject, inner-visibleWidth(head))
	for i, l := range body {
		if i == 0 {
			lines = append(lines, head+t.Fg(SlotMuted, l))
			continue
		}
		lines = append(lines, strings.Repeat(" ", visibleWidth(head))+t.Fg(SlotMuted, l))
	}
	lines = append(lines, "")

	yes, no := "  yes  ", "  no  "
	if c.Approved {
		lines = append(lines, " "+t.Bg(SlotSuccess, t.Fg(SlotBgBase, yes))+"  "+t.Fg(SlotMuted, no))
	} else {
		lines = append(lines, " "+t.Fg(SlotMuted, yes)+"  "+t.Bg(SlotError, t.Fg(SlotBgBase, no)))
	}
	lines = append(lines, t.Fg(SlotDim, " y/n · ←→ then enter · esc declines"))

	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = fitRow(l, width)
	}
	return out
}
