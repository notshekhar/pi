package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/core/db"
)

// writeLegacy fabricates a file-era session: the two files a pre-database
// install has on disk.
func writeLegacy(t *testing.T, id string, meta Meta, messages ...provider.Message) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".pi-agent", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta.ID = id
	if meta.Created.IsZero() {
		meta.Created = time.Now()
	}
	if meta.Updated.IsZero() {
		meta.Updated = meta.Created
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".meta.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := JSONL(messages)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The whole point of the import: an install that has been in use keeps its
// conversations when the storage changes underneath it.
func TestImportBringsFileEraSessionsIn(t *testing.T) {
	withSessionHome(t)
	writeLegacy(t, "20260101T120000.000",
		Meta{Title: "old work", Model: "kimi/k3", CWD: "/repo", InputTokens: 10, OutputTokens: 5, CostUSD: 0.25},
		user("do the old thing"), assistant("done"))

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("imported %d sessions, want 1", len(list))
	}
	got := list[0]
	if got.Title != "old work" || got.Model != "kimi/k3" || got.CWD != "/repo" {
		t.Errorf("metadata lost in the import: %+v", got)
	}
	if got.Messages != 2 {
		t.Errorf("imported %d messages, want 2", got.Messages)
	}
	// Usage came across too, or a history's spend silently resets to zero.
	if got.InputTokens != 10 || got.OutputTokens != 5 || got.CostUSD != 0.25 {
		t.Errorf("usage lost in the import: %+v", got)
	}
	loaded, err := Load(got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("loaded %d messages, want 2", len(loaded.Messages))
	}
	if _, ok := loaded.Messages[0].(provider.UserMessage); !ok {
		t.Errorf("imported message is not the user turn: %T", loaded.Messages[0])
	}
}

// It must run once. A repeat would duplicate every conversation the user has.
func TestImportRunsOnce(t *testing.T) {
	withSessionHome(t)
	writeLegacy(t, "20260101T120000.000", Meta{Title: "old work"}, user("hello"))

	if _, err := List(); err != nil {
		t.Fatal(err)
	}
	// Force the second consideration a new process would make.
	h, err := db.Open()
	if err != nil {
		t.Fatal(err)
	}
	importedFor = nil
	importLegacy(h)

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("the import ran twice: %d sessions", len(list))
	}
	if list[0].Messages != 1 {
		t.Errorf("messages duplicated: %d", list[0].Messages)
	}
}

// One session damaged by an old crash must not cost the user the other
// hundred — the rule the file-era listing had, kept where it now matters.
func TestImportSkipsUnreadableSessions(t *testing.T) {
	withSessionHome(t)
	writeLegacy(t, "20260101T120000.000", Meta{Title: "good"}, user("fine"))
	home, _ := os.UserHomeDir()
	broken := filepath.Join(home, ".pi-agent", "sessions", "broken.meta.json")
	if err := os.WriteFile(broken, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	list, err := List()
	if err != nil {
		t.Fatalf("a broken file failed the whole import: %v", err)
	}
	if len(list) != 1 || list[0].Title != "good" {
		t.Errorf("expected only the good session, got %+v", list)
	}
}

// A crash mid-append leaves a torn final line in the old body. The rest of
// that conversation is good and must come across.
func TestImportDropsATornFinalLine(t *testing.T) {
	withSessionHome(t)
	path := writeLegacy(t, "20260101T120000.000", Meta{Title: "torn"}, user("one"), assistant("two"))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"role":"user","content":[{"type":"te`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("the torn line failed the import: %d sessions", len(list))
	}
	if list[0].Messages != 2 {
		t.Errorf("imported %d messages, want the 2 intact ones", list[0].Messages)
	}
}

// The files are left where they are. They are the only copy until the import
// has been proven on real data.
func TestImportLeavesTheFilesAlone(t *testing.T) {
	withSessionHome(t)
	path := writeLegacy(t, "20260101T120000.000", Meta{Title: "old"}, user("hello"))
	if _, err := List(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the import moved or deleted the original: %v", err)
	}
}

// A file-era session's spend must still be findable in the buckets. The old
// format had no per-turn record, so the import stands one row in for the
// session's total — without it, everything that happened before the database
// reads $0 and the migration looks like it lost the money.
func TestImportSeedsTheCostLedger(t *testing.T) {
	withSessionHome(t)
	writeLegacy(t, "20260101T120000.000", Meta{
		Title: "old work", Model: "kimi/k3", CWD: "/repo",
		InputTokens: 100, OutputTokens: 50, CostUSD: 1.25,
		Created: time.Now(), Updated: time.Now(),
	}, user("hello"))

	b, err := SpendBuckets("/repo")
	if err != nil {
		t.Fatal(err)
	}
	if b.Lifetime != 1.25 {
		t.Errorf("lifetime = %v, want 1.25", b.Lifetime)
	}
	if b.Today != 1.25 {
		t.Errorf("today = %v, want 1.25", b.Today)
	}
	if b.Directory != 1.25 {
		t.Errorf("directory = %v, want 1.25", b.Directory)
	}
	if b.ByProvider["kimi"] != 1.25 {
		t.Errorf("provider split lost it: %+v", b.ByProvider)
	}
}
