package main

import (
	"context"
	"fmt"
	"github.com/notshekhar/pi/internal/core/cmux"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/hooks"
	"github.com/notshekhar/pi/internal/core/permissions"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/sound"
	"github.com/notshekhar/pi/internal/core/tools"
	"github.com/notshekhar/pi/internal/tui"
)

// Live sessions in one interactive pi — loop's slots.ts and
// session-roster.ts. Several can be mid-turn at once: /new while a turn runs,
// or the Ctrl+S switcher, sends the running one to the background instead of
// killing it. Exactly one is the FOREGROUND, the one the screen shows and the
// keyboard talks to. Work that outlives a switch (a turn) pins its own slot,
// so a turn finishing out of sight writes into its own transcript.

type slotSet struct {
	mu      sync.Mutex
	list    []*slot
	fg      *slot
	nextKey int
}

func newSlotSet(first *slot) *slotSet {
	ss := &slotSet{}
	ss.add(first)
	ss.fg = first
	return ss
}

func (ss *slotSet) add(s *slot) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.nextKey++
	s.key = ss.nextKey
	if s.liveStatus == "" {
		s.liveStatus = "idle"
	}
	s.lastActivity = time.Now()
	ss.list = append(ss.list, s)
}

func (ss *slotSet) all() []*slot {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return append([]*slot{}, ss.list...)
}

func (ss *slotSet) foreground() *slot {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.fg
}

func (ss *slotSet) isForeground(s *slot) bool { return ss.foreground() == s }

func (ss *slotSet) bySessionID(id string) *slot {
	if id == "" {
		return nil
	}
	for _, s := range ss.all() {
		if s.run.Session != nil && s.run.Session.Saved() && s.run.Session.ID == id {
			return s
		}
	}
	return nil
}

func (ss *slotSet) remove(s *slot) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if s == ss.fg {
		return
	}
	for i, x := range ss.list {
		if x == s {
			ss.list = append(ss.list[:i], ss.list[i+1:]...)
			break
		}
	}
	for _, w := range s.waiters {
		w <- false
	}
	s.waiters = nil
}

// ── statuses (loop's live-status.ts) ────────────────────────────────────

var liveGlyphs = map[string]string{"needs-input": "◆", "working": "●", "done": "✓", "failed": "✗", "idle": "○"}
var liveWords = map[string]string{"needs-input": "needs you", "working": "working", "done": "done", "failed": "failed", "idle": "idle"}
var liveOrder = []string{"needs-input", "working", "done", "failed", "idle"}

func liveSlot(status string) tui.Slot {
	switch status {
	case "needs-input":
		return tui.SlotWarning
	case "working":
		return tui.SlotAccent
	case "done":
		return tui.SlotSuccess
	case "failed":
		return tui.SlotError
	}
	return tui.SlotDim
}

func liveRank(status string) int {
	for i, s := range liveOrder {
		if s == status {
			return i
		}
	}
	return len(liveOrder)
}

// setStatus records what a slot is doing; parked prompts outrank everything.
func (t *repl) setLiveStatus(s *slot, status, activity string) {
	t.slots.mu.Lock()
	s.lastActivity = time.Now()
	next := status
	if len(s.waiters) > 0 {
		next = "needs-input"
	}
	if next == "working" || next == "needs-input" {
		if activity != "" {
			s.activity = activity
		}
	} else {
		s.activity = ""
	}
	changed := next != s.liveStatus
	s.liveStatus = next
	t.slots.mu.Unlock()
	if changed {
		t.refreshSessionsChip()
	}
}

// settle is a finished turn: seen when on screen, else done/failed until
// the user looks.
func (t *repl) settle(s *slot, failed bool) {
	switch {
	case t.slots.isForeground(s):
		t.setLiveStatus(s, "idle", "")
	case failed:
		t.setLiveStatus(s, "failed", "")
	default:
		t.setLiveStatus(s, "done", "")
	}
}

// refreshSessionsChip is loop's formatSessionsChip on the status line.
func (t *repl) refreshSessionsChip() {
	fg := t.slots.foreground()
	var others []*slot
	for _, s := range t.slots.all() {
		if s != fg {
			others = append(others, s)
		}
	}
	t.app.Do(func() {
		th := t.app.Theme()
		if len(others) == 0 {
			t.app.SetSessionsChip("")
			return
		}
		counts := map[string]int{}
		for _, s := range others {
			counts[s.liveStatus]++
		}
		var parts []string
		for _, st := range []string{"needs-input", "working", "done", "failed"} {
			if counts[st] > 0 {
				parts = append(parts, th.Fg(liveSlot(st), fmt.Sprintf("%s %d %s", liveGlyphs[st], counts[st], liveWords[st])))
			}
		}
		if len(parts) == 0 {
			noun := "sessions"
			if len(others) == 1 {
				noun = "session"
			}
			parts = append(parts, th.Fg(tui.SlotDim, fmt.Sprintf("%d other %s", len(others), noun)))
		}
		t.app.SetSessionsChip(strings.Join(parts, th.Fg(tui.SlotDim, " · ")) + th.Fg(tui.SlotDim, " (ctrl+s)"))
	})
}

// ── pinning ─────────────────────────────────────────────────────────────

// pinned is this repl fixed to its current slot: what a turn holds, so a
// switch mid-turn does not move where it writes.
func (t *repl) pinned() *repl { return &repl{shared: t.shared, slot: t.slot} }

// do runs f on the render loop against this slot's conversation.
func (t *repl) do(f func()) { t.app.DoIn(t.conv, f) }

// waitForeground parks an agent-driven prompt until its session is on
// screen (true), or its turn is cancelled first (false).
func (t *repl) waitForeground(s *slot, label string, ctx context.Context) bool {
	if t.slots.isForeground(s) {
		return true
	}
	ch := make(chan bool, 1)
	t.slots.mu.Lock()
	s.waiters = append(s.waiters, ch)
	t.slots.mu.Unlock()
	t.setLiveStatus(s, "needs-input", "Waiting: "+label)
	select {
	case ok := <-ch:
		return ok
	case <-ctx.Done():
		t.slots.mu.Lock()
		for i, w := range s.waiters {
			if w == ch {
				s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
				break
			}
		}
		t.slots.mu.Unlock()
		status := "idle"
		if s.turning {
			status = "working"
		}
		t.setLiveStatus(s, status, "")
		return false
	}
}

// wireRun points a slot's run at the UI: prompts gated on its session being
// on screen, subagent progress into its own transcript.
func (t *repl) wireRun(s *slot) {
	p := &repl{shared: t.shared, slot: s}
	app := t.app
	s.run.Tools.Ask = func(ctx context.Context, question string, options []string) string {
		if !p.waitForeground(s, "question", ctx) {
			return ""
		}
		sound.Attention()
		bridge := cmux.Current()
		if !bridge.Active() {
			return app.AskChoice(question, options)
		}
		// cmux's card for the same question: whichever answers first wins,
		// and the loser is withdrawn.
		remote := make(chan string, 1)
		rctx, withdraw := context.WithCancel(ctx)
		defer withdraw()
		go func() {
			opts := make([]cmux.Option, len(options))
			for i, o := range options {
				opts[i] = cmux.Option{Label: o}
			}
			sel := bridge.RequestQuestions(rctx, []cmux.Question{{Header: "question", Question: question, Options: opts}})
			// No answer from cmux is not an answer: only the TUI settles it.
			if len(sel) > 0 {
				remote <- sel[0]
			}
		}()
		return app.AskChoiceRemote(question, options, remote)
	}
	s.run.Ask = func(ctx context.Context, tool string, args map[string]any, reason string) bool {
		if !p.waitForeground(s, "approve "+tool, ctx) {
			return false
		}
		sound.Attention()
		subject := permissions.Subject(tool, args)
		bridge := cmux.Current()
		if !bridge.Active() {
			return app.Ask(tool, subject, reason)
		}
		remote := make(chan bool, 1)
		rctx, withdraw := context.WithCancel(ctx)
		defer withdraw()
		go func() {
			kind, name := "path", "file"
			switch tool {
			case "bash":
				kind, name = "bash", "bash"
			case "read", "write", "edit", "ls", "grep", "find":
			default:
				name = tool
			}
			d := bridge.RequestApproval(rctx, cmux.ApprovalRequest{Kind: kind, ToolName: name, Body: subject, Title: reason})
			if d != nil {
				remote <- d.Mode != "deny"
			}
		}()
		return app.AskRemote(tool, subject, reason, remote)
	}
	s.run.Progress = func(id, status string) {
		p.do(func() { app.ToolStatus(id, status) })
		if status == "" {
			go p.fireHook(context.Background(), hooks.Context{Event: hooks.SubagentStop, ToolName: "task"})
		}
	}
}

// ── switching ───────────────────────────────────────────────────────────

func (t *repl) isEmptySlot(s *slot) bool {
	return !s.run.Session.Saved() && !s.turning && len(s.queue) == 0 && len(s.waiters) == 0
}

// switchTo brings target on screen (loop's slots.switchTo + onSwitch).
func (t *repl) switchTo(target *slot) {
	t.slots.mu.Lock()
	from := t.slots.fg
	if target == from {
		t.slots.mu.Unlock()
		return
	}
	t.slots.fg = target
	if target.liveStatus == "done" || target.liveStatus == "failed" {
		target.liveStatus = "idle"
	}
	waiting := target.waiters
	target.waiters = nil
	t.slots.mu.Unlock()

	t.mu.Lock()
	t.slot = target
	t.mu.Unlock()
	t.setTabName(tabNameFor(target))
	done := make(chan struct{})
	t.app.Do(func() {
		from.draft = t.app.EditorText()
		t.app.ShowConversation(target.conv)
		t.app.SetEditorText(target.draft)
		close(done)
	})
	<-done
	if t.isEmptySlot(from) {
		t.slots.remove(from)
	}
	// Anything it parked while out of sight is let through now.
	if len(waiting) > 0 {
		status := "idle"
		if target.turning {
			status = "working"
		}
		t.setLiveStatus(target, status, "")
	}
	for _, w := range waiting {
		w <- true
	}
	t.refreshSessionsChip()
	// Input queued while it was out of sight runs now that it is not.
	if !target.turning && len(target.queue) > 0 {
		t.drainNext()
	}
}

// openNew opens a fresh session and brings it on screen; the current one
// keeps running (loop's roster.openNew).
func (t *repl) openNew() *slot {
	from := t.slots.foreground()
	cfg := from.cfg
	run := *from.run
	tc := *from.run.Tools
	tc.Registry, tc.Todos = tools.NewRegistry(), &tools.TodoList{}
	run.Tools = &tc
	run.Session = session.New(cfg.FullID(), cfg.CWD)
	run.Planning = false
	s := &slot{cfg: cfg, run: &run, agent: from.agent, conv: tui.NewConversation(cfg.FullID())}
	t.slots.add(s)
	t.wireRun(s)
	p := &repl{shared: t.shared, slot: s}
	window := 0
	reasoning := true
	if m, ok := config.ModelInfo(cfg.Provider, cfg.ModelID); ok {
		window, reasoning = m.Context, m.Reasoning
	}
	agentName := from.agent
	p.do(func() {
		t.app.SetModel(cfg.FullID())
		t.app.SetAgent(agentName)
		t.app.SetThinking(thinkingName(cfg.Reasoning))
		t.app.SetModelReasoning(reasoning)
		t.app.SetUsage(0, 0, window, 0)
		t.app.ShowWelcome(p.welcomeInfo())
		p.printStartupNotices()
	})
	t.switchTo(s)
	return s
}

func slotTitle(s *slot) string {
	if n := s.run.Session.Name(); n != "" {
		return n
	}
	if s.firstPrompt != "" {
		return strings.Join(strings.Fields(s.firstPrompt), " ")
	}
	return "new session"
}

func formatAge(d time.Duration) string {
	sec := int(d.Round(time.Second) / time.Second)
	switch {
	case sec < 60:
		return "now"
	case sec < 3600:
		return fmt.Sprintf("%dm", sec/60)
	case sec < 86_400:
		return fmt.Sprintf("%dh", sec/3600)
	}
	return fmt.Sprintf("%dd", sec/86_400)
}

// showSwitcher is Ctrl+S: switch to a live session, or start a new one.
func (t *repl) showSwitcher() {
	const newKey = "\x00new"
	fg := t.slots.foreground()
	rows := t.slots.all()
	sort.SliceStable(rows, func(i, j int) bool {
		if ri, rj := liveRank(rows[i].liveStatus), liveRank(rows[j].liveStatus); ri != rj {
			return ri < rj
		}
		return rows[i].lastActivity.After(rows[j].lastActivity)
	})
	desc := "start fresh"
	if fg.turning {
		desc = "this one keeps running"
	}
	th := t.app.Theme()
	items := []tui.Item{{Value: newKey, Label: "+ New session", Description: desc}}
	initial := 0
	for i, s := range rows {
		here := ""
		if s == fg {
			here = "here · "
		}
		activity := ""
		if s.liveStatus != "idle" {
			a := s.activity
			if a == "" {
				a = liveWords[s.liveStatus]
			}
			activity = a + " · "
		}
		if initial == 0 && s.liveStatus == "needs-input" && s != fg {
			initial = i + 1
		}
		items = append(items, tui.Item{Value: fmt.Sprint(s.key),
			Label:       th.Fg(liveSlot(s.liveStatus), liveGlyphs[s.liveStatus]) + " " + slotTitle(s),
			Description: here + activity + formatAge(time.Since(s.lastActivity))})
	}
	pick := t.app.Pick(fmt.Sprintf("Sessions · %d live", len(rows)), items, initial, "")
	if pick == nil {
		return
	}
	if pick.Value == newKey {
		t.openNew()
		return
	}
	for _, s := range rows {
		if fmt.Sprint(s.key) == pick.Value {
			t.switchTo(s)
			return
		}
	}
}
