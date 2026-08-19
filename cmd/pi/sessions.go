package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/modules/core/session"
	"github.com/notshekhar/pi/internal/modules/core/tools"
	"github.com/notshekhar/pi/internal/modules/tui"
)

// Session management: list, resume, fork, rename, delete.

// pickSession opens the session picker and resumes the chosen conversation.
// A bare id resumes it directly.
func (t *repl) pickSession(rest string) {
	if t.busy() {
		return
	}
	if rest != "" {
		t.resume(rest)
		return
	}

	metas, err := session.List()
	if err != nil {
		t.fail("sessions: %s", err)
		return
	}
	if len(metas) == 0 {
		t.dim("no saved sessions")
		return
	}

	// Sessions from THIS directory. A session list that reaches across every
	// project you have ever opened is a list you have to search rather than
	// read, and the thing you almost always want is the conversation you were
	// having here yesterday.
	here := make([]session.Meta, 0, len(metas))
	for _, m := range metas {
		if m.CWD == "" || m.CWD == t.cfg.CWD {
			here = append(here, m)
		}
	}
	if len(here) == 0 {
		t.dim("no sessions in this cwd")
		return
	}

	// The first row cycles date buckets; typing filters the rest. Two
	// different narrowing gestures because they answer different questions —
	// "when was it" is a range, "what was it about" is a string.
	filters := dateFilters()
	at := 0
	go func() {
		t.inPanel.Store(true)
		defer t.inPanel.Store(false)
		for {
			filter := filters[at]
			shown := make([]session.Meta, 0, len(here))
			for _, m := range here {
				if filter.test(m.Updated) {
					shown = append(shown, m)
				}
			}
			items := []tui.Item{{
				Value:       dateFilterRow,
				Label:       "⏷ date: " + filter.label,
				Description: "Enter cycles · all → today → yesterday → last 7 days → last 30 days",
			}}
			for _, m := range shown {
				label := fmt.Sprintf("%s  %s", shortID(m.ID), orDefault(m.Model, "?"))
				if m.Name != "" {
					label = fmt.Sprintf("%s  ·  %s", m.Name, shortID(m.ID))
				}
				items = append(items, tui.Item{
					Value: m.ID,
					Label: label,
					Description: fmt.Sprintf("%s  ·  %s",
						sessionTime(m.Updated), elide(orDefault(m.Title, "(no messages)"), 80)),
				})
			}
			choice := t.app.Pick(fmt.Sprintf("Resume session · %d/%d", len(shown), len(here)), items, 0, "")
			if choice == nil {
				return
			}
			if choice.Value == dateFilterRow {
				at = (at + 1) % len(filters)
				continue
			}
			t.resume(choice.Value)
			return
		}
	}()
}

// dateFilterRow is the sentinel value of the bucket-cycling row.
const dateFilterRow = "\x00date-filter"

// dateFilter is one bucket of the session list.
type dateFilter struct {
	label string
	test  func(time.Time) bool
}

// dateFilters are the buckets, widest first, in the order Enter cycles them.
func dateFilters() []dateFilter {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	day := 24 * time.Hour
	return []dateFilter{
		{"all", func(time.Time) bool { return true }},
		{"today", func(at time.Time) bool { return !at.Before(today) }},
		{"yesterday", func(at time.Time) bool { return !at.Before(today.Add(-day)) && at.Before(today) }},
		{"last 7 days", func(at time.Time) bool { return !at.Before(today.Add(-6 * day)) }},
		{"last 30 days", func(at time.Time) bool { return !at.Before(today.Add(-29 * day)) }},
	}
}

// shortID is the id as a picker shows it — enough to tell two apart without
// spending half the row on a value nobody reads in full.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// sessionTime is a timestamp read against today: recent sessions get a
// weekday word, older ones a date. "today 10:56 PM" answers "is this the one
// I was just in" without any arithmetic.
func sessionTime(at time.Time) string {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	clock := at.Format("3:04 PM")
	switch {
	case !at.Before(today):
		return "today " + clock
	case !at.Before(today.Add(-24 * time.Hour)):
		return "yesterday " + clock
	}
	return at.Format("1/2/2006") + " " + clock
}

// resume swaps the live session for a stored one.
//
// The tool registry is reset with it: read-before-edit state describes files
// this process has seen, and a resumed conversation has seen none of them —
// carrying the old registry across would let an edit through on a file the
// agent has not actually read in this run.
func (t *repl) resume(id string) {
	loaded, err := session.Load(id)
	if err != nil {
		t.fail("resume: %s", err)
		return
	}
	t.run.Session = loaded
	t.run.Tools.Registry = tools.NewRegistry()

	t.app.Do(func() {
		t.app.Clear()
		th := t.app.Theme()
		t.app.Print(
			th.Fg(tui.SlotAccent, th.Bold("resumed"))+" "+th.Fg(tui.SlotText, loaded.Meta.Label()),
			th.Fg(tui.SlotDim, fmt.Sprintf("%d messages · %s", len(loaded.Messages), loaded.Meta.Detail())),
		)
		t.replayTranscript(loaded)
	})
}

// replayTranscript re-renders a loaded conversation into the transcript.
//
// Tool calls and their results are paired up so a replayed row shows the same
// summary and output a live one did. Reasoning is not replayed: providers
// hand back signed blobs that are meant for the model, not the reader, and
// showing them would fill the screen with noise.
func (t *repl) replayTranscript(s *session.Session) {
	results := s.ToolResults()
	for _, msg := range s.Messages {
		for _, part := range session.Parts(msg) {
			switch p := part.(type) {
			case session.ReplayUser:
				t.app.UserEcho(p.Text)
			case session.ReplayAssistant:
				t.app.AssistantDelta(p.Text)
				t.app.AssistantEnd()
			case session.ReplayToolCall:
				t.app.ToolStart(p.ID, p.Name, p.Input)
				out, isErr := results[p.ID].Text, results[p.ID].IsError
				t.app.ToolEnd(p.ID, out, isErr)
			}
		}
	}
}

// forkSession branches from a previous user message.
//
// A picker rather than a straight copy: forking exists to rewind and take a
// different path from some earlier point, and duplicating the whole thing at
// the current position is what /clone does.
func (t *repl) forkSession() {
	if t.busy() {
		return
	}
	s := t.run.Session
	type mark struct {
		text string
		n    int // messages to keep, i.e. everything BEFORE this prompt
	}
	var marks []mark
	for i, msg := range s.Messages {
		for _, part := range session.Parts(msg) {
			if u, ok := part.(session.ReplayUser); ok {
				marks = append(marks, mark{text: u.Text, n: i})
			}
		}
	}
	if len(marks) == 0 {
		t.dim("nothing to fork from yet")
		return
	}

	items := make([]tui.Item, 0, len(marks))
	for i, m := range marks {
		items = append(items, tui.Item{
			Value:       fmt.Sprintf("%d", m.n),
			Label:       tui.Truncate(strings.ReplaceAll(m.text, "\n", " "), 60),
			Description: fmt.Sprintf("prompt %d of %d", i+1, len(marks)),
		})
	}
	// Pick blocks on the render loop, so it must not run on it.
	go func() {
		choice := t.app.Pick("Fork from", items, len(items)-1, "")
		if choice == nil {
			return
		}
		n, err := strconv.Atoi(choice.Value)
		if err != nil {
			return
		}
		forked, err := session.ForkAt(s, n)
		if err != nil {
			t.fail("fork: %s", err)
			return
		}
		t.run.Session = forked
		t.run.Tools.Registry = tools.NewRegistry()
		t.app.Do(func() {
			t.app.Clear()
			t.replayTranscript(forked)
			t.dim("forked at prompt — %d messages kept, the original is untouched", len(forked.Messages))
		})
	}()
}

// renameSession sets the title a listing shows.
func (t *repl) renameSession(rest string) {
	// With no argument this asks, prefilled with the current name — printing
	// the name instead answers a question nobody asked by typing a command
	// whose whole purpose is to change it.
	next := strings.TrimSpace(rest)
	if next == "" {
		go func() {
			t.inPanel.Store(true)
			defer t.inPanel.Store(false)
			if name := strings.TrimSpace(t.ask("session name", t.run.Session.Meta.Name)); name != "" {
				t.applySessionName(name)
			}
		}()
		return
	}
	t.applySessionName(next)
}

func (t *repl) applySessionName(name string) {
	if err := t.run.Session.SetTitle(name); err != nil {
		t.fail("rename: %s", err)
		return
	}
	t.dim("session name → %s", name)
}
