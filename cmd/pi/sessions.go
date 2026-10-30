package main

import (
	"encoding/json"
	"fmt"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/agent"
	"strconv"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/tools"
	"github.com/notshekhar/pi/internal/tui"
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
	// Already live in this pi (running in the background, say): go to it
	// rather than open a second copy that would fight it over the session.
	if live := t.slots.bySessionID(id); live != nil {
		t.switchTo(live)
		return
	}
	// Never replace a session mid-turn: it keeps running, and the resumed
	// one opens beside it.
	if t.isTurning() {
		t.openNew()
	}
	t.firstPrompt = ""
	loaded, err := session.Load(id)
	if err != nil {
		t.fail("resume: %s", err)
		return
	}
	t.run.Session = loaded
	t.run.Tools.Registry = tools.NewRegistry()
	// The tab follows the session that is live now — the resumed one's own
	// name if it has one, and the standing name if it never earned one.
	t.setTabName(tabNameFor(t.slot))
	cost := t.tracker().Format()
	ctxTokens := int(agent.NewCostTracker().SeedFromSession(loaded))

	t.do(func() {
		t.app.Clear()
		t.app.SetSession(loaded.ID)
		t.app.SetCostText(cost)
		t.app.SetContextTokens(ctxTokens)
		th := t.app.Theme()
		t.app.Print(
			th.Fg(tui.SlotAccent, th.Bold("resumed"))+" "+th.Fg(tui.SlotText, loaded.Meta.Label()),
			th.Fg(tui.SlotDim, fmt.Sprintf("%d messages · %s", len(loaded.Messages), loaded.Meta.Detail())),
		)
		t.replayTranscript(loaded)
	})
}

// replayTranscript re-renders a loaded conversation into the transcript
// (loop's renderSessionBranch). Everything on the branch is drawn — a
// compaction drops entries from the model's context, not from the
// conversation — and the latest compaction's box goes where it CUT, before
// the first message it kept: the entry itself is appended when compaction
// runs, later than the cut it records, and drawing it there would put
// surviving messages above their own boundary.
//
// Tool calls and their results are paired up so a replayed row shows the same
// summary and output a live one did; reasoning comes back as its thinking
// block, with how long it took.
func (t *repl) replayTranscript(s *session.Session) {
	branch := s.CurrentBranch()
	results := s.ToolResults()
	latest, hasCompaction := latestCompaction(branch)
	drawn := !hasCompaction
	index := 0
	for _, e := range branch {
		if e.Type != session.TypeMessage {
			continue
		}
		if !drawn && index >= latest.CutAt {
			t.app.AddCompaction(compactionOf(latest))
			drawn = true
		}
		index++
		if msg, ok := session.DecodeContent(e.Role, e.Content); ok {
			t.replayMessage(msg, e.ReasoningMs, results)
		}
	}
	if !drawn {
		t.app.AddCompaction(compactionOf(latest))
	}
}

func latestCompaction(branch []session.Entry) (session.Entry, bool) {
	for i := len(branch) - 1; i >= 0; i-- {
		if branch[i].Type == session.TypeCompact {
			return branch[i], true
		}
	}
	return session.Entry{}, false
}

func compactionOf(e session.Entry) tui.Compaction {
	var focus string
	_ = json.Unmarshal(e.Extra["focus"], &focus)
	return tui.Compaction{Summary: e.Summary, Handoff: e.Handoff, Focus: focus, TokensBefore: e.TokensBefore}
}

func (t *repl) replayMessage(msg provider.Message, reasoningMs []int64, results map[string]session.ToolOutcome) {
	for _, part := range session.Parts(msg) {
		switch p := part.(type) {
		case session.ReplayUser:
			t.app.UserEcho(p.Text)
		case session.ReplayThinking:
			var took time.Duration
			if len(reasoningMs) > 0 {
				took, reasoningMs = time.Duration(reasoningMs[0])*time.Millisecond, reasoningMs[1:]
			}
			t.app.AddThinking(p.Text, took)
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
		t.do(func() {
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
	// A name the user chose outranks the generated one everywhere the
	// session is shown, the tab included.
	t.setTabName(name)
	t.dim("session name → %s", name)
}
