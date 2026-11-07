package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/shells"
	"github.com/notshekhar/pi/internal/tui"
)

// /notes — loop's notes-handlers.ts: things to say later, kept with this
// session and nowhere else. `/notes` opens the list: Enter on a note offers
// submit (send it as your next message, which takes it off the list) or
// delete; the list also adds a note and removes them all. `/notes <text>` and
// `/note <text>` add one without opening anything, so a thought can be parked
// mid-turn.

const (
	noteAdd   = "\x00add"
	noteClear = "\x00clear"
)

func notesCount(n int) string {
	if n == 1 {
		return "1 note"
	}
	return fmt.Sprintf("%d notes", n)
}

// noteLabel is one line of a note for a list row; the picker truncates it.
func noteLabel(n session.Note) string { return strings.Join(strings.Fields(n.Text), " ") }

func noteItems(notes []session.Note) []tui.Item {
	items := make([]tui.Item, len(notes))
	for i, n := range notes {
		age := shells.FormatDuration(time.Since(n.CreatedAt))
		items[i] = tui.Item{Value: n.ID, Label: noteLabel(n), Description: age + " ago"}
	}
	return items
}

// notesCmd runs off the input goroutine, as /recipe does: the panel blocks on
// the picker, and a turn may be streaming underneath it.
func (t *repl) notesCmd(args string) {
	text := strings.TrimSpace(args)
	go func() {
		if text != "" {
			t.addNote(text)
			return
		}
		t.notesPanel()
	}()
}

// currentNotes is the notes of the session on screen; an unsaved one has none.
func (t *repl) currentNotes() []session.Note {
	if !t.run.Session.Saved() {
		return nil
	}
	notes, err := session.ListNotes(t.run.Session.ID)
	if err != nil {
		t.fail("notes: %s", err)
	}
	return notes
}

// addNote is what saves a new session: its notes need a session to belong to.
func (t *repl) addNote(text string) {
	s := t.run.Session
	if err := s.Start(); err != nil {
		t.fail("notes: %s", err)
		return
	}
	// A note may be what saved the session, so the status line learns its id.
	id := s.ID
	t.do(func() { t.app.SetSession(id) })
	if _, err := session.AddNote(s.ID, text); err != nil {
		t.fail("notes: %s", err)
		return
	}
	notes, _ := session.ListNotes(s.ID)
	t.dim("note saved · %s in this session · /notes to open", notesCount(len(notes)))
}

func (t *repl) notesPanel() {
	last := 0
	for {
		notes := t.currentNotes()
		items := append(noteItems(notes), tui.Item{Value: noteAdd, Label: "+ add note…"})
		if len(notes) > 0 {
			items = append(items, tui.Item{Value: noteClear, Label: "remove all notes"})
		}
		title := "Notes · none yet"
		if len(notes) > 0 {
			title = fmt.Sprintf("Notes · %d", len(notes))
		}
		pick := t.app.Pick(title, items, min(last, len(items)-1), "")
		if pick == nil {
			return
		}
		last = itemIndex(items, pick.Value)
		switch pick.Value {
		case noteAdd:
			if text := strings.TrimSpace(t.ask("New note", "")); text != "" {
				t.addNote(text)
			}
		case noteClear:
			t.confirmClearNotes(len(notes))
		default:
			if n, ok := findNote(notes, pick.Value); ok && t.actOnNote(n) {
				return
			}
		}
	}
}

func itemIndex(items []tui.Item, value string) int {
	for i, it := range items {
		if it.Value == value {
			return i
		}
	}
	return 0
}

func findNote(notes []session.Note, id string) (session.Note, bool) {
	for _, n := range notes {
		if n.ID == id {
			return n, true
		}
	}
	return session.Note{}, false
}

func (t *repl) confirmClearNotes(count int) {
	pick := t.choose("Remove every note in this session?",
		tui.Item{Value: "yes", Label: "remove all " + notesCount(count)},
		tui.Item{Value: "no", Label: "cancel"})
	if pick == nil || pick.Value != "yes" {
		return
	}
	n, err := session.ClearNotes(t.run.Session.ID)
	if err != nil {
		t.fail("notes: %s", err)
		return
	}
	t.dim("removed %s", notesCount(n))
}

// actOnNote submits or deletes one note; true once the panel should close.
func (t *repl) actOnNote(n session.Note) bool {
	pick := t.choose(noteLabel(n),
		tui.Item{Value: "submit", Label: "submit", Description: "send it as your next message"},
		tui.Item{Value: "delete", Label: "delete", Description: "remove it from this session"})
	if pick == nil {
		return false
	}
	taken, ok, err := session.TakeNote(t.run.Session.ID, n.ID)
	if err != nil {
		t.fail("notes: %s", err)
		return false
	}
	if pick.Value == "delete" {
		return false
	}
	if !ok {
		t.dim("That note is already gone.")
		return false
	}
	t.submitAsTyped(taken.Text)
	return true
}

// submitAsTyped sends text through the composer's own path, so it queues
// behind a running turn exactly like typed input.
func (t *repl) submitAsTyped(text string) {
	t.app.Events <- tui.Event{Kind: tui.EvSubmit, Text: text}
}
