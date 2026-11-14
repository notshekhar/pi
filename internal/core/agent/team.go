package agent

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"slices"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/teams"
)

// A turn in a thread team (loop's runTurn/runTeamTurn): the same turn, plus
// the bookkeeping every team panel reads — the member is "running" while it
// goes, what it is doing follows its tool calls, its row is kept fresh, mail
// reaches it between steps, and how the turn ended reaches its lead.

// teamTools is the team set for an unrestricted agent while threadTeams is
// on. Added after task so subagents never inherit them: a subagent spawning
// threads or reporting for its parent would make no sense.
func (r *Run) teamTools(a *assembly, settings config.Settings) {
	if !settings.ThreadTeamsOn() || a.restricted() {
		return
	}
	a.add(teams.Tools(teams.ToolContext{
		SessionID: r.Session.ID, ModelID: r.Config.FullID(), CWD: r.Config.CWD,
		Thinking: string(r.Config.Reasoning), Planning: r.Planning,
	})...)
}

// teamNote is the lead's guidance or a member's rules, keyed off the
// assembled tools. No roster: it would change the prompt whenever someone
// joined; the team travels in saved messages instead.
func teamNote(names []string) string {
	switch {
	case slices.Contains(names, teams.SpawnThreadsTool):
		return teams.LeadNote(teams.MaxThreads())
	case slices.Contains(names, teams.ReportTool):
		return teams.MemberNote()
	}
	return ""
}

// teamTurn is one team turn's bookkeeping. A session in no team gets a
// no-op: it runs exactly as it always did.
type teamTurn struct {
	run       *Run
	sessionID string
	meta      *teams.TurnMeta
	started   time.Time
	stopBeat  func()
	mu        sync.Mutex
	failure   string
}

// beginTeamTurn marks the turn active and keeps its row alive. Checked on
// every beat, not once: a lead's team can start mid-turn.
func (r *Run) beginTeamTurn(meta *teams.TurnMeta) *teamTurn {
	id := r.Session.ID
	t := &teamTurn{run: r, sessionID: id, meta: meta, started: time.Now()}
	teams.MarkTurnActive(id, true)
	if _, ok := teams.TeamOf(id); ok {
		teams.Move(id, teams.StateRunning, "")
	}
	ticker := time.NewTicker(teams.HeartbeatInterval)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				teams.Touch(id)
			case <-done:
				return
			}
		}
	}()
	t.stopBeat = func() { ticker.Stop(); close(done) }
	return t
}

// watch wraps the turn's emitter: an error is the turn's failure, and a tool
// call is what the member is doing.
func (t *teamTurn) watch(emit Emitter) Emitter {
	return func(event string, data any) {
		switch event {
		case "error":
			if s, ok := data.(string); ok {
				t.mu.Lock()
				t.failure = s
				t.mu.Unlock()
			}
		case "tool-call":
			if call, ok := data.(ToolCallEvent); ok {
				t.noteActivity(call)
			}
		}
		emit(event, data)
	}
}

func (t *teamTurn) noteActivity(call ToolCallEvent) {
	if call.ToolName == "" || call.ToolName == teams.WaitForTeamTool {
		return
	}
	team, ok := teams.TeamOf(t.sessionID)
	// The stream can deliver a call after it already ran — a report that
	// finished the member must not come back as what it is "doing".
	if !ok || (team.State != teams.StateRunning && team.State != teams.StateStarting) {
		return
	}
	input, _ := json.Marshal(call.Input, jsonopt.Write)
	if teams.SetActivity(t.sessionID, call.ToolName+argSummary(string(input))) {
		teams.Emit(teams.Change{TeamID: team.TeamID, Kind: teams.ChangeActivity, SessionID: t.sessionID})
	}
}

// end settles the turn — also reached by a lead whose team was created
// mid-turn (it spawned its first threads).
func (t *teamTurn) end(ctx context.Context, err error) {
	t.stopBeat()
	teams.MarkTurnActive(t.sessionID, false)
	m, ok := teams.TeamOf(t.sessionID)
	if !ok {
		return
	}
	t.mu.Lock()
	failure := t.failure
	t.mu.Unlock()
	if failure == "" && err != nil && ctx.Err() == nil {
		failure = err.Error()
	}
	teams.Settle(m, teams.TurnEnd{StartedAt: t.started.UnixMilli(), Aborted: ctx.Err() != nil, Failure: failure, Meta: t.meta})
	// Mail that arrived as the turn wound down starts the next one.
	teams.WakeInbox(t.sessionID)
}

// teamEntryExtra is the opening entry's `team` key, loop's shape.
func teamEntryExtra(meta *teams.TurnMeta) map[string]jsontext.Value {
	if meta == nil {
		return nil
	}
	raw, err := json.Marshal(meta, jsonopt.Write)
	if err != nil {
		return nil
	}
	return map[string]jsontext.Value{"team": raw}
}

// TeamMessageEvent is mid-turn team mail, saved as its own entry.
type TeamMessageEvent struct {
	ID   string          `json:"id"`
	Team *teams.TurnMeta `json:"team"`
	TS   int64           `json:"ts"`
}

// carriedMail is team mail taken mid-turn, and where in the run's history
// it went in.
type carriedMail struct {
	at  int
	msg provider.Message
}

// teamMail hands mail to a thread before its next step. pi's PrepareStep
// messages last one step, but mail is history: once in, it stays where it
// went in on every later step, so the cached prefix never moves and it is
// never sent twice. It is also saved as its own entry, so a resumed attempt
// reads it from the session. Nil when the turn can't get team mail — the
// team tools are a session's only way into a team.
func (r *Run) teamMail(names []string, emit Emitter) ai.PrepareStepFunc {
	id := r.Session.ID
	_, inTeam := teams.TeamOf(id)
	if !inTeam && !slices.Contains(names, teams.SendMessageTool) {
		return nil
	}
	var carried []carriedMail
	return func(_ context.Context, step ai.StepContext) (ai.StepOverrides, error) {
		if msg, ok := r.takeTeamMail(emit); ok {
			carried = append(carried, carriedMail{at: len(step.Messages), msg: msg})
		}
		if len(carried) == 0 {
			return ai.StepOverrides{}, nil
		}
		return ai.StepOverrides{Messages: spliceCarried(step.Messages, carried)}, nil
	}
}

// takeTeamMail takes the session's unread mail as one user message, saved
// as a team entry. wait_for_team takes mail itself, so none is taken while
// the turn sits in it.
func (r *Run) takeTeamMail(emit Emitter) (provider.Message, bool) {
	id := r.Session.ID
	if teams.IsWaiting(id) {
		return nil, false
	}
	team, ok := teams.TeamOf(id)
	if !ok {
		return nil, false
	}
	taken, err := teams.TakeInbox(id)
	mail := teams.MailViews(taken)
	if err != nil || len(mail) == 0 {
		return nil, false
	}
	text := teams.FormatMail(mail)
	meta := &teams.TurnMeta{Kind: "mail", TeamID: team.TeamID, Mail: mail, MidTurn: true}
	msg := ai.UserText(text)
	if entry, err := session.MessageEntry(msg); err == nil {
		entry.Extra = teamEntryExtra(meta)
		r.Session.Lock()
		err = r.Session.AppendAll(entry)
		saved, _ := r.Session.Leaf()
		r.Session.Unlock()
		if err == nil {
			emit("team-message", TeamMessageEvent{ID: saved.ID, Team: meta, TS: saved.TS})
		}
	}
	return msg, true
}

// spliceCarried puts each carried message back at the point it went in.
func spliceCarried(history []provider.Message, carried []carriedMail) []provider.Message {
	out := make([]provider.Message, 0, len(history)+len(carried))
	next := 0
	for i := 0; i <= len(history); i++ {
		for next < len(carried) && carried[next].at == i {
			out = append(out, carried[next].msg)
			next++
		}
		if i < len(history) {
			out = append(out, history[i])
		}
	}
	return out
}

// reportedThisStep ends a member's turn at its report.
func reportedThisStep(step ai.Step) bool {
	for _, c := range step.ToolCalls {
		if c.ToolName == teams.ReportTool {
			return true
		}
	}
	return false
}

// savePrompt saves the turn's prompt — with its team key, when the team
// started it. Its entry id is the turn's id everywhere.
func (r *Run) savePrompt(opts TurnOptions) (session.Entry, bool, error) {
	entry, err := session.MessageEntry(ai.UserText(opts.Input))
	if err != nil {
		return session.Entry{}, false, err
	}
	entry.Extra = teamEntryExtra(opts.Team)
	r.Session.Lock()
	defer r.Session.Unlock()
	if err := r.Session.AppendAll(entry); err != nil {
		return session.Entry{}, false, err
	}
	leaf, ok := r.Session.Leaf()
	return leaf, ok, nil
}

// turnStop is the turn's early stop: a delivered plan, or a member's report.
func turnStop(toolset []ai.Tool) func(ai.Step) bool {
	plan := stopWhen(toolset)
	if !slices.Contains(toolNames(toolset), teams.ReportTool) {
		return plan
	}
	return func(step ai.Step) bool { return reportedThisStep(step) || (plan != nil && plan(step)) }
}

// chainSteps runs the PrepareSteps in order over each other's messages,
// skipping nils.
func chainSteps(steps ...ai.PrepareStepFunc) ai.PrepareStepFunc {
	var out ai.PrepareStepFunc
	for _, s := range steps {
		switch {
		case s == nil:
		case out == nil:
			out = s
		default:
			out = thenPrepareStep(out, s)
		}
	}
	return out
}
