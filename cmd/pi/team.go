package main

import (
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/core/teams"
	"github.com/notshekhar/pi/internal/tui"
)

// /team and /lead — loop's team-sessions.ts: every thread in this session's
// team, jump to one, stop them all; and back to the thread that started
// this one.

var stateGlyph = map[teams.State]string{
	teams.StateStarting: "◌", teams.StateRunning: "●", teams.StateWaiting: "◔", teams.StateIdle: "○",
	teams.StateDone: "✓", teams.StateFailed: "✗", teams.StateStopped: "■",
}

func stateSlot(s teams.State) tui.Slot {
	switch s {
	case teams.StateRunning, teams.StateStarting:
		return tui.SlotAccent
	case teams.StateWaiting:
		return tui.SlotWarning
	case teams.StateDone:
		return tui.SlotSuccess
	case teams.StateFailed:
		return tui.SlotError
	}
	return tui.SlotDim
}

func teamUSD(v float64) string {
	if v >= 1 {
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("$%.3f", v)
}

// stopTeamValue is the picker's Stop row; no session id looks like it.
const stopTeamValue = "\x00stop"

func (t *repl) teamCmd(args string) {
	here := t.run.Session.ID
	team, ok := teams.TeamOf(here)
	if !ok {
		t.dim("this session is not in a thread team — with “thread teams” on in /settings, the agent can start one when a job splits into parallel parts")
		return
	}
	switch strings.TrimSpace(args) {
	case "lead":
		t.goToLead(team)
	case "stop":
		t.stopTeam(team.TeamID)
	default:
		t.pickTeamMember(team.TeamID, here)
	}
}

func (t *repl) leadCmd() { t.teamCmd("lead") }

func (t *repl) goToLead(team teams.Membership) {
	if team.Role == teams.RoleLead {
		t.dim("this is the lead — /team lists its threads")
		return
	}
	t.goToSession(team.LeadID)
}

func (t *repl) stopTeam(teamID string) {
	teams.Stop(teamID, teams.StopUser)
	th := t.app.Theme()
	t.do(func() {
		t.app.Print(th.Fg(tui.SlotWarning, "team stopped") + th.Fg(tui.SlotDim, " — every thread and its shells were stopped; the lead can start new ones"))
	})
}

func (t *repl) pickTeamMember(teamID, here string) {
	snap, ok := teams.TeamSnapshot(teamID)
	if !ok {
		return
	}
	items := []tui.Item{t.teamRow(snap.Lead, here)}
	working := 0
	for _, m := range snap.Members {
		items = append(items, t.teamRow(m, here))
		if m.State == teams.StateRunning || m.State == teams.StateStarting {
			working++
		}
	}
	if !snap.Stopped {
		items = append(items, tui.Item{Value: stopTeamValue, Label: t.app.Theme().Fg(tui.SlotDim, "■ Stop the team"), Description: "cancel every running thread"})
	}
	go func() {
		pick := t.app.Select(teamTitle(snap, working), items, 0, "")
		switch {
		case pick == nil:
		case pick.Value == stopTeamValue:
			t.stopTeam(teamID)
		case pick.Value != here:
			t.goToSession(pick.Value)
		}
	}()
}

func teamTitle(snap teams.Snapshot, working int) string {
	n := len(snap.Members)
	title := fmt.Sprintf("Team · %d thread", n)
	if n != 1 {
		title += "s"
	}
	if working > 0 {
		title += fmt.Sprintf(" · %d working", working)
	}
	if snap.OverBudget {
		title += " · stopped at its budget"
	}
	return title + " · " + teamUSD(snap.Cost.USD)
}

func (t *repl) teamRow(m teams.SnapshotMember, here string) tui.Item {
	th := t.app.Theme()
	state := m.State
	if m.Role == teams.RoleLead && m.Running {
		state = teams.StateRunning
	}
	label := m.Title
	if m.Role == teams.RoleLead {
		label += " " + th.Fg(tui.SlotDim, "(lead)")
	}
	what := string(state)
	if m.Activity != "" && (state == teams.StateRunning || state == teams.StateStarting) {
		what = m.Activity
	}
	if m.ID == here {
		what = "here · " + what
	}
	return tui.Item{Value: m.ID, Label: th.Fg(stateSlot(state), stateGlyph[state]) + " " + label, Description: what + " · " + teamUSD(m.USD)}
}

// goToSession brings a team session to the front, opening it in the
// background first when no slot holds it.
func (t *repl) goToSession(sessionID string) {
	rt := t.teamHost
	if rt == nil {
		rt = &tuiTeamRuntime{t: t}
	}
	s, err := rt.memberSlot(sessionID)
	if err != nil {
		t.fail("could not open that thread: %s", err)
		return
	}
	t.switchTo(s)
}
