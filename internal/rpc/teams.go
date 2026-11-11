package rpc

import (
	"errors"

	"github.com/notshekhar/pi/internal/core/teams"
)

// Thread teams over RPC (loop's rpc/server.ts): the panel's data, its Stop
// button, and — unless the server is embedded in a TUI whose sessions they
// are — the host that runs a team's member turns.

// pendingTeamTurn is a team turn queued behind one already running; the
// team only does that in a race, as mail to a busy thread waits in its
// inbox.
type pendingTeamTurn struct {
	input string
	meta  teams.TurnMeta
}

// claimTeams makes this server the team host and tells every client when a
// team moves. The returned func undoes both.
func (s *Server) claimTeams() func() {
	claimed := s.host.Live == nil && teams.ClaimRuntime(s, rpcTeamRuntime{s})
	stopListening := teams.OnChange(func(c teams.Change) {
		s.announce(SessionStatus{SessionID: c.SessionID, Change: "team", TeamID: c.TeamID, TeamChange: string(c.Kind)})
	})
	return func() {
		stopListening()
		if claimed {
			teams.ReleaseRuntime(s)
		}
	}
}

type rpcTeamRuntime struct{ s *Server }

func (rt rpcTeamRuntime) Deliver(sessionID, input string, meta teams.TurnMeta) error {
	c, err := rt.s.ensureCtx(sessionID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		c.pendingTeam = append(c.pendingTeam, pendingTeamTurn{input: input, meta: meta})
		return nil
	}
	return rt.s.runTeamTurn(sessionID, c, pendingTeamTurn{input: input, meta: meta})
}

func (rt rpcTeamRuntime) IsRunning(sessionID string) bool {
	c := rt.s.lookup(sessionID)
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

func (rt rpcTeamRuntime) Cancel(sessionID string) { rt.s.CancelOwnTurn(sessionID) }

// runTeamTurn runs a team turn the way the lead started the thread — its
// agent and thinking level, not whatever a client last chose. One whose team
// stopped since it was queued is turned down, its mail back in the inbox.
// Called with c.mu held.
func (s *Server) runTeamTurn(id string, c *activeSession, turn pendingTeamTurn) error {
	if !teams.MayRunTurn(id) {
		teams.DropTurn(turn.meta)
		return nil
	}
	rc, _ := teams.MemberRunConfig(id)
	agentName := rc.Agent
	if agentName == "" {
		agentName = turn.meta.Agent
	}
	meta := turn.meta
	return s.startTurn(id, c, turnRequest{input: turn.input, modelID: c.modelID, thinking: rc.Thinking, agent: agentName, team: &meta})
}

// nextTeamTurn starts the team turn queued behind the one that just ended.
// Called with c.mu held.
func (s *Server) nextTeamTurn(id string, c *activeSession) {
	if len(c.pendingTeam) == 0 {
		return
	}
	next := c.pendingTeam[0]
	c.pendingTeam = c.pendingTeam[1:]
	if err := s.runTeamTurn(id, c, next); err != nil {
		teams.DropTurn(next.meta)
	}
}

func (s *Server) teamHandlers() map[string]handler {
	return map[string]handler{
		// By team or by any thread in it — a member's view asks with its own
		// session id and gets the whole team back.
		"team.get": func(p map[string]any, _ Transport) (any, error) {
			if id := pstr(p, "teamId"); id != "" {
				return snapshotOrNil(teams.TeamSnapshot(id)), nil
			}
			if id := pstr(p, "sessionId"); id != "" {
				return snapshotOrNil(teams.SnapshotForSession(id)), nil
			}
			return nil, errors.New("team.get needs a teamId or a sessionId")
		},
		"team.stop": func(p map[string]any, _ Transport) (any, error) {
			teamID := pstr(p, "teamId")
			if teamID == "" {
				if m, ok := teams.TeamOf(pstr(p, "sessionId")); ok {
					teamID = m.TeamID
				}
			}
			if teamID == "" {
				return nil, errors.New("team.stop needs a teamId or a sessionId in a team")
			}
			teams.Stop(teamID, teams.StopUser)
			return map[string]any{"ok": true, "teamId": teamID}, nil
		},
	}
}

func snapshotOrNil(snap teams.Snapshot, ok bool) any {
	if !ok {
		return nil
	}
	return snap
}

// teamField is where a session sits in a team, for a list to nest members
// under their lead; nil when it is in none.
func teamField(sessionID string) any {
	m, ok := teams.TeamOf(sessionID)
	if !ok {
		return nil
	}
	return map[string]any{"id": m.TeamID, "role": m.Role, "leadId": m.LeadID, "state": m.State}
}
