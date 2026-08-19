// Package db is the one place that opens the agent's database.
//
// Everything durable and structured lives in a single WAL SQLite file at
// ~/.pi-agent/agent.db — sessions, their messages, and the cost ledger —
// which is the arrangement loop has. Nothing else in the codebase calls
// sql.Open; a second opener is a second set of pragmas, a second schema
// belief, and the first source of "it works in one command and not the
// other".
//
// SQLite rather than the JSONL files this replaced. The files were chosen
// when this repo took no external dependencies at all, and they were honest
// about the trade: listing meant reading every header file, usage totals
// meant reading every header file, and any question that crossed sessions
// ("what did I spend today", "which sessions touched this repo") meant
// reading all of them and doing the arithmetic in Go. The queries are the
// point. What is NOT lost by moving: a turn still appends and nothing
// rewrites, and the conversation is still recoverable as JSONL on demand
// (see session.JSONL) — the export path did not depend on the storage
// format, only on the codec.
//
// The driver is cgo (mattn/go-sqlite3). That is the cost, and it is a real
// one: a C toolchain is now needed to BUILD, and cross-compiling a release
// needs a cross-compiler. Everything here goes through database/sql, so
// swapping to a pure-Go driver (modernc.org/sqlite) is one import line if
// that day comes.
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// FileName is the database's name inside the config directory. Product-free,
// so a rename moves the directory and leaves this alone.
const FileName = "agent.db"

// schemaVersion is stamped in `meta`. A file from a NEWER build is refused
// rather than opened: the alternative is a new column silently ignored by an
// old binary, which writes rows the new one cannot interpret.
const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    pub_id        TEXT NOT NULL UNIQUE,
    title         TEXT,
    name          TEXT,
    model         TEXT NOT NULL DEFAULT '',
    cwd           TEXT NOT NULL DEFAULT '',
    parent_pub    TEXT,
    forked_at     INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd      REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_cwd ON sessions(cwd, updated_at DESC);

CREATE TABLE IF NOT EXISTS entries (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq        INTEGER NOT NULL,
    ts         INTEGER NOT NULL,
    role       TEXT NOT NULL DEFAULT '',
    payload    TEXT NOT NULL,
    UNIQUE (session_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_entries_session ON entries(session_id, seq);

CREATE TABLE IF NOT EXISTS cost_ledger (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    ts            INTEGER NOT NULL,
    day           TEXT NOT NULL,
    session_pub   TEXT,
    cwd           TEXT,
    model         TEXT NOT NULL DEFAULT '',
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    usd           REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_ledger_day ON cost_ledger(day);
CREATE INDEX IF NOT EXISTS idx_ledger_session ON cost_ledger(session_pub);
`

var (
	mu     sync.Mutex
	handle *sql.DB
	path   string
)

// SetPath points the database somewhere else and drops any open handle. For
// tests, and for a caller that has to run against a scratch database.
func SetPath(p string) {
	mu.Lock()
	defer mu.Unlock()
	if handle != nil {
		handle.Close()
		handle = nil
	}
	path = p
}

// DefaultPath is ~/.pi-agent/agent.db.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi-agent", FileName), nil
}

// Path is where the database lives.
func Path() (string, error) {
	mu.Lock()
	p := path
	mu.Unlock()
	if p != "" {
		return p, nil
	}
	return DefaultPath()
}

// Open returns the shared handle, opening it on first use.
func Open() (*sql.DB, error) {
	mu.Lock()
	defer mu.Unlock()
	if handle != nil {
		return handle, nil
	}
	// Not Path(): it takes the same lock this call already holds.
	p := path
	if p == "" {
		var err error
		if p, err = DefaultPath(); err != nil {
			return nil, err
		}
	}
	h, err := open(p, false)
	if err != nil {
		return nil, err
	}
	handle = h
	return handle, nil
}

// Close stamps a clean shutdown and closes the handle. Safe to call twice.
func Close() error {
	mu.Lock()
	defer mu.Unlock()
	if handle == nil {
		return nil
	}
	// The marker is what makes the integrity check on the next open cheap:
	// only an unclean exit leaves '0' behind and earns a scan.
	_, _ = handle.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES ('clean_shutdown', '1')")
	err := handle.Close()
	handle = nil
	return err
}

// open is the whole opening sequence — connect, pragmas, schema, version,
// integrity — as one retried unit.
//
// Retried because the FIRST-ever switch to WAL takes a lock the busy timeout
// does not reliably cover: two processes creating the file at the same
// instant can both fail. On an existing WAL file no retry is ever needed.
func open(p string, recovered bool) (*sql.DB, error) {
	if p != ":memory:" {
		// SQLite will create the file but not its parent — a fresh install or
		// a scratch HOME has neither.
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return nil, err
		}
	}
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		h, err := connect(p)
		if err == nil {
			err = prepare(h)
			if err == nil {
				return h, nil
			}
			h.Close()
		}
		lastErr = err
		// Heavier damage than quick_check can reach surfaces here: the schema
		// exec itself throws on a file that is not a database.
		if !recovered && p != ":memory:" && damaged(err) {
			return recoverDamaged(p)
		}
		// Only contention is worth retrying. Everything else — a version from
		// the future, a permission problem — fails the same way twenty times
		// and turns a clear error into a slow one.
		if !contended(err) {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return nil, fmt.Errorf("db: cannot open %s: %w", p, lastErr)
}

// connect opens the pool with its pragmas IN THE DSN.
//
// In the DSN and not as `db.Exec("PRAGMA …")`, which is the trap this file
// exists to contain: a pragma applies to the CONNECTION that ran it, and
// database/sql hands out a pool of them. Set that way, the first query on a
// second connection runs with foreign keys off and a zero busy timeout.
//
// One connection for the same reason from the other side: a CLI's writers and
// readers are goroutines of one process, and serialising them here turns
// every SQLITE_BUSY into a wait on a mutex we control. The busy timeout is
// then only about OTHER processes — a second pi-agent in another terminal.
func connect(p string) (*sql.DB, error) {
	dsn := "file:" + p + "?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=on"
	if p == ":memory:" {
		dsn = "file::memory:?cache=shared&_foreign_keys=on"
	}
	h, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	h.SetMaxOpenConns(1)
	if err := h.Ping(); err != nil {
		h.Close()
		return nil, err
	}
	return h, nil
}

// prepare applies the schema, checks the version, and verifies the file after
// an unclean exit.
func prepare(h *sql.DB) error {
	if _, err := h.Exec(schema); err != nil {
		return err
	}
	if _, err := h.Exec(
		"INSERT OR IGNORE INTO meta (key, value) VALUES ('schema_version', ?)", schemaVersion,
	); err != nil {
		return err
	}
	var version int
	if err := h.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&version); err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("db: schema v%d is newer than this build (v%d) — upgrade pi-agent", version, schemaVersion)
	}
	// Migrations land here as version < schemaVersion branches. There are
	// none yet, and the stamp is what makes the first one possible.

	// quick_check scans the whole file, so it is earned rather than routine:
	// Close stamps '1', open flips it to '0' for the run, and a crash
	// therefore leaves '0' behind and the next open verifies.
	var clean string
	_ = h.QueryRow("SELECT value FROM meta WHERE key = 'clean_shutdown'").Scan(&clean)
	if clean != "1" {
		var result string
		if err := h.QueryRow("PRAGMA quick_check").Scan(&result); err != nil {
			return err
		}
		if result != "ok" {
			return fmt.Errorf("db: quick_check failed: %s", result)
		}
	}
	_, err := h.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES ('clean_shutdown', '0')")
	return err
}

// contended reports whether an error is another connection holding the lock —
// the one case where trying again is the answer.
func contended(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "locked") || strings.Contains(msg, "busy")
}

// damaged reports whether an error means the FILE is broken rather than busy.
func damaged(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"malformed", "not a database", "corrupt", "quick_check"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// recover sets a damaged file aside and starts a fresh one, ONCE.
//
// Set aside rather than deleted: a corrupt database still holds the
// conversations, and `sqlite3 .recover` on the salvaged copy gets most of
// them back. Refusing to start instead would be the worse failure — the
// agent would be unusable until someone cleared the file by hand, which is
// the same outcome minus the ability to say what happened.
func recoverDamaged(p string) (*sql.DB, error) {
	aside := fmt.Sprintf("%s.corrupt-%s", p, time.Now().UTC().Format("20060102T150405"))
	if err := os.Rename(p, aside); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	// The WAL and shared-memory files belong to the file that just moved;
	// leaving them behind hands the fresh database someone else's journal.
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Rename(p+suffix, aside+suffix)
	}
	h, err := open(p, true)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "pi-agent: the session database was damaged; it was moved to %s and a new one started\n", aside)
	return h, nil
}
