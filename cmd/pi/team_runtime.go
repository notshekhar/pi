package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/teams"
	"github.com/notshekhar/pi/internal/tui"
)

// The TUI's thread-team runtime (loop's team-sessions.ts): each member is a
// background session — a slot like any other, which Ctrl+S switches to —
// and its turns run there, out of sight, writing into its own transcript.

type tuiTeamRuntime struct {
	t      *repl
	parent context.Context
	mu     sync.Mutex
	// pending is team turns handed over while their thread was busy.
	pending map[*slot][]queuedTeamTurn
}

type queuedTeamTurn struct {
	input string
	meta  teams.TurnMeta
}

// claimTeamRuntime makes this TUI the process's team host. The returned
// func gives the claim back.
func (t *repl) claimTeamRuntime(parent context.Context) func() {
	rt := &tuiTeamRuntime{t: t, parent: parent, pending: map[*slot][]queuedTeamTurn{}}
	if !teams.ClaimRuntime(rt, rt) {
		return func() {}
	}
	t.teamHost = rt
	stopBudget := teams.OnChange(func(c teams.Change) {
		if c.Kind == teams.ChangeBudget {
			t.sayBudgetStop(c.TeamID)
		}
	})
	return func() {
		stopBudget()
		t.teamHost = nil
		teams.ReleaseRuntime(rt)
	}
}

// sayBudgetStop: nothing else says why every thread just stopped, and the
// lead may be out of sight.
func (t *repl) sayBudgetStop(teamID string) {
	lead := "the team"
	if snap, ok := teams.TeamSnapshot(teamID); ok {
		lead = snap.Lead.Title
	}
	th := t.app.Theme()
	t.app.Do(func() {
		t.app.Print(th.Fg(tui.SlotWarning, "team stopped") + th.Fg(tui.SlotDim, " — “"+lead+"” reached its budget, so every thread was cancelled"))
	})
}

func (rt *tuiTeamRuntime) Deliver(sessionID, input string, meta teams.TurnMeta) error {
	s, err := rt.memberSlot(sessionID)
	if err != nil {
		return err
	}
	if !rt.start(s, queuedTeamTurn{input: input, meta: meta}) {
		rt.mu.Lock()
		rt.pending[s] = append(rt.pending[s], queuedTeamTurn{input: input, meta: meta})
		rt.mu.Unlock()
	}
	return nil
}

// start runs a team turn in s, unless it is busy. Queued behind a busy
// thread, a turn can outlive its team's stop: that one is dropped, its mail
// back in the inbox.
func (rt *tuiTeamRuntime) start(s *slot, turn queuedTeamTurn) bool {
	if !teams.MayRunTurn(s.run.Session.ID) {
		teams.DropTurn(turn.meta)
		return true
	}
	p := &repl{shared: rt.t.shared, slot: s}
	meta := turn.meta
	return p.startTurnWith(rt.parent, turn.input, turnOrigin{team: &meta})
}

// drain runs what waited for a thread that is free now; a closed thread's
// queue goes back to its inbox.
func (rt *tuiTeamRuntime) drain() {
	open := map[*slot]bool{}
	for _, s := range rt.t.slots.all() {
		open[s] = true
	}
	rt.mu.Lock()
	var ready []struct {
		s    *slot
		turn queuedTeamTurn
	}
	for s, queue := range rt.pending {
		if !open[s] {
			for _, turn := range queue {
				teams.DropTurn(turn.meta)
			}
			delete(rt.pending, s)
			continue
		}
		ready = append(ready, struct {
			s    *slot
			turn queuedTeamTurn
		}{s, queue[0]})
		if rt.pending[s] = queue[1:]; len(rt.pending[s]) == 0 {
			delete(rt.pending, s)
		}
	}
	rt.mu.Unlock()
	for _, r := range ready {
		if !rt.start(r.s, r.turn) {
			rt.mu.Lock()
			rt.pending[r.s] = append([]queuedTeamTurn{r.turn}, rt.pending[r.s]...)
			rt.mu.Unlock()
		}
	}
}

func (rt *tuiTeamRuntime) IsRunning(sessionID string) bool {
	s := rt.t.slots.bySessionID(sessionID)
	if s == nil {
		return false
	}
	rt.t.mu.Lock()
	defer rt.t.mu.Unlock()
	return s.turning
}

func (rt *tuiTeamRuntime) Cancel(sessionID string) {
	s := rt.t.slots.bySessionID(sessionID)
	if s == nil {
		return
	}
	rt.t.mu.Lock()
	cancel := s.cancel
	rt.t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// memberSlot is the slot a thread runs in: open already, or loaded from
// disk into a new background slot that runs as its lead started it, not as
// whatever tab is in front.
func (rt *tuiTeamRuntime) memberSlot(sessionID string) (*slot, error) {
	opening := slotOpening{banner: func(p *repl) { p.introduceMember(sessionID) }}
	if rc, ok := teams.MemberRunConfig(sessionID); ok {
		opening.choices = backgroundRunChoices{thinking: provider.ReasoningEffort(rc.Thinking), agent: rc.Agent, useAgent: true}
	}
	return rt.t.slotFor(sessionID, opening)
}

// echoTeamTurn opens a team turn in the transcript: who it came from, then
// what arrived — the part a typed turn's prompt echo would show.
func (t *repl) echoTeamTurn(meta *teams.TurnMeta, input string) {
	th := t.app.Theme()
	from := "the team"
	switch {
	case meta.Kind == "spawn" && meta.From != nil:
		from = "started by “" + meta.From.Title + "”"
	case len(meta.Mail) == 1:
		from = "message from “" + meta.Mail[0].From.Title + "”"
	case len(meta.Mail) > 1:
		from = fmt.Sprintf("%d messages from the team", len(meta.Mail))
	}
	t.app.Print(th.Fg(tui.SlotAccent, "⧉ ") + th.Fg(tui.SlotDim, from))
	t.app.UserEcho(input)
}

// introduceMember heads a thread's transcript with where it sits.
func (t *repl) introduceMember(sessionID string) {
	team, ok := teams.TeamOf(sessionID)
	if !ok || team.Role != teams.RoleMember {
		return
	}
	th := t.app.Theme()
	t.app.Print(th.Fg(tui.SlotAccent, "⧉ ") + teams.SessionTitle(sessionID) +
		th.Fg(tui.SlotDim, " — a thread in “"+teams.SessionTitle(team.LeadID)+"”'s team · /lead goes back · /team lists every thread"))
}
