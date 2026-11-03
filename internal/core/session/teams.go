package session

import (
	"database/sql"
	"errors"
	"time"
)

// loop's thread teams (teams/store.ts) live in the same database. pi never
// runs a team, but it deletes sessions, and a deleted session must leave its
// team exactly as loop's forgetTeamSession leaves it — otherwise loop keeps a
// member it can never open, or a team whose lead is gone.

// forgetTeamRows takes a session being deleted out of its team. A member
// leaves with its unread mail and its run settings. A lead takes the team
// with it: the team is stopped, every member leaves and the unread mail
// goes, but the board notes stay.
func forgetTeamRows(tx *sql.Tx, pubID string) error {
	var teamID, role string
	err := tx.QueryRow("SELECT team_id, role FROM team_members WHERE session_pub = ?", pubID).Scan(&teamID, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if role == "lead" {
		return dissolveTeam(tx, teamID)
	}
	return leaveTeam(tx, pubID)
}

func dissolveTeam(tx *sql.Tx, teamID string) error {
	return execAll(tx,
		stmt{"UPDATE teams SET stopped_at = ? WHERE id = ?", []any{time.Now().UnixMilli(), teamID}},
		stmt{"DELETE FROM team_member_runs WHERE session_pub IN (SELECT session_pub FROM team_members WHERE team_id = ?)", []any{teamID}},
		stmt{"DELETE FROM team_members WHERE team_id = ?", []any{teamID}},
		stmt{"DELETE FROM team_messages WHERE team_id = ? AND kind != 'board' AND delivered_at IS NULL", []any{teamID}},
	)
}

func leaveTeam(tx *sql.Tx, pubID string) error {
	return execAll(tx,
		stmt{"DELETE FROM team_member_runs WHERE session_pub = ?", []any{pubID}},
		stmt{"DELETE FROM team_members WHERE session_pub = ?", []any{pubID}},
		stmt{"DELETE FROM team_messages WHERE to_pub = ? AND delivered_at IS NULL", []any{pubID}},
	)
}

type stmt struct {
	query string
	args  []any
}

func execAll(tx *sql.Tx, stmts ...stmt) error {
	for _, s := range stmts {
		if _, err := tx.Exec(s.query, s.args...); err != nil {
			return err
		}
	}
	return nil
}
