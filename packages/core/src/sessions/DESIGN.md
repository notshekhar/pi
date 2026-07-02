# Sessions on SQLite — Design

Move ALL session-adjacent state from files into one `bun:sqlite` database:
transcripts, cost/usage accrual, per-project trust decisions, per-project
model picks, and reminders. This retires the whole class of fs bugs we kept
fixing (torn-tail corruption, lockfile-per-append, full-file peeks, cross-
process lost updates) instead of patching around them, and makes /resume,
/steak, and /cost indexed queries instead of directory scans.

What moves into the DB (and which file it replaces):

| DB                   | replaces                                     |
| -------------------- | -------------------------------------------- |
| `sessions`/`entries` | `agent/sessions/<slug>/<id>.jsonl`           |
| `cost_ledger` + meta | `cost.json`                                  |
| `projects.trust`     | `trust.json`                                 |
| `projects.model`     | the `projectModels` key inside settings.json |
| `reminders`          | `reminders.json`                             |

What deliberately stays a file: settings.json (user preferences — minus the
`projectModels` key, which is per-project state, not a preference), auth.json
(credentials), datasources.json (connection config), catalog.json (remote
cache), the extension stores, and user-authored prompts/agents/skills dirs.
Those are configs people read and edit by hand; session state is not.

JSONL remains the _interchange_ format: `/export` emits it, `/import` ingests
it; only the storage engine changes. `bun:sqlite` only — no ORM, no native
deps, hand-written prepared statements.

---

## 0. Proven foundations (verified 2026-07-02, Bun 1.4.0-canary)

1. **`bun:sqlite` works inside a `bun build --compile` binary** — WAL mode,
   transactions, AUTOINCREMENT, prepared statements; persists across runs.
   Nothing extra ships in the binary.
2. **Gotcha: `import.meta.dir` inside a compiled binary is `/$bunfs/root/`**
   (the read-only virtual bundle fs) — opening a DB there fails with
   SQLITE_CANTOPEN. The DB path must always come from `getLoopDir()`.
3. **Two processes writing one WAL DB is safe, with two rules.**
   `PRAGMA busy_timeout` must be set _before_ `PRAGMA journal_mode = WAL`
   (the switch itself takes a lock), and the _first-ever_ WAL switch on a
   fresh file can still return SQLITE_BUSY when two processes create it
   simultaneously — busy_timeout does not reliably cover it. WAL is
   _persistent_ in the file, so the race exists only at creation: wrap the
   whole init (open → pragmas → schema) in a bounded retry (~20 × 25ms
   backoff). Verified: 2 processes × 500 transactional inserts on a fresh
   file → 1000 rows, `integrity_check: ok`; on an existing WAL file no retry
   is ever needed.

---

## 1. Schema

```sql
PRAGMA busy_timeout = 5000;      -- FIRST (see foundation #3)
PRAGMA journal_mode = WAL;       -- persistent; set once at creation
PRAGMA synchronous = NORMAL;     -- durable-enough with WAL, much faster
PRAGMA foreign_keys = ON;

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,      -- schema_version, migrated_at,
    value TEXT NOT NULL          -- cost_baseline (JSON: lifetime usd/byProvider)
);

CREATE TABLE sessions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,  -- internal only
    pub_id     TEXT NOT NULL UNIQUE,               -- the session ulid (public)
    cwd        TEXT NOT NULL,
    provider   TEXT NOT NULL,
    model      TEXT NOT NULL,
    name       TEXT,                               -- latest session-name (denormalized)
    parent_pub TEXT,                               -- fork provenance
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL                    -- replaces file mtime for ordering
);
CREATE INDEX idx_sessions_cwd ON sessions(cwd, updated_at DESC);

CREATE TABLE entries (
    id            INTEGER PRIMARY KEY AUTOINCREMENT, -- internal; ORDER BY id ≡ append order
    session_id    INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    pub_id        TEXT NOT NULL,                     -- the 8-hex entry id (public)
    parent_pub_id TEXT,                              -- tree edge (NULL = root)
    ts            INTEGER NOT NULL,
    type          TEXT NOT NULL,
    role          TEXT,                              -- for type='message'
    payload       TEXT NOT NULL,                     -- the FULL entry JSON (source of truth)
    -- Derived usage columns, FULL UsageBlock fidelity (aggregate queries only;
    -- never read back into entries). Populated from the v7 nested details with
    -- v6 flat-field fallback, same precedence the readers use today.
    usage_input       INTEGER,                       -- total input as reported
    usage_output      INTEGER,
    usage_total       INTEGER,
    usage_no_cache    INTEGER,                       -- inputTokenDetails.noCacheTokens
    usage_cache_read  INTEGER,                       -- …cacheReadTokens ?? cachedInputTokens
    usage_cache_write INTEGER,                       -- …cacheWriteTokens
    usage_text        INTEGER,                       -- outputTokenDetails.textTokens
    usage_reasoning   INTEGER,                       -- …reasoningTokens ?? reasoningTokens
    usage_estimated   INTEGER,                       -- 0/1
    model             TEXT,
    UNIQUE (session_id, pub_id)
);
CREATE INDEX idx_entries_session ON entries(session_id, id);
CREATE INDEX idx_entries_usage ON entries(ts) WHERE usage_input IS NOT NULL;

-- The cost LEDGER: one row per billed API round-trip (a turn step, a subagent
-- step, a recap/compact/branch-summary call). Append-only; every displayed
-- dollar is a SUM over these rows, and every row carries the token quantities
-- AND the unit prices it was computed from, so any total is auditable and
-- recomputable row-by-row.
CREATE TABLE cost_ledger (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ts           INTEGER NOT NULL,
    day          TEXT NOT NULL,                      -- local YYYY-MM-DD (dayKey())
    session_id   INTEGER REFERENCES sessions(id) ON DELETE SET NULL,
    session_pub  TEXT,                               -- denormalized: billing survives session deletion
    source       TEXT NOT NULL,                      -- 'turn'|'subagent'|'recap'|'compact'|'branch-summary'
    cwd          TEXT,
    provider     TEXT NOT NULL,
    model        TEXT NOT NULL,                      -- full id, e.g. anthropic/claude-x
    -- what was consumed (token quantities, full class split)
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    no_cache_tokens    INTEGER,                      -- billed at full input rate
    cache_read_tokens  INTEGER,
    cache_write_tokens INTEGER,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens   INTEGER,
    -- at what rates ($/MTok, snapshot of the catalog at insert time)
    price_input       REAL,
    price_output      REAL,
    price_cache_read  REAL,
    price_cache_write REAL,
    -- the result
    usd           REAL NOT NULL,
    provider_cost REAL,                              -- provider-reported (openrouter); when set, usd = this
    estimated     INTEGER NOT NULL DEFAULT 0,        -- interrupted-turn estimate
    backfilled    INTEGER NOT NULL DEFAULT 0         -- created at migration from old entries (see §3)
);
CREATE INDEX idx_ledger_day ON cost_ledger(day) WHERE estimated = 0 AND backfilled = 0;
CREATE INDEX idx_ledger_cwd ON cost_ledger(cwd) WHERE estimated = 0 AND backfilled = 0;
CREATE INDEX idx_ledger_session ON cost_ledger(session_pub);

CREATE TABLE projects (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    dir        TEXT NOT NULL UNIQUE,                 -- canonical (realpath) absolute dir
    trust      INTEGER,                              -- 1 trusted / 0 untrusted / NULL undecided
    model      TEXT,                                 -- last model pick for this dir
    updated_at INTEGER NOT NULL
);

CREATE TABLE reminders (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    pub_id     TEXT NOT NULL UNIQUE,                 -- existing reminder ulid
    text       TEXT NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    kind       TEXT NOT NULL,                        -- 'once' | 'cron'
    at         INTEGER,                              -- kind='once': absolute ms
    expr       TEXT,                                 -- kind='cron': expression verbatim
    created_at INTEGER NOT NULL
);
```

Rationale, point by point:

- **Integer `AUTOINCREMENT` PK + separate `pub_id`.** Internal joins and
  ordering ride a monotonically increasing integer (AUTOINCREMENT, not bare
  rowid, so ids are never reused after deletes — `ORDER BY id` is append
  order, forever, the exact property file order gave JSONL). The public
  identity (session ulid, 8-hex entry id) is its own UNIQUE column — URLs,
  /resume, exports, and the tree keep speaking pub ids; the integers never
  leak out of the storage layer.
- **`payload` JSON is the single source of truth** for entry content. New
  entry types need zero schema changes (OCP) — they're just rows with a new
  `type` string, exactly as JSONL behaved. The usage/model columns are
  _derived at insert_ and used only by aggregate queries (never deserialized
  back into an Entry), so they can't drift into a second source of truth.
- **`UNIQUE(session_id, pub_id)`** makes migration and import idempotent:
  `INSERT OR IGNORE` re-runs cleanly after a crash.
- **`cost_ledger` is separate from `entries`, deliberately.** USD is
  _time-of-use_ data: prices change, so deriving dollars from tokens at query
  time would rewrite history. The tracker computes usd once per step (as
  today), snapshots the unit prices it used into the row, and appends it
  transactionally. Billing also must survive session deletion — hence
  `ON DELETE SET NULL` + the denormalized `session_pub`. Every row satisfies
  `usd ≈ no_cache/1e6·price_input + output/1e6·price_output +
cache_read/1e6·price_cache_read + cache_write/1e6·price_cache_write`
  (unless `provider_cost` is set, in which case `usd = provider_cost`) — a
  reconciliation test asserts this over the whole table.
- **`projects` unifies the two per-directory maps** that today live in
  different files (trust.json; `projectModels` inside settings.json). Trust
  keeps its exact semantics: canonical (realpath) keys, nearest-ancestor
  lookup, three-state decision — the lookup walks ancestors over an in-memory
  snapshot of the (tiny) table, refreshed on write, same as the current
  Configstore read. Session-only trust grants stay in-process, unpersisted.
- **`reminders` keeps its read cache.** The 1s ticker reads the list twice a
  second; even though a prepared SELECT is microseconds, the in-memory
  cache-invalidated-on-write pattern stays — the DB is the persistence layer,
  not a thing to poll. `MAX_REMINDERS` enforcement stays in code.

## 1b. Usage & cost correctness (highest priority)

### The invariants (each one is a test)

1. **Session total, live = session total, reopened.** Live display is the
   in-memory running sum the status line already ticks per step; every
   `tracker.add` ALSO appends a ledger row, and reopening seeds from
   `SELECT SUM(...) FROM cost_ledger WHERE session_pub = ?` — the _recorded_
   dollars, never a recompute. (Today `seedFromSession` re-prices old token
   counts with the CURRENT catalog, so a resumed session can show a different
   number than it showed live. The ledger kills this bug by construction.)
2. **Lifetime = `meta.cost_baseline` + `SUM(usd) WHERE estimated=0 AND
backfilled=0`.** Estimated (interrupted-turn) rows ARE inserted — flagged —
   so the session sum includes them (with the `~` prefix when any exist,
   exactly today's semantics) while billing views exclude them.
3. **Every row is self-auditing** (quantities × snapshot prices = usd, see
   schema note) — one `PRAGMA`-cheap reconciliation query run in tests and
   exposed as `loop cost audit` for the user.
4. **Ledger ↔ entries cross-check.** For source='turn'/'subagent', the token
   quantities in the ledger must equal the usage columns of the entries the
   same steps persisted (per session). One JOIN asserts it in tests.
5. **`/steak` (tokens) excludes `usage_estimated=1`** — fixes a pre-existing
   inconsistency where dailyTokens counted estimates that /cost never billed.

### Audit findings this design must fix (verified in code, 2026-07-02)

- **Compact and branch-summary spend is UNBILLED today.** Both call
  `generateText` — real API spend — with no `tracker.add` at all (recap bills
  correctly). Fix: both take the tracker and append ledger rows with their
  `source`. Their usage should also be persisted on their transcript entries.
- **Reprice drift on reopen** (invariant 1 above).
- **`/steak` counts estimated usage** (invariant 5 above).
- **Full token-class fidelity end-to-end.** `UsageBlock` already carries
  input/output/total, noCache/cacheRead/cacheWrite, text/reasoning, provider
  `cost`, `estimated`, plus v6 flat fallbacks (`cachedInputTokens`,
  `reasoningTokens`). The ledger and the entry usage columns must carry ALL of
  them — the earlier draft dropped reasoning/noCache/total. One shared
  `normalizeUsage(u: UsageBlock)` helper resolves the v7-nested-else-v6-flat
  precedence in exactly one place (today it's re-derived in cost.ts,
  model-messages.ts, and the CLI's status line independently).
- **`inputTokens` inclusivity must be VERIFIED per provider, not assumed.**
  `computeUsd` bills `noCacheTokens ?? max(0, input − cacheRead − cacheWrite)`
  — i.e. it assumes reported `inputTokens` INCLUDES the cached classes. If any
  ai-sdk adapter reports exclusive input, we misbill. P0 of the implementation
  session: record real `finish-step` usage fixtures for anthropic, openai,
  openrouter, google, ollama (LOOP_HTTP_DEBUG + one cached call each), pin the
  mapping in a fixture test, and branch per-provider in `normalizeUsage` if
  they differ. Do not ship the ledger on an unverified assumption.
- **Provider-reported cost**: honored only for openrouter today (correct —
  openrouter's `usage.cost` is authoritative); the ledger stores it in
  `provider_cost` and prefers it, per-row, so the policy is visible in data.

### CostTracker after the change

```
add(modelId, usage, ctx: { cwd, sessionPub?, source })   // computes usd, snapshots prices, INSERTs, updates in-memory sum
addEstimated(...)                                        // same, estimated=1
seedFromLedger(sessionPub)                               // reopened session: recorded sums, zero recompute
stats(cwd?)                                              // baseline + GROUP BYs (today/7d/month/cwd/provider)
sessionBreakdown()/format()/reset()                      // unchanged surface
```

`seedFromSession` survives only to read the ctx meter (last main assistant
usage from entries — context size is not money) and as the fallback for
sessions with no ledger rows at all (pre-backfill edge). The cost.json
refresh/NaN defenses die with cost.json.

## 2. Architecture

- **`sessions/db.ts`** — the connection singleton. Path from `getLoopDir()`
  (foundation #2), init-retry (foundation #3), pragmas in the right order,
  `meta.schema_version` gate for future migrations, `PRAGMA quick_check` on
  open. Tests inject their own path or `:memory:` — nothing else in the
  codebase touches `new Database`.
- **`sessions/sqlite-store.ts`** — prepared statements behind a small
  `SessionStore` surface: `insertSession`, `appendEntries(sessionId, rows)`
  (one transaction per batch — the `appendAll` contract survives),
  `loadEntries`, `listSessions`, `forkSession` (transactional copy).
- **`Session` keeps its public API byte-for-byte** (`append`/`appendAll`/
  `getBranch`/`getTree`/`branch`/`fork` helpers/…). It keeps the in-memory
  index (byId/leaf/labels) exactly as now; only the persistence calls swap
  from appendFileSync+lockfile to the store (LSP: no caller can tell). The
  340-test suite runs against the new engine and is the acceptance gate.
- **`SessionManager`**: `list()` = one SELECT (peek + its mtime cache are
  deleted); `findById` = SELECT by pub_id; `dailyTokens()` (/steak) = one
  GROUP BY over the usage columns; `forkAtEntry` = one transaction.
- **`CostTracker`**: see §1b — `add()` appends a self-auditing `cost_ledger`
  row (quantities + price snapshot + usd) instead of read-modify-writing
  cost.json; reopen seeds from the ledger; `stats()` = baseline + GROUP BYs.
  Callers gain a `source` + `sessionPub` in the add context; compact and
  branch-summary START billing (they don't today).
- **`agent/trust.ts`** swaps its Configstore for the `projects` table behind
  the same exported functions (`getTrustDecision`/`setTrust`/…). Same for
  **`getProjectModel`/`setProjectModel`** in auth/storage.ts and for
  **`reminders.ts`** — every module keeps its public surface; only the
  persistence line changes (ISP: callers never see the DB).
- **Deleted once P4 lands**: `proper-lockfile` dependency, the torn-tail
  guard, the peek cache, all writes to cost.json / trust.json /
  reminders.json / `settings.projectModels`.

## 3. Migrating existing JSONL sessions

Trigger: first `db.ts` open with no `meta.migrated_at`.

1. Walk the sessions dir exactly as `list()` does today.
2. For each `.jsonl`: `Session.load()` — which _already_ tolerates corrupt
   lines, adapts legacy entry shapes (`adaptLoopEntry`), and upgrades flat
   entries to the tree (`ensureTreeFields`) — then insert the session row and
   all entry rows in **one transaction** with `INSERT OR IGNORE`.
3. Snapshot cost.json's lifetime into `meta.cost_baseline` (this remains the
   ONLY source of pre-migration lifetime dollars). Then **backfill per-session
   ledger rows** from each migrated session's usage-bearing entries, priced
   with the current catalog and flagged `backfilled=1`: reopening an old
   session shows a sensible cost figure (invariant 1 still holds — the sum is
   whatever the ledger says), while billing views exclude backfilled rows so
   nothing pre-baseline is double-counted. Honest by construction: the flag
   records that these prices are today's, not time-of-use.
4. Copy the small stores in the same transaction batch: every trust.json
   entry → `projects.trust` (dirs kept verbatim — they're already canonical);
   every `settings.projectModels` entry → `projects.model` (merging into the
   same row when a dir has both); every reminders.json reminder →
   `reminders` (ulid → pub_id). Then delete the `projectModels` key from
   settings.json; trust.json and reminders.json stay on disk unread, like the
   transcripts.
5. Set `meta.migrated_at`. **Originals stay in place** — downgrade-safe, and
   they double as the corruption-recovery source: if `quick_check` ever
   fails, move the DB aside and re-migrate.

Idempotent and resumable: a crash mid-migration re-runs and skips whatever
landed (pub_id uniqueness). Stragglers (a `.jsonl` restored from backup later)
are handled by import-on-open fallback in `open()`, and `/import` funnels
through the same path. `/export` is untouched.

Test fixtures: legacy flat entries (no ids), torn tail, duplicate pub_ids
across files, empty file, a multi-MB session, crash-mid-migration re-run.
Cost/usage fixtures: recorded per-provider `finish-step` usage shapes (the
inclusivity verification set), a session with a mid-turn model switch, an
interrupted turn (estimated row), a subagent run, and a backfilled session —
each asserting the five §1b invariants.

## 4. Phases

- **P1** — db.ts + sqlite-store + Session/SessionManager on SQLite; full
  suite green with injected temp/`:memory:` DBs.
- **P2** — migration pass + import-on-open + /import //export parity + the
  fixture set above.
- **P3** — the ledger: provider usage-fixture verification FIRST (§1b — do not
  build on the inclusivity assumption), `normalizeUsage`, cost_ledger +
  baseline + backfill, compact/branch-summary billing, seedFromLedger,
  /cost + /steak on queries, `loop cost audit`, the five invariant tests.
  Then projects (trust + model) and reminders — each module keeps its public
  API, file writes retired (files left on disk).
- **P4** — cleanup (drop proper-lockfile, peek cache, torn-tail guard), a real
  two-process concurrency test, compiled-binary smoke test, release **0.8.0**
  (storage change ⇒ minor, not patch).

## 5. Known risks

- **WAL on network filesystems** (NFS-mounted home dirs) is unsupported by
  SQLite generally; document it. Not worth a fallback mode until someone hits it.
- **WAL checkpointing**: defaults are fine at loop's write rate; run
  `wal_checkpoint(TRUNCATE)` on clean exit so the `-wal` file doesn't grow
  unbounded across long daemon runs.
- **Corruption**: `quick_check` on open; recovery = re-migrate from the
  retained JSONL originals (and, going forward, periodic `VACUUM INTO` backup
  is cheap insurance — decide in P4).
