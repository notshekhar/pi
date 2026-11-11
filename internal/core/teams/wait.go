package teams

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/core/tools"
)

// wait_for_team: pause a turn, at no cost, until the team has something for
// it, then say what happened.

type waitArgs struct {
	Until      json.RawMessage `json:"until,omitempty"`
	TimeoutSec *float64        `json:"timeoutSec,omitempty"`
}

type waitOutcome string

const (
	waitDone    waitOutcome = "done"
	waitMessage waitOutcome = "message"
	waitChanged waitOutcome = "changed"
	waitQuiet   waitOutcome = "quiet"
	waitTimeout waitOutcome = "timeout"
	waitAborted waitOutcome = "aborted"
)

func waitTool(c ToolContext) ai.Tool {
	return tools.LoopTool(WaitForTeamTool, func(ctx context.Context, a waitArgs) (ai.ToolResult, error) {
		return ai.ToolText(waitForTeam(ctx, c, a)), nil
	})
}

// waiter is one wait_for_team call in progress.
type waiter struct {
	self, teamID string
	mode         string // all | any | list
	targets      []string
	startState   map[string]State
	collected    []MailView
}

func waitForTeam(ctx context.Context, c ToolContext, a waitArgs) string {
	self := c.SessionID
	team, ok := TeamOf(self)
	if !ok {
		return noTeamYet
	}
	w, refusal := newWaiter(self, team.TeamID, a.Until)
	if refusal != "" {
		return refusal
	}
	timeout := waitTimeoutFor(a.TimeoutSec)
	started := time.Now()

	if !isStoppedThread(self) {
		Move(self, StateWaiting, "")
	}
	mark(waiting, self, true)
	outcome := w.run(ctx, started, timeout)
	mark(waiting, self, false)
	if !isStoppedThread(self) {
		next := StateRunning
		if ctx.Err() != nil {
			next = StateIdle
		}
		Move(self, next, "")
	}
	// A cancelled turn drops this result, so the mail goes back to the inbox
	// for the next one instead of going with it.
	if ctx.Err() != nil {
		outcome = waitAborted
		ids := make([]int64, len(w.collected))
		for i, m := range w.collected {
			ids[i] = m.ID
		}
		_ = ReturnToInbox(ids)
		w.collected = nil
	}
	return w.result(outcome, time.Since(started))
}

func waitTimeoutFor(sec *float64) time.Duration {
	s := float64(defaultWaitSec)
	if sec != nil {
		s = *sec
	}
	return time.Duration(min(maxWaitSec, max(minWaitSec, s)) * float64(time.Second))
}

func newWaiter(self, teamID string, until json.RawMessage) (*waiter, string) {
	roster, _ := ListTeam(teamID)
	mode, ids := decodeUntil(until)
	w := &waiter{self: self, teamID: teamID, mode: mode, startState: map[string]State{}}
	now := time.Now()
	for _, m := range roster {
		w.startState[m.SessionID] = m.Effective(now)
		if m.Role == RoleMember && m.SessionID != self {
			w.targets = append(w.targets, m.SessionID)
		}
	}
	if mode == "list" {
		if refusal := checkWaitTargets(ids, roster, self); refusal != "" {
			return nil, refusal
		}
		w.targets = ids
	}
	return w, ""
}

// checkWaitTargets: an empty list would be "finished" at once and tell the
// caller every thread is done; its own id is "waiting" for as long as it
// waits, so only the timeout could end it.
func checkWaitTargets(ids []string, roster []Member, self string) string {
	team := "The team:\n" + rosterText(roster, self)
	if len(ids) == 0 {
		return `REJECTED: until names no thread. Pass thread ids, "all" or "any". ` + team
	}
	if slices.Contains(ids, self) {
		return "REJECTED: you cannot wait for yourself. " + team
	}
	var unknown []string
	for _, id := range ids {
		if !slices.ContainsFunc(roster, func(m Member) bool { return m.SessionID == id }) {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return fmt.Sprintf("REJECTED: not in this team: %s. %s", strings.Join(unknown, ", "), team)
	}
	return ""
}

func (w *waiter) run(ctx context.Context, started time.Time, timeout time.Duration) waitOutcome {
	for {
		taken, _ := TakeInbox(w.self)
		w.collected = append(w.collected, MailViews(taken)...)
		roster, _ := ListTeam(w.teamID)
		if outcome, done := w.check(roster); done {
			return outcome
		}
		if ctx.Err() != nil {
			return waitAborted
		}
		left := timeout - time.Since(started)
		if left <= 0 {
			return waitTimeout
		}
		w.sleep(ctx, min(left, waitTick))
	}
}

// check is whether the wait is over, and why.
func (w *waiter) check(roster []Member) (waitOutcome, bool) {
	if w.mode == "any" {
		// "Someone joined" is news, not something to answer — it rides along
		// with whatever does end the wait.
		if slices.ContainsFunc(w.collected, func(m MailView) bool { return m.Kind != KindUpdate }) {
			return waitMessage, true
		}
		if w.aThreadFinished(roster) {
			return waitChanged, true
		}
		if !someoneIsWorking(roster, w.self) {
			return waitQuiet, true
		}
		return "", false
	}
	if slices.ContainsFunc(w.collected, func(m MailView) bool { return m.Kind == KindMessage }) {
		return waitMessage, true
	}
	if w.targetsFinished(roster) {
		return waitDone, true
	}
	return "", false
}

func (w *waiter) targetsFinished(roster []Member) bool {
	now := time.Now()
	for _, id := range w.targets {
		i := slices.IndexFunc(roster, func(m Member) bool { return m.SessionID == id })
		if i >= 0 && !roster[i].Effective(now).Finished() {
			return false
		}
	}
	return true
}

// aThreadFinished is whether a thread finished since the wait began. The
// lead's own turns ending say nothing about the threads.
func (w *waiter) aThreadFinished(roster []Member) bool {
	now := time.Now()
	for _, m := range roster {
		state := m.Effective(now)
		if m.Role == RoleMember && m.SessionID != w.self && state.Finished() && w.startState[m.SessionID] != state {
			return true
		}
	}
	return false
}

// someoneIsWorking is whether anyone is left who could still write to self,
// the lead included. A teammate its own mail just woke counts, though its
// row has not moved yet.
func someoneIsWorking(roster []Member, self string) bool {
	now := time.Now()
	for _, m := range roster {
		if m.SessionID != self && (!m.Effective(now).Finished() || IsBusyHere(m.SessionID)) {
			return true
		}
	}
	return false
}

// sleep returns at the first nudge, the cancel, or after d.
func (w *waiter) sleep(ctx context.Context, d time.Duration) {
	woke := make(chan struct{}, 1)
	stop := addWaiter(w.self, func() {
		select {
		case woke <- struct{}{}:
		default:
		}
	})
	defer stop()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-woke:
	case <-timer.C:
	case <-ctx.Done():
	}
}

var waitHeads = map[waitOutcome]string{
	waitDone:    "Every thread has finished.",
	waitMessage: "A teammate sent you a message.",
	waitChanged: "A thread finished.",
	waitQuiet:   "Nobody in the team is working, so nothing more is coming.",
	waitAborted: "Stopped waiting: the turn was cancelled.",
}

func (w *waiter) result(outcome waitOutcome, waited time.Duration) string {
	head, ok := waitHeads[outcome]
	if !ok {
		head = fmt.Sprintf("Stopped waiting after %ds; the team is still working.", int(waited.Round(time.Second)/time.Second))
	}
	roster, _ := ListTeam(w.teamID)
	var states []string
	now := time.Now()
	for _, m := range roster {
		if m.SessionID == w.self || m.Role != RoleMember {
			continue
		}
		line := fmt.Sprintf("- \"%s\" (id %s): %s", m.Title, m.SessionID, m.Effective(now))
		if m.Activity != "" {
			line += " — " + m.Activity
		}
		states = append(states, line)
	}
	body := strings.Join(states, "\n")
	if body == "" {
		body = "(none)"
	}
	out := head + "\n\nThreads:\n" + body
	if len(w.collected) > 0 {
		out += "\n\n" + FormatMail(w.collected)
	}
	return out
}

// isStoppedThread: a stopped thread stays stopped, whatever a wait it was in
// would otherwise leave behind.
func isStoppedThread(sessionID string) bool {
	m, ok := TeamOf(sessionID)
	return ok && m.State == StateStopped
}
