package session

import (
	"testing"

	"github.com/notshekhar/pi/internal/core/db"
)

// A saved session, so Delete has a row to remove.
func savedSession(t *testing.T) string {
	t.Helper()
	s := New("xai/grok-4.7", "/repo")
	if err := s.Add(user("hi")); err != nil {
		t.Fatal(err)
	}
	return s.ID
}

// A team the way loop writes one: the lead, two members, unread mail for
// each member, a run row for each and one board note.
func seedTeam(t *testing.T, lead string, members ...string) {
	t.Helper()
	h, err := db.Open()
	if err != nil {
		t.Fatal(err)
	}
	run := func(q string, args ...any) {
		if _, err := h.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	run("INSERT INTO teams (id, lead_pub, created_at) VALUES ('tm_1', ?, 1)", lead)
	run("INSERT INTO team_members (session_pub, team_id, role, state, created_at, updated_at) VALUES (?, 'tm_1', 'lead', 'idle', 1, 1)", lead)
	for _, m := range members {
		run("INSERT INTO team_members (session_pub, team_id, role, state, created_at, updated_at) VALUES (?, 'tm_1', 'member', 'running', 1, 1)", m)
		run("INSERT INTO team_member_runs (session_pub, agent, created_at) VALUES (?, 'default', 1)", m)
		run("INSERT INTO team_messages (team_id, from_pub, to_pub, kind, body, ts) VALUES ('tm_1', ?, ?, 'message', 'hi', 1)", lead, m)
	}
	run("INSERT INTO team_messages (team_id, from_pub, to_pub, kind, board_key, body, ts) VALUES ('tm_1', ?, NULL, 'board', 'contract', 'GET /x', 1)", lead)
}

func count(t *testing.T, q string, args ...any) int {
	t.Helper()
	h, err := db.Open()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := h.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func deleteOK(t *testing.T, id string) {
	t.Helper()
	ok, err := Delete(id)
	if err != nil || !ok {
		t.Fatalf("Delete(%s) = %v, %v", id, ok, err)
	}
}

// Deleting a thread takes it out of its team, with its unread mail and run
// settings; the rest of the team is untouched.
func TestDeletingAMemberLeavesItsTeam(t *testing.T) {
	withSessionHome(t)
	lead, a, b := savedSession(t), savedSession(t), savedSession(t)
	seedTeam(t, lead, a, b)
	deleteOK(t, a)
	if n := count(t, "SELECT COUNT(*) FROM team_members WHERE session_pub = ?", a); n != 0 {
		t.Errorf("deleted member still in its team")
	}
	if n := count(t, "SELECT COUNT(*) FROM team_messages WHERE to_pub = ? AND delivered_at IS NULL", a); n != 0 {
		t.Errorf("deleted member still has %d unread", n)
	}
	if n := count(t, "SELECT COUNT(*) FROM team_member_runs WHERE session_pub = ?", a); n != 0 {
		t.Errorf("deleted member's run settings kept")
	}
	if n := count(t, "SELECT COUNT(*) FROM team_members WHERE team_id = 'tm_1'"); n != 2 {
		t.Errorf("team has %d rows, want the lead and the other member", n)
	}
	if n := count(t, "SELECT COUNT(*) FROM teams WHERE id = 'tm_1' AND stopped_at IS NULL"); n != 1 {
		t.Errorf("a member leaving must not stop the team")
	}
}

// Deleting the lead dissolves the team as loop does: stopped, no members, no
// unread mail — but the board stays.
func TestDeletingTheLeadDissolvesItsTeam(t *testing.T) {
	withSessionHome(t)
	lead, a, b := savedSession(t), savedSession(t), savedSession(t)
	seedTeam(t, lead, a, b)
	deleteOK(t, lead)
	if n := count(t, "SELECT COUNT(*) FROM teams WHERE id = 'tm_1' AND stopped_at IS NOT NULL"); n != 1 {
		t.Errorf("team not stopped")
	}
	if n := count(t, "SELECT COUNT(*) FROM team_members WHERE team_id = 'tm_1'"); n != 0 {
		t.Errorf("%d members left in a dissolved team", n)
	}
	if n := count(t, "SELECT COUNT(*) FROM team_member_runs"); n != 0 {
		t.Errorf("%d run rows left", n)
	}
	if n := count(t, "SELECT COUNT(*) FROM team_messages WHERE kind != 'board' AND delivered_at IS NULL"); n != 0 {
		t.Errorf("%d unread left", n)
	}
	if n := count(t, "SELECT COUNT(*) FROM team_messages WHERE kind = 'board'"); n != 1 {
		t.Errorf("the board must stay")
	}
}

// A session in no team deletes as before, and one that does not exist
// touches no team.
func TestDeletingOutsideATeam(t *testing.T) {
	withSessionHome(t)
	lead, a := savedSession(t), savedSession(t)
	seedTeam(t, lead, a)
	loner := savedSession(t)
	deleteOK(t, loner)
	if ok, err := Delete("no-such-session"); ok || err != nil {
		t.Errorf("Delete(missing) = %v, %v", ok, err)
	}
	if n := count(t, "SELECT COUNT(*) FROM team_members WHERE team_id = 'tm_1'"); n != 2 {
		t.Errorf("team changed by deleting a session outside it: %d rows", n)
	}
}
