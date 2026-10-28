// Package db is the one place that opens the agent's database — LOOP's
// database, ~/.loop/agent.db, which pi shares with loop.
//
// It is a port of loop's sessions/db.ts: the same schema text, the same
// version (7), the same migrations, the same open sequence. That sameness is
// the whole contract. Both programs open this file, so:
//
//   - pi NEVER stamps a version loop does not know. loop refuses a database
//     newer than it was built for, and so does pi.
//   - pi creates a fresh file exactly as loop would, so whichever program runs
//     first on a new machine leaves the other a database it understands.
//
// Nothing else in the codebase calls sql.Open; a second opener is a second set
// of pragmas and a second schema belief.
//
// The driver is cgo (mattn/go-sqlite3). Everything goes through database/sql,
// so swapping to a pure-Go driver is one import line.
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

	"github.com/notshekhar/pi/internal/core/home"
)

// FileName is the database's name inside the config directory. Product-free,
// so a rename moves the directory and leaves this alone.
const FileName = "agent.db"

// schemaVersion is loop's SCHEMA_VERSION. Bumping it here would lock loop out
// of its own database; it moves only when loop's does.
const schemaVersion = 7

// schema is loop's SCHEMA, verbatim (sessions/db.ts).
const schema = `
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    pub_id     TEXT NOT NULL UNIQUE,
    cwd        TEXT NOT NULL,
    provider   TEXT NOT NULL,
    model      TEXT NOT NULL,
    name       TEXT,
    parent_pub TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    archived_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_sessions_cwd ON sessions(cwd, updated_at DESC);

CREATE TABLE IF NOT EXISTS entries (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id    INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    pub_id        TEXT NOT NULL,
    parent_pub_id TEXT,
    ts            INTEGER NOT NULL,
    type          TEXT NOT NULL,
    role          TEXT,
    payload       TEXT NOT NULL,
    usage_input       INTEGER,
    usage_output      INTEGER,
    usage_total       INTEGER,
    usage_no_cache    INTEGER,
    usage_cache_read  INTEGER,
    usage_cache_write INTEGER,
    usage_text        INTEGER,
    usage_reasoning   INTEGER,
    usage_estimated   INTEGER,
    usage_usd         REAL,
    model             TEXT,
    UNIQUE (session_id, pub_id)
);
CREATE INDEX IF NOT EXISTS idx_entries_session ON entries(session_id, id);
CREATE INDEX IF NOT EXISTS idx_entries_usage ON entries(ts) WHERE usage_input IS NOT NULL;

CREATE TABLE IF NOT EXISTS cost_ledger (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ts           INTEGER NOT NULL,
    day          TEXT NOT NULL,
    session_id   INTEGER REFERENCES sessions(id) ON DELETE SET NULL,
    session_pub  TEXT,
    entry_id     INTEGER REFERENCES entries(id) ON DELETE SET NULL,
    entry_pub    TEXT,
    source       TEXT NOT NULL,
    cwd          TEXT,
    provider     TEXT NOT NULL,
    model        TEXT NOT NULL,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    no_cache_tokens    INTEGER,
    cache_read_tokens  INTEGER,
    cache_write_tokens INTEGER,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens   INTEGER,
    price_input       REAL,
    price_output      REAL,
    price_cache_read  REAL,
    price_cache_write REAL,
    usd           REAL NOT NULL,
    provider_cost REAL,
    estimated     INTEGER NOT NULL DEFAULT 0,
    backfilled    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_ledger_day ON cost_ledger(day) WHERE estimated = 0 AND backfilled = 0;
CREATE INDEX IF NOT EXISTS idx_ledger_cwd ON cost_ledger(cwd) WHERE estimated = 0 AND backfilled = 0;
CREATE INDEX IF NOT EXISTS idx_ledger_session ON cost_ledger(session_pub);

CREATE TABLE IF NOT EXISTS projects (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    dir             TEXT NOT NULL UNIQUE,
    trust           INTEGER,
    model           TEXT,
    provider_models TEXT,
    bash_allow      TEXT,
    updated_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS reminders (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    pub_id     TEXT NOT NULL UNIQUE,
    text       TEXT NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    kind       TEXT NOT NULL,
    at         INTEGER,
    expr       TEXT,
    created_at INTEGER NOT NULL
);

-- Thread teams (teams/store.ts). New tables only, never a column on an
-- existing one, and no schema_version bump: the desktop and the CLI share
-- this file and update apart, and a build that finds a newer version refuses
-- the whole database. An older build simply never looks at these.
CREATE TABLE IF NOT EXISTS teams (
    id         TEXT PRIMARY KEY,
    lead_pub   TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    stopped_at INTEGER
);
CREATE TABLE IF NOT EXISTS team_members (
    session_pub TEXT PRIMARY KEY,
    team_id     TEXT NOT NULL,
    role        TEXT NOT NULL,
    state       TEXT NOT NULL,
    activity    TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_team_members_team ON team_members(team_id);
CREATE TABLE IF NOT EXISTS team_messages (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    team_id      TEXT NOT NULL,
    from_pub     TEXT NOT NULL,
    to_pub       TEXT,
    kind         TEXT NOT NULL,
    board_key    TEXT,
    body         TEXT NOT NULL,
    ts           INTEGER NOT NULL,
    delivered_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_team_messages_inbox ON team_messages(to_pub, delivered_at);
CREATE INDEX IF NOT EXISTS idx_team_messages_team ON team_messages(team_id, kind);

CREATE TABLE IF NOT EXISTS goals (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    pub_id              TEXT NOT NULL UNIQUE,
    text                TEXT NOT NULL,
    cwd                 TEXT NOT NULL,
    model               TEXT,
    agent               TEXT,
    kind                TEXT NOT NULL,
    at                  INTEGER,
    expr                TEXT,
    enabled             INTEGER NOT NULL DEFAULT 1,
    created_at          INTEGER NOT NULL,
    last_run_at         INTEGER,
    last_run_session_id TEXT,
    last_run_status     TEXT,
    last_run_summary    TEXT
);
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

// DefaultPath is ~/.loop/agent.db.
func DefaultPath() (string, error) {
	return home.Path(FileName), nil
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
	// only an unclean exit leaves '0' behind and earns a scan. The checkpoint
	// keeps the -wal file from growing without bound (loop's closeDb).
	_, _ = handle.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES ('clean_shutdown', '1')")
	_, _ = handle.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
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
		return fmt.Errorf("session db is schema v%d, newer than this build (v%d) — upgrade pi", version, schemaVersion)
	}
	if version < schemaVersion {
		// loop's migrations, in loop's order. Two processes can race an
		// ALTER; the loser's duplicate-column error is the success case.
		var alters []string
		if version < 2 {
			alters = append(alters,
				"ALTER TABLE entries ADD COLUMN usage_usd REAL",
				"ALTER TABLE cost_ledger ADD COLUMN entry_id INTEGER REFERENCES entries(id) ON DELETE SET NULL",
				"ALTER TABLE cost_ledger ADD COLUMN entry_pub TEXT")
		}
		if version < 3 {
			alters = append(alters, "ALTER TABLE projects ADD COLUMN provider_models TEXT")
		}
		if version < 5 {
			alters = append(alters, "ALTER TABLE goals ADD COLUMN agent TEXT")
		}
		if version < 6 {
			alters = append(alters, "ALTER TABLE projects ADD COLUMN bash_allow TEXT")
		}
		if version < 7 {
			alters = append(alters, "ALTER TABLE sessions ADD COLUMN archived_at INTEGER")
		}
		for _, alter := range alters {
			if _, err := h.Exec(alter); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				return err
			}
		}
		if _, err := h.Exec("UPDATE meta SET value = ? WHERE key = 'schema_version'", schemaVersion); err != nil {
			return err
		}
	}

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
	salvage(aside, h)
	_, _ = h.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES ('recovered_at', ?)", fmt.Sprint(time.Now().UnixMilli()))
	fmt.Fprintf(os.Stderr, "pi: the session database was damaged; it was moved to %s and a new one started\n", aside)
	return h, nil
}

// salvage copies every readable row from the damaged file into the fresh one
// (loop's salvageCorruptDb). Parent tables first so foreign keys hold; a row
// whose parent was lost, or that cannot be read, is skipped — best effort,
// never fatal. The frozen pre-ledger cost baseline is carried over too, since
// nothing else holds it.
func salvage(damagedPath string, fresh *sql.DB) {
	old, err := sql.Open("sqlite3", "file:"+damagedPath+"?mode=ro")
	if err != nil {
		return
	}
	defer old.Close()
	for _, table := range []string{"sessions", "entries", "cost_ledger", "projects", "reminders", "goals"} {
		rows, err := old.Query("SELECT * FROM " + table)
		if err != nil {
			continue
		}
		cols, err := rows.Columns()
		if err != nil || len(cols) == 0 {
			rows.Close()
			continue
		}
		insert := "INSERT OR IGNORE INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" +
			strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + ")"
		tx, err := fresh.Begin()
		if err != nil {
			rows.Close()
			continue
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if rows.Scan(ptrs...) != nil {
				continue
			}
			_, _ = tx.Exec(insert, vals...)
		}
		rows.Close()
		_ = tx.Commit()
	}
	var baseline string
	if old.QueryRow("SELECT value FROM meta WHERE key = 'cost_baseline'").Scan(&baseline) == nil {
		_, _ = fresh.Exec("INSERT OR IGNORE INTO meta (key, value) VALUES ('cost_baseline', ?)", baseline)
	}
}
