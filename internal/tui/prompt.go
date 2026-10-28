package tui

import "strings"

// The text prompt: the third thing a command can ask for, after "choose one"
// and "yes or no".
//
// Without it a whole class of loop's flows cannot exist — naming a session,
// writing a reminder, typing a permission rule, adding an alias. Those
// commands had to take their argument on the command line instead, which
// means the no-argument form could only print usage.
//
// It is its own editor, not the composer: text typed into a prompt must never
// end up in the draft if the prompt is cancelled, and sharing the composer
// would also share its history and its completion menu, neither of which
// belongs to a one-line question.

// Prompt is a pending question.
type Prompt struct {
	Theme *Theme
	// Label is the question. Rendered above the field.
	Label string
	// Result carries the answer; "" is both an empty answer and a cancel,
	// which is what loop does — every caller treats blank as "never mind".
	Result chan string

	editor *Editor
}

// NewPrompt builds the question, pre-filled with initial for an edit-in-place.
func NewPrompt(theme *Theme, label, initial string) *Prompt {
	ed := NewEditor()
	if initial != "" {
		ed.SetValue(initial)
	}
	return &Prompt{Theme: theme, Label: label, Result: make(chan string, 1), editor: ed}
}

// Handle routes one key; it returns true when the prompt closed.
func (p *Prompt) Handle(k Key, pasteText string) (done bool) {
	switch k.Kind {
	case KeyEsc, KeyCtrlC:
		p.Result <- ""
		return true
	}
	submit, quit, _ := p.editor.Handle(k, pasteText)
	if quit {
		p.Result <- ""
		return true
	}
	if submit != "" || k.Kind == KeyEnter {
		p.Result <- strings.TrimSpace(submit)
		return true
	}
	return false
}

// View renders the label, the field between two rules, and the hint.
func (p *Prompt) View(width int) (lines []string, curRow, curCol int) {
	t := p.Theme
	if t == nil {
		t = NewTheme(NightPalette)
		p.Theme = t
	}
	var out []string
	if p.Label != "" {
		out = append(out, fitRow(" "+t.Fg(SlotAccent, p.Label), width))
	}
	// The editor draws its own rule above and below the field, so this adds
	// none of its own — a prompt with four rules reads as two stacked boxes.
	field, row, col := p.editor.View(width)
	// The cursor's row is counted from the top of the whole prompt, not from
	// the top of the field — the caller places it on the real screen.
	curRow, curCol = row+len(out), col
	out = append(out, field...)
	out = append(out, t.Fg(SlotDim, " Enter to submit · Shift+Enter newline · Esc to cancel"))
	return out, curRow, curCol
}
