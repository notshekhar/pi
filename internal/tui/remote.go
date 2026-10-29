package tui

// Prompts that something outside the terminal can also answer — cmux's
// sidebar cards (loop's bash-approve / ask-user races). Whichever side
// answers first wins; an answer from outside closes the prompt on screen.

// AskRemote is Ask, raced against remote. A value on remote closes the
// prompt and is the answer.
func (a *App) AskRemote(tool, subject, reason string, remote <-chan bool) bool {
	defer a.blocked(tool + " approval")()
	done := make(chan bool, 1)
	var c *Confirm
	a.Do(func() {
		c = &Confirm{Theme: a.theme, Tool: tool, Subject: subject, Reason: reason, Result: done}
		a.confirm = c
	})
	select {
	case v := <-done:
		return v
	case v := <-remote:
		a.Do(func() {
			if a.confirm == c {
				a.confirm = nil
			}
		})
		return v
	}
}

// AskChoiceRemote is AskChoice, raced against remote.
func (a *App) AskChoiceRemote(question string, options []string, remote <-chan string) string {
	defer a.blocked("question")()
	if len(options) == 0 {
		return ""
	}
	items := make([]Item, 0, len(options))
	for _, o := range options {
		items = append(items, Item{Value: o, Label: o})
	}
	done := make(chan *Item, 1)
	var m *Modal
	a.Do(func() {
		m = &Modal{Mode: ModalSearch, Title: question, All: items, Result: done, Theme: a.theme}
		m.Refilter()
		a.modal = m
	})
	select {
	case choice := <-done:
		if choice != nil {
			return choice.Value
		}
		return ""
	case v := <-remote:
		a.Do(func() {
			if a.modal == m {
				a.modal = nil
			}
		})
		return v
	}
}
