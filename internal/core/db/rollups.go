package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Running totals for /steak and /cost, kept by SQLite triggers — a port of
// loop's sessions/rollups.ts. The tables, triggers, filters and version stamp
// must stay identical to loop's: both programs open this file, whichever opens
// it first builds the totals, and the other trusts the stamp.
//
// Both commands used to aggregate the raw tables on every call (/steak scanned
// every entry, /cost grouped the whole ledger), so they slowed down with every
// turn ever billed. The triggers fold each row into a small total as it is
// written; because they live in the database, every writer keeps the totals
// right — pi, loop, an older build of either, a cascade when a session is
// deleted. New tables and triggers only, so no schema_version bump.

// rollupsVersion is loop's ROLLUPS_VERSION. Any other stamp drops the totals
// and rebuilds them from the raw rows.
const rollupsVersion = "1"

// UsageBucketMS is the slice /steak's token totals are kept in. The local day
// is a read-time question (the machine may change zone), and every real UTC
// offset (+5:30, +5:45, −3:30 …) is a whole number of quarter hours, so a
// quarter-hour total lands in the same local day its raw ts would.
const UsageBucketMS = 15 * 60 * 1000

// countedEntry is the entries /steak counts: non-estimated assistant and
// subagent usage.
func countedEntry(row string) string {
	return fmt.Sprintf(`
    ((%[1]s.type = 'message' AND %[1]s.role = 'assistant') OR %[1]s.type = 'subagent')
    AND COALESCE(%[1]s.usage_input, 0) + COALESCE(%[1]s.usage_output, 0) > 0
    AND COALESCE(%[1]s.usage_estimated, 0) = 0`, row)
}

// billedRow is the ledger rows /cost bills: real spend, not estimates or backfills.
func billedRow(row string) string {
	return fmt.Sprintf("%[1]s.estimated = 0 AND %[1]s.backfilled = 0", row)
}

// foldUsage folds one entry into (sign 1) or out of (sign -1) the usage totals.
func foldUsage(row string, sign int) string {
	return fmt.Sprintf(`
    INSERT INTO usage_rollup (bucket, tokens)
    SELECT %[1]s.ts / %[3]d,
           %[2]d * (COALESCE(%[1]s.usage_input, 0) + COALESCE(%[1]s.usage_output, 0))
    WHERE %[4]s
    ON CONFLICT (bucket) DO UPDATE SET tokens = tokens + excluded.tokens;`,
		row, sign, UsageBucketMS, countedEntry(row))
}

// foldLedger folds one ledger row into or out of the ledger totals. cwd is
// NOT NULL there so it can be part of the key.
func foldLedger(row string, sign int) string {
	return fmt.Sprintf(`
    INSERT INTO ledger_rollup (day, provider, cwd, usd, input_tokens, output_tokens)
    SELECT %[1]s.day, %[1]s.provider, COALESCE(%[1]s.cwd, ''),
           %[2]d * %[1]s.usd, %[2]d * %[1]s.input_tokens, %[2]d * %[1]s.output_tokens
    WHERE %[3]s
    ON CONFLICT (day, provider, cwd) DO UPDATE SET
        usd = usd + excluded.usd,
        input_tokens = input_tokens + excluded.input_tokens,
        output_tokens = output_tokens + excluded.output_tokens;`,
		row, sign, billedRow(row))
}

// Columns a total depends on: an UPDATE of anything else (the ledger's entry
// link, say) skips the trigger.
const (
	usageColumns  = "ts, type, role, usage_input, usage_output, usage_estimated"
	ledgerColumns = "day, provider, cwd, usd, input_tokens, output_tokens, estimated, backfilled"
)

const dropRollups = `
DROP TRIGGER IF EXISTS usage_rollup_insert;
DROP TRIGGER IF EXISTS usage_rollup_delete;
DROP TRIGGER IF EXISTS usage_rollup_update;
DROP TRIGGER IF EXISTS ledger_rollup_insert;
DROP TRIGGER IF EXISTS ledger_rollup_delete;
DROP TRIGGER IF EXISTS ledger_rollup_update;
DROP TABLE IF EXISTS usage_rollup;
DROP TABLE IF EXISTS ledger_rollup;
`

func createRollups() string {
	var b strings.Builder
	b.WriteString(`
CREATE TABLE usage_rollup (
    bucket INTEGER PRIMARY KEY,
    tokens INTEGER NOT NULL
);
CREATE TABLE ledger_rollup (
    day      TEXT NOT NULL,
    provider TEXT NOT NULL,
    cwd      TEXT NOT NULL,
    usd      REAL NOT NULL,
    input_tokens  INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    PRIMARY KEY (day, provider, cwd)
) WITHOUT ROWID;
CREATE INDEX idx_ledger_rollup_cwd ON ledger_rollup(cwd);
`)
	fmt.Fprintf(&b, "CREATE TRIGGER usage_rollup_insert AFTER INSERT ON entries BEGIN %s END;\n", foldUsage("NEW", 1))
	fmt.Fprintf(&b, "CREATE TRIGGER usage_rollup_delete AFTER DELETE ON entries BEGIN %s END;\n", foldUsage("OLD", -1))
	fmt.Fprintf(&b, "CREATE TRIGGER usage_rollup_update AFTER UPDATE OF %s ON entries BEGIN %s %s END;\n",
		usageColumns, foldUsage("OLD", -1), foldUsage("NEW", 1))
	fmt.Fprintf(&b, "CREATE TRIGGER ledger_rollup_insert AFTER INSERT ON cost_ledger BEGIN %s END;\n", foldLedger("NEW", 1))
	fmt.Fprintf(&b, "CREATE TRIGGER ledger_rollup_delete AFTER DELETE ON cost_ledger BEGIN %s END;\n", foldLedger("OLD", -1))
	fmt.Fprintf(&b, "CREATE TRIGGER ledger_rollup_update AFTER UPDATE OF %s ON cost_ledger BEGIN %s %s END;\n",
		ledgerColumns, foldLedger("OLD", -1), foldLedger("NEW", 1))
	return b.String()
}

// backfillRollups is the one-time catch-up: every row written before the
// triggers existed.
func backfillRollups() string {
	return fmt.Sprintf(`
INSERT INTO usage_rollup (bucket, tokens)
SELECT ts / %[1]d, SUM(COALESCE(usage_input, 0) + COALESCE(usage_output, 0))
FROM entries AS NEW WHERE %[2]s
GROUP BY 1;
INSERT INTO ledger_rollup (day, provider, cwd, usd, input_tokens, output_tokens)
SELECT day, provider, COALESCE(cwd, ''), SUM(usd), SUM(input_tokens), SUM(output_tokens)
FROM cost_ledger AS NEW WHERE %[3]s
GROUP BY 1, 2, 3;
`, UsageBucketMS, countedEntry("NEW"), billedRow("NEW"))
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func stampedRollups(ctx context.Context, q querier) string {
	var v string
	_ = q.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'rollups_version'").Scan(&v)
	return v
}

// ensureRollups makes sure the totals and their triggers exist and match this
// build, building them from the raw rows when they don't. After the first open
// it is one meta read.
//
// The build holds the write lock (BEGIN IMMEDIATE) on one connection and
// re-checks the stamp inside it, so two processes opening at once build it
// once, and no row can land between the catch-up and the triggers going live.
func ensureRollups(h *sql.DB) error {
	ctx := context.Background()
	if stampedRollups(ctx, h) == rollupsVersion {
		return nil
	}
	conn, err := h.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	if err := buildRollups(ctx, conn); err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func buildRollups(ctx context.Context, conn *sql.Conn) error {
	if stampedRollups(ctx, conn) == rollupsVersion {
		return nil
	}
	for _, step := range []string{dropRollups, createRollups(), backfillRollups()} {
		if _, err := conn.ExecContext(ctx, step); err != nil {
			return err
		}
	}
	_, err := conn.ExecContext(ctx,
		"INSERT OR REPLACE INTO meta (key, value) VALUES ('rollups_version', ?)", rollupsVersion)
	return err
}
