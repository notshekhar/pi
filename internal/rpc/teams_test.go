package rpc

import (
	"testing"

	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/teams"
)

// teamFixture is a lead with one member, in a scratch home.
func teamFixture(t *testing.T) (lead, member *session.Session) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cwd := t.TempDir()
	var err error
	if lead, err = session.Create("gw/x", cwd); err != nil {
		t.Fatal(err)
	}
	if member, err = session.Create("gw/x", cwd); err != nil {
		t.Fatal(err)
	}
	if err := member.SetName("CSV export"); err != nil {
		t.Fatal(err)
	}
	teamID, err := teams.CreateTeam(lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := teams.AddMember(teamID, member.ID, teams.StateRunning); err != nil {
		t.Fatal(err)
	}
	return lead, member
}

// A member's id fetches the whole team; Stop stops its threads and is told
// to the lead.
func TestTeamGetAndStop(t *testing.T) {
	lead, member := teamFixture(t)
	s := NewServer(Host{})
	t.Cleanup(s.Dispose)

	got, err := s.Call("team.get", map[string]any{"sessionId": member.ID})
	if err != nil {
		t.Fatal(err)
	}
	snap, ok := got.(teams.Snapshot)
	if !ok || snap.Lead.ID != lead.ID || len(snap.Members) != 1 || snap.Members[0].Title != "CSV export" {
		t.Fatalf("team.get = %#v", got)
	}
	if got, _ := s.Call("team.get", map[string]any{"sessionId": "nobody"}); got != nil {
		t.Errorf("a session in no team got %#v", got)
	}

	if _, err := s.Call("team.stop", map[string]any{"sessionId": lead.ID}); err != nil {
		t.Fatal(err)
	}
	if m, _ := teams.TeamOf(member.ID); m.State != teams.StateStopped {
		t.Errorf("member state after stop = %s", m.State)
	}
	if teams.InboxCount(lead.ID) != 1 {
		t.Error("the lead was not told the team stopped")
	}
	if _, err := s.Call("team.stop", map[string]any{"sessionId": "nobody"}); err == nil {
		t.Error("stopping no team did not fail")
	}
}
