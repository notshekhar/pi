package session

import (
	"testing"
)

func noteTexts(t *testing.T, sessionID string) []string {
	t.Helper()
	notes, err := ListNotes(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	texts := make([]string, len(notes))
	for i, n := range notes {
		texts[i] = n.Text
	}
	return texts
}

func mustAddNote(t *testing.T, sessionID, text string) Note {
	t.Helper()
	n, err := AddNote(sessionID, text)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEachSessionKeepsItsOwnNotes(t *testing.T) {
	withSessionHome(t)
	mustAddNote(t, "a", "first")
	mustAddNote(t, "b", "other session")
	mustAddNote(t, "a", "second")
	if got := noteTexts(t, "a"); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("session a: %v", got)
	}
	if got := noteTexts(t, "b"); len(got) != 1 || got[0] != "other session" {
		t.Fatalf("session b: %v", got)
	}
}

func TestTakingANoteRemovesItOnce(t *testing.T) {
	withSessionHome(t)
	n := mustAddNote(t, "a", "send me")
	if _, ok, _ := TakeNote("b", n.ID); ok {
		t.Fatal("a note was taken from another session")
	}
	got, ok, err := TakeNote("a", n.ID)
	if err != nil || !ok || got.Text != "send me" {
		t.Fatalf("take: %v %v %v", got, ok, err)
	}
	if _, ok, _ := TakeNote("a", n.ID); ok {
		t.Fatal("a note was taken twice")
	}
}

func TestRemoveAllClearsOneSession(t *testing.T) {
	withSessionHome(t)
	mustAddNote(t, "a", "1")
	mustAddNote(t, "a", "2")
	mustAddNote(t, "b", "keep")
	if n, err := ClearNotes("a"); err != nil || n != 2 {
		t.Fatalf("cleared %d, %v", n, err)
	}
	if len(noteTexts(t, "a")) != 0 || len(noteTexts(t, "b")) != 1 {
		t.Fatal("remove all touched the wrong session")
	}
}

func TestDeletingASessionDeletesItsNotes(t *testing.T) {
	withSessionHome(t)
	id := savedSession(t)
	mustAddNote(t, id, "goes with it")
	if ok, err := Delete(id); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if got := noteTexts(t, id); len(got) != 0 {
		t.Fatalf("notes outlived their session: %v", got)
	}
}

func TestStartSavesAnEmptySession(t *testing.T) {
	withSessionHome(t)
	s := New("xai/grok-4.7", "/repo")
	if err := s.Start(); err != nil || !s.Saved() {
		t.Fatalf("start: saved=%v err=%v", s.Saved(), err)
	}
}
