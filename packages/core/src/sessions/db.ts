import { Database } from "bun:sqlite";
import { mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { getLoopDir } from "../auth/storage";
import { debugLog } from "../debug";
import { migrateLegacySessions } from "./migrate";

/**
 * The one place that opens the session database. Everything session-adjacent
 * (transcripts, cost ledger, per-project trust/model, reminders) lives in this
 * single WAL SQLite file; nothing else in the codebase touches `new Database`.
 *
 * The path must come from getLoopDir(): inside a compiled binary
 * `import.meta.dir` is the read-only /$bunfs bundle and opening a DB there
 * fails with SQLITE_CANTOPEN.
 */

const SCHEMA_VERSION = 1;

const SCHEMA = `
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
    updated_at INTEGER NOT NULL
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
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    dir        TEXT NOT NULL UNIQUE,
    trust      INTEGER,
    model      TEXT,
    updated_at INTEGER NOT NULL
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
`;

let db: Database | null = null;
let overridePath: string | null = null;

function defaultDbPath(): string {
    return join(getLoopDir(), "loop.db");
}

/**
 * Open → pragmas → schema, as one retried unit. busy_timeout must be set
 * before the WAL switch (the switch itself takes a lock), and the first-ever
 * WAL switch on a fresh file can still return SQLITE_BUSY when two processes
 * create it simultaneously — busy_timeout does not reliably cover that, so
 * the whole init retries with backoff. On an existing WAL file no retry is
 * ever needed.
 */
function openDb(path: string): Database {
    // SQLite can create the file but not its parent (fresh install / temp HOME).
    if (path !== ":memory:") mkdirSync(dirname(path), { recursive: true });
    let lastErr: unknown;
    for (let attempt = 0; attempt < 20; attempt++) {
        let candidate: Database | null = null;
        try {
            candidate = new Database(path, { create: true });
            candidate.exec("PRAGMA busy_timeout = 5000");
            candidate.exec("PRAGMA journal_mode = WAL");
            candidate.exec("PRAGMA synchronous = NORMAL");
            candidate.exec("PRAGMA foreign_keys = ON");
            candidate.exec(SCHEMA);
            candidate.run("INSERT OR IGNORE INTO meta (key, value) VALUES ('schema_version', ?)", [
                String(SCHEMA_VERSION),
            ]);
            const row = candidate
                .query<{ value: string }, []>("SELECT value FROM meta WHERE key = 'schema_version'")
                .get();
            const version = Number(row?.value ?? SCHEMA_VERSION);
            if (version > SCHEMA_VERSION) {
                throw new Error(
                    `session db ${path} is schema v${version}, newer than this build (v${SCHEMA_VERSION}) — upgrade loop`,
                );
            }
            const check = candidate.query<{ quick_check: string }, []>("PRAGMA quick_check").get();
            if (check && check.quick_check !== "ok") {
                // Recovery (re-migrate from retained JSONL) is a later phase;
                // surface loudly but keep the app usable.
                debugLog("session-db", `quick_check failed for ${path}: ${check.quick_check}`);
            }
            return candidate;
        } catch (err) {
            candidate?.close();
            lastErr = err;
            const msg = String((err as Error)?.message ?? err);
            if (!/SQLITE_BUSY|database is locked/i.test(msg)) throw err;
            Bun.sleepSync(25);
        }
    }
    throw lastErr instanceof Error ? lastErr : new Error(`could not open session db at ${path}: ${lastErr}`);
}

export function getDb(): Database {
    if (!db) {
        db = openDb(overridePath ?? defaultDbPath());
        // One-time JSONL migration, only for the real store: an injected test
        // path must never scan (or import) the user's actual sessions dir.
        // Tests drive migrateLegacySessions directly with a fixture root.
        if (!overridePath) {
            try {
                migrateLegacySessions(db);
            } catch (err) {
                debugLog("session-db", "legacy session migration failed:", err as Error);
            }
        }
    }
    return db;
}

/** Checkpoint and close — call on clean exit so the -wal file doesn't grow unbounded. */
export function closeDb(): void {
    if (!db) return;
    try {
        db.exec("PRAGMA wal_checkpoint(TRUNCATE)");
    } catch (err) {
        debugLog("session-db", "wal_checkpoint on close failed:", err as Error);
    }
    db.close();
    db = null;
}

/** Point the singleton at a temp file or ":memory:" — tests only. */
export function setDbPathForTests(path: string | null): void {
    closeDb();
    overridePath = path;
}
