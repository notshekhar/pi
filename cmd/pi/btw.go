package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/tui"
)

// `/btw` in the TUI (loop's btw-threads.ts): ask a side question without
// touching the chat you are in.
//
// The question runs in a fresh session linked to this one (core
// session/btw.go), opened as a background slot — so it works while this
// chat's turn is still running, and Ctrl+S (or `/btw` alone) opens it to
// follow up; `/btw back` returns. When the
// answer is in, it is shown here as a ruled card that is NOT part of this
// conversation: the model on this side never sees the question or the
// answer.

// askedThread is a side thread, as `/btw` lists it.
type askedThread struct {
	sessionID string
	question  string
}

// sideThreads is every side thread asked in this process, by the session it
// was asked from, oldest first.
type sideThreads struct {
	mu       sync.Mutex
	bySource map[string][]askedThread
}

func (st *sideThreads) add(sourceID string, thread askedThread) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.bySource == nil {
		st.bySource = map[string][]askedThread{}
	}
	st.bySource[sourceID] = append(st.bySource[sourceID], thread)
}

func (st *sideThreads) of(sourceID string) []askedThread {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]askedThread{}, st.bySource[sourceID]...)
}

// btwCmd is `/btw <question>`; `/btw` alone lists the side threads asked
// from this chat, and `/btw back` returns to the chat this one was asked
// from.
func (t *repl) btwCmd(ctx context.Context, question string) {
	switch question {
	case "":
		t.listSideThreads()
		return
	case "back":
		t.goBackFromBtw()
		return
	}
	if !hasMessages(t.run.Session) {
		t.dim("nothing to ask about yet — this chat is empty, so just ask it here")
		return
	}
	if t.cfg.ModelID == "" {
		t.dim("choose a model with /model first")
		return
	}
	if err := t.askBtw(ctx, question); err != nil {
		t.sayBtw(tui.SlotWarning, " · "+err.Error())
	}
}

func hasMessages(s *session.Session) bool {
	if s == nil {
		return false
	}
	s.RLock()
	defer s.RUnlock()
	return len(s.BranchMessages()) > 0
}

// askBtw starts a side thread for question and runs the question there.
// It returns once the turn has started: this chat's own queue keeps
// draining while the side thread answers.
func (t *repl) askBtw(ctx context.Context, question string) error {
	source := t.pinned()
	thread, meta, err := session.CreateBtwThread(source.run.Session, question)
	if err != nil {
		return err
	}
	choices := backgroundRunChoices{model: source.cfg.FullID(), thinking: source.cfg.Reasoning, agent: source.agent, useAgent: true}
	s, err := t.slotFor(thread.ID, slotOpening{choices: choices})
	if err != nil {
		return err
	}
	t.sideThreads.add(source.run.Session.ID, askedThread{sessionID: thread.ID, question: question})
	th := t.app.Theme()
	source.do(func() {
		source.app.Print(th.Fg(tui.SlotAccent, "↳ ") + "btw" + th.Fg(tui.SlotDim, " · asking in a side thread — the answer shows here; /btw opens it"))
	})

	// The slot holds its own Session object for this id; the turn appends there.
	sideSession := s.run.Session
	before := len(sideSession.CurrentBranch())
	side := &repl{shared: t.shared, slot: s}
	side.startTurnWith(ctx, question, turnOrigin{btw: &meta, finished: func() {
		source.deliverBtwAnswer(question, btwAnswer(sideSession, before))
	}})
	return nil
}

// btwAnswer is the reply to the side question: the last thing the thread
// said after it was asked.
func btwAnswer(thread *session.Session, before int) string {
	thread.RLock()
	defer thread.RUnlock()
	branch := thread.CurrentBranch()
	if before > len(branch) {
		return ""
	}
	after := branch[before:]
	for i := len(after) - 1; i >= 0; i-- {
		e := after[i]
		if e.Type != session.TypeMessage || e.Role != "assistant" {
			continue
		}
		if text := strings.TrimSpace(session.TextContent(e.Content)); text != "" {
			return text
		}
	}
	return ""
}

// deliverBtwAnswer shows the answer in the chat it was asked from, as a
// card that is not part of this conversation.
func (t *repl) deliverBtwAnswer(question, answer string) {
	if answer == "" {
		t.sayBtw(tui.SlotWarning, " · the side thread ended without an answer — /btw opens it")
		return
	}
	th := t.app.Theme()
	head := th.Fg(tui.SlotAccent, "↳ ") + th.Fg(tui.SlotAccent, th.Bold("btw")) + th.Fg(tui.SlotDim, " · "+strings.Join(strings.Fields(question), " "))
	foot := th.Fg(tui.SlotDim, "not part of this conversation · /btw opens the thread to follow up")
	t.do(func() { t.app.PrintAside(head, answer, foot) })
}

// sayBtw is a "btw" line in tone, then detail in dim.
func (t *repl) sayBtw(tone tui.Slot, detail string) {
	th := t.app.Theme()
	t.do(func() { t.app.Print(th.Fg(tone, "btw") + th.Fg(tui.SlotDim, detail)) })
}

// echoBtwQuestion opens a side thread's question in its transcript: the
// user's box, under a tag saying where it was asked and how to get back. A
// replayed thread draws it the same way. Called on the render loop.
func (t *repl) echoBtwQuestion(meta session.BtwMeta, text string) {
	th := t.app.Theme()
	t.app.TaggedUserEcho(th.Fg(tui.SlotAccent, "↳ ")+th.Fg(tui.SlotAccent, th.Bold("Side question"))+
		th.Fg(tui.SlotDim, " from “"+meta.ParentTitle+"” · that chat never sees this thread · /btw back returns there"), text)
}

// btwBackValue is the picker's way-back row; no session id looks like it.
const btwBackValue = "\x00back"

// btwParent is where this chat was asked from, when it is a side thread.
func (t *repl) btwParent() (session.BtwMeta, bool) {
	s := t.run.Session
	s.RLock()
	defer s.RUnlock()
	return session.BtwParentIn(s.CurrentBranch())
}

// goBackFromBtw is `/btw back`: the chat this side thread was asked from.
func (t *repl) goBackFromBtw() {
	parent, ok := t.btwParent()
	if !ok {
		t.dim("this chat is not a side thread — /btw <question> asks one")
		return
	}
	t.goToChat(parent.ParentID)
}

// listSideThreads is `/btw` alone: the way back when this is a side thread,
// then the side threads asked from this chat, newest first.
func (t *repl) listSideThreads() {
	threads := t.sideThreads.of(t.run.Session.ID)
	parent, isThread := t.btwParent()
	if len(threads) == 0 && !isThread {
		t.dim("no side threads here yet — /btw <question> asks one without touching this chat")
		return
	}
	var items []tui.Item
	if isThread {
		items = append(items, tui.Item{Value: btwBackValue, Label: "← Back to “" + parent.ParentTitle + "”", Description: "the chat this was asked from"})
	}
	items = append(items, t.sideThreadItems(threads)...)
	title := "Side thread"
	if len(threads) > 0 {
		title = fmt.Sprintf("Side threads · %d", len(threads))
	}
	// Select blocks on the render loop, so it must not run on it.
	go func() {
		switch pick := t.app.Select(title, items, 0, ""); {
		case pick == nil:
		case pick.Value == btwBackValue:
			t.goToChat(parent.ParentID)
		default:
			t.goToChat(pick.Value)
		}
	}()
}

// sideThreadItems is the picker rows for threads, newest first.
func (t *repl) sideThreadItems(threads []askedThread) []tui.Item {
	items := make([]tui.Item, 0, len(threads))
	for i := len(threads) - 1; i >= 0; i-- {
		description := "open to follow up"
		if s := t.slots.bySessionID(threads[i].sessionID); s != nil && t.slotTurning(s) {
			description = "answering…"
		}
		items = append(items, tui.Item{Value: threads[i].sessionID, Label: threads[i].question, Description: description})
	}
	return items
}

func (t *repl) slotTurning(s *slot) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return s.turning
}

// goToChat brings a session on screen — a side thread or the chat one was
// asked from — opening it in the background first when no slot holds it.
func (t *repl) goToChat(sessionID string) {
	s, err := t.slotFor(sessionID, slotOpening{})
	if err != nil {
		t.fail("could not open that chat: %s", err)
		return
	}
	t.switchTo(s)
}
