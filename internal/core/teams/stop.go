package teams

import (
	"time"

	"github.com/notshekhar/pi/internal/core/shells"
)

// Move moves a member to state (activity as in SetState) and, when that
// changed anything, tells the panels and anyone waiting on the team.
func Move(sessionID string, state State, activity string) {
	team, ok := TeamOf(sessionID)
	if !ok {
		return
	}
	if SetState(sessionID, state, activity) {
		Emit(Change{TeamID: team.TeamID, Kind: ChangeMembers, SessionID: sessionID})
		Nudge("")
	}
}

// StopReason is why a team was stopped.
type StopReason string

const (
	StopUser   StopReason = "user"
	StopBudget StopReason = "budget"
)

var stopNotices = map[StopReason]string{
	StopUser:   "The user stopped the team: every thread was cancelled. Do not restart them unless the user asks.",
	StopBudget: "The team reached its budget (threadTeamsBudgetUsd) and was stopped: every thread was cancelled. Finish the job yourself, or ask the user to raise the budget.",
}

// Stop stops a whole team and tells its lead why. The note waits in the
// lead's inbox: a stop never starts a turn of its own.
func Stop(teamID string, reason StopReason) {
	alreadyStopped := IsStopped(teamID)
	if lead, ok := leadOf(teamID); ok && !alreadyStopped {
		_, _ = PostMessages(teamID, lead.SessionID, []string{lead.SessionID}, KindUpdate, stopNotices[reason])
	}
	halt(teamID)
	if reason == StopBudget {
		Emit(Change{TeamID: teamID, Kind: ChangeBudget})
	}
}

// halt stops every thread: marked stopped first — so a cancelled turn
// ending finds "stopped" and leaves it, not "idle", which would read as a
// thread that could still be woken — then its turn cancelled and its shells
// killed. The lead is not a thread: its turn carries on, as the one left to
// finish the job.
func halt(teamID string) {
	_ = MarkStopped(teamID, true)
	roster, _ := ListTeam(teamID)
	var threads []Member
	for _, m := range roster {
		if m.Role == RoleMember {
			threads = append(threads, m)
			Move(m.SessionID, StateStopped, "")
		}
	}
	r := runtime()
	for _, m := range threads {
		if r != nil && r.IsRunning(m.SessionID) {
			r.Cancel(m.SessionID)
		}
		shells.KillSession(m.SessionID)
	}
	// A lead inside wait_for_team returns now, with the stop.
	Nudge("")
}

// Leave is a session about to be deleted leaving its team. A lead's team
// goes with it, so its threads are stopped first — nobody would be left to
// read their reports. The rows themselves go with the session's delete.
func Leave(sessionID string) {
	team, ok := TeamOf(sessionID)
	if !ok {
		return
	}
	if team.Role == RoleLead {
		halt(team.TeamID)
	}
	Emit(Change{TeamID: team.TeamID, Kind: ChangeMembers, SessionID: sessionID})
	Nudge("")
}

func leadOf(teamID string) (Member, bool) {
	roster, _ := ListTeam(teamID)
	for _, m := range roster {
		if m.Role == RoleLead {
			return m, true
		}
	}
	return Member{}, false
}

// SnapshotMember is one member as every client's panel draws it.
type SnapshotMember struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Role     Role   `json:"role"`
	State    State  `json:"state"`
	Activity string `json:"activity,omitempty"`
	// Running is whether a turn is going in it right now, in this process.
	Running bool    `json:"running"`
	USD     float64 `json:"usd"`
	// TeamUSD is, of USD, what turns the team started cost.
	TeamUSD      float64 `json:"teamUsd"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
}

// SnapshotNote is a board note with its author's title.
type SnapshotNote struct {
	Key  string `json:"key"`
	Body string `json:"body"`
	From Peer   `json:"from"`
	TS   int64  `json:"ts"`
}

// SnapshotCost is the team's total spend.
type SnapshotCost struct {
	USD          float64 `json:"usd"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	Estimated    bool    `json:"estimated"`
}

// Snapshot is a team as every client's panel draws it (RPC team.get).
type Snapshot struct {
	TeamID  string `json:"teamId"`
	Stopped bool   `json:"stopped"`
	// OverBudget is set when the team stopped for its budget.
	OverBudget bool             `json:"overBudget"`
	Lead       SnapshotMember   `json:"lead"`
	Members    []SnapshotMember `json:"members"`
	Board      []SnapshotNote   `json:"board"`
	Cost       SnapshotCost     `json:"cost"`
}

// TeamSnapshot is the team as it stands; ok is false when it has no lead.
func TeamSnapshot(teamID string) (Snapshot, bool) {
	roster, err := ListTeam(teamID)
	if err != nil {
		return Snapshot{}, false
	}
	cost, _ := TeamCost(teamID)
	stopped := IsStopped(teamID)
	snap := Snapshot{TeamID: teamID, Stopped: stopped, OverBudget: stopped && IsOverBudget(teamID),
		Members: []SnapshotMember{}, Board: []SnapshotNote{},
		Cost: SnapshotCost{USD: cost.USD, InputTokens: cost.InputTokens, OutputTokens: cost.OutputTokens, Estimated: cost.Estimated}}
	hasLead := false
	for _, m := range roster {
		view := snapshotMember(m, cost.BySession[m.SessionID])
		if m.Role == RoleLead {
			snap.Lead, hasLead = view, true
		} else {
			snap.Members = append(snap.Members, view)
		}
	}
	board, _ := ReadBoard(teamID)
	for _, n := range board {
		snap.Board = append(snap.Board, SnapshotNote{Key: n.Key, Body: n.Body, From: Peer{ID: n.From, Title: SessionTitle(n.From)}, TS: n.TS})
	}
	return snap, hasLead
}

// SnapshotForSession is the team a session is in; the lead's or any
// member's id works.
func SnapshotForSession(sessionID string) (Snapshot, bool) {
	team, ok := TeamOf(sessionID)
	if !ok {
		return Snapshot{}, false
	}
	return TeamSnapshot(team.TeamID)
}

func snapshotMember(m Member, share CostShare) SnapshotMember {
	r := runtime()
	running := (r != nil && r.IsRunning(m.SessionID)) || IsTurnActive(m.SessionID)
	// A turn here keeps its row alive with a heartbeat, so only a row
	// nothing anywhere is running goes stale.
	state := m.State
	if !running {
		state = m.Effective(time.Now())
	}
	title := m.Title
	if m.Role == RoleLead {
		title = SessionTitle(m.SessionID)
	}
	view := SnapshotMember{ID: m.SessionID, Title: title, Role: m.Role, State: state, Running: running,
		USD: share.USD, TeamUSD: share.TeamUSD, InputTokens: share.InputTokens, OutputTokens: share.OutputTokens}
	if state == m.State {
		view.Activity = m.Activity
	}
	return view
}
