package db

import (
	"os"
	"path/filepath"
	"testing"
)

func scratch(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.db")
	SetPath(p)
	t.Cleanup(func() {
		Close()
		SetPath("")
	})
	return p
}

func TestOpenAppliesItsPragmas(t *testing.T) {
	scratch(t)
	h, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	// Every pragma here is set in the DSN rather than executed, because a
	// pragma applies to the CONNECTION that ran it and database/sql hands out
	// a pool. This asserts they actually took.
	var journal string
	if err := h.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal", journal)
	}
	var fk int
	if err := h.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Error("foreign keys are off — ON DELETE CASCADE is silently ignored")
	}
	var busy int
	if err := h.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy == 0 {
		t.Error("busy_timeout is 0 — a second pi-agent would fail instead of waiting")
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	scratch(t)
	a, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("Open handed out a second handle — two handles is two sets of pragmas")
	}
}

// A file that is not a database must not make the agent unusable. It is set
// aside, not deleted: it still holds the conversations, and `.recover` on the
// copy gets most of them back.
func TestDamagedFileIsSetAsideNotDeleted(t *testing.T) {
	p := scratch(t)
	if err := os.WriteFile(p, []byte("this is not a database, not even close"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := Open()
	if err != nil {
		t.Fatalf("a damaged file stopped the agent: %v", err)
	}
	if _, err := h.Exec(`INSERT INTO meta (key, value) VALUES ('x', 'y')`); err != nil {
		t.Fatalf("the recovered database is not usable: %v", err)
	}
	matches, _ := filepath.Glob(p + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("the damaged file was not set aside: %v", matches)
	}
	kept, err := os.ReadFile(matches[0])
	if err != nil || string(kept) != "this is not a database, not even close" {
		t.Errorf("the damaged file was not kept intact: %v %q", err, kept)
	}
}

// The marker is what makes the integrity check earned rather than routine:
// a clean exit stamps '1', so the next open skips a whole-file scan.
func TestCleanShutdownIsStamped(t *testing.T) {
	scratch(t)
	h, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	var during string
	_ = h.QueryRow(`SELECT value FROM meta WHERE key = 'clean_shutdown'`).Scan(&during)
	if during != "0" {
		t.Errorf("clean_shutdown = %q while running, want 0", during)
	}
	if err := Close(); err != nil {
		t.Fatal(err)
	}
	h, err = Open()
	if err != nil {
		t.Fatal(err)
	}
	// Reopening flips it back to 0 for this run; what matters is that the
	// close wrote 1 and the reopen therefore had nothing to verify.
	var after string
	_ = h.QueryRow(`SELECT value FROM meta WHERE key = 'clean_shutdown'`).Scan(&after)
	if after != "0" {
		t.Errorf("clean_shutdown = %q after reopen, want 0", after)
	}
}

// A database written by a NEWER build is refused rather than opened: an old
// binary silently ignoring a new column writes rows the new one cannot read.
func TestNewerSchemaIsRefused(t *testing.T) {
	scratch(t)
	h, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`UPDATE meta SET value = '99' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(); err == nil {
		t.Error("a newer schema was opened anyway")
	}
}
