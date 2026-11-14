package session

import (
	"math"
	"testing"
	"time"

	"github.com/notshekhar/pi/internal/core/db"
)

const dayMS = int64(24 * time.Hour / time.Millisecond)

// rawDailyTokens and rawStats are the oracle: the queries /steak and /cost ran
// over the raw tables before the trigger-kept totals existed.
func rawDailyTokens(t *testing.T) map[string]int64 {
	t.Helper()
	h, _ := db.Open()
	rows, err := h.Query(`SELECT date(ts / 1000, 'unixepoch', 'localtime') AS day,
			SUM(COALESCE(usage_input, 0) + COALESCE(usage_output, 0))
		FROM entries
		WHERE ((type = 'message' AND role = 'assistant') OR type = 'subagent')
		  AND COALESCE(usage_input, 0) + COALESCE(usage_output, 0) > 0
		  AND COALESCE(usage_estimated, 0) = 0
		GROUP BY day`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var d string
		var n int64
		_ = rows.Scan(&d, &n)
		out[d] = n
	}
	return out
}

func rawTotals(t *testing.T) (usd float64, in, out int64) {
	t.Helper()
	h, _ := db.Open()
	if err := h.QueryRow(`SELECT COALESCE(SUM(usd), 0), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)
		FROM cost_ledger WHERE estimated = 0 AND backfilled = 0`).Scan(&usd, &in, &out); err != nil {
		t.Fatal(err)
	}
	return
}

func exec(t *testing.T, query string, args ...any) {
	t.Helper()
	h, err := db.Open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

// seedUsage writes a session's entries straight into the table, the way any
// writer would: counted, estimated, user, and either side of a local midnight.
func seedUsage(t *testing.T, pub string) {
	t.Helper()
	now := time.Now().UnixMilli()
	exec(t, `INSERT INTO sessions (pub_id, cwd, provider, model, created_at, updated_at) VALUES (?, '/repo', 'xai', 'm', ?, ?)`, pub, now, now)
	y, m, d := time.Now().Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, time.Local).UnixMilli()
	for i, e := range []struct {
		ts           int64
		role         string
		in, out, est int64
	}{
		{now, "user", 0, 0, 0},
		{now, "assistant", 100, 10, 0},
		{now - 3*dayMS, "assistant", 7, 3, 0},
		{midnight - 1, "assistant", 5, 0, 0},
		{midnight, "assistant", 0, 9, 0},
		{now, "assistant", 1_000_000, 0, 1},
	} {
		exec(t, `INSERT INTO entries (session_id, pub_id, ts, type, role, payload, usage_input, usage_output, usage_estimated)
			VALUES ((SELECT id FROM sessions WHERE pub_id = ?), ?, ?, 'message', ?, '{}', ?, ?, ?)`,
			pub, pub+string(rune('a'+i)), e.ts, e.role, e.in, e.out, e.est)
	}
}

func bill(t *testing.T, usd float64, estimated bool) int64 {
	t.Helper()
	in, out := int64(10), int64(1)
	id, err := AddLedgerRow(LedgerRow{Provider: "xai", Model: "xai/grok", USD: usd, Estimated: estimated,
		Source: SourceTurn, CWD: "/repo", Usage: Usage{InputTokens: &in, OutputTokens: &out}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func expectTotalsMatchRawRows(t *testing.T) {
	t.Helper()
	daily, err := DailyTokens()
	if err != nil {
		t.Fatal(err)
	}
	raw := rawDailyTokens(t)
	if len(daily) != len(raw) {
		t.Fatalf("daily tokens %v, raw rows say %v", daily, raw)
	}
	for d, n := range raw {
		if daily[d] != n {
			t.Fatalf("day %s: %d tokens, raw rows say %d", d, daily[d], n)
		}
	}
	usd, in, out := rawTotals(t)
	all, err := TotalUsage()
	if err != nil {
		t.Fatal(err)
	}
	st, err := Stats("/repo")
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(all.CostUSD-usd) > 1e-9 || all.InputTokens != in || all.OutputTokens != out {
		t.Fatalf("/cost all %+v, raw rows say $%v in:%d out:%d", all, usd, in, out)
	}
	if math.Abs(st.Lifetime-usd) > 1e-9 || math.Abs(st.Cwd-usd) > 1e-9 {
		t.Fatalf("stats %+v, raw rows say $%v", st, usd)
	}
}

func TestRollupsFollowEveryWrite(t *testing.T) {
	withSessionHome(t)
	seedUsage(t, "kept")
	seedUsage(t, "dropped")
	bill(t, 0.5, false)
	bill(t, 9, true)
	regraded := bill(t, 3, true)
	expectTotalsMatchRawRows(t)

	exec(t, "DELETE FROM sessions WHERE pub_id = 'dropped'") // entries go by cascade
	exec(t, "UPDATE cost_ledger SET estimated = 0 WHERE id = ?", regraded)
	exec(t, "DELETE FROM cost_ledger WHERE usd = 0.5")
	expectTotalsMatchRawRows(t)
}

func TestStatsReadTheTotalsNotTheRawTables(t *testing.T) {
	withSessionHome(t)
	seedUsage(t, "s")
	bill(t, 0.5, false)
	before, _ := DailyTokens()
	// Empty the raw tables behind the triggers' back: totals that survive
	// came from the rollups alone.
	exec(t, `DROP TRIGGER usage_rollup_delete; DROP TRIGGER ledger_rollup_delete;
		DELETE FROM cost_ledger; DELETE FROM entries;`)
	after, _ := DailyTokens()
	if len(after) == 0 || len(after) != len(before) {
		t.Fatalf("/steak read the raw table: before %v, after %v", before, after)
	}
	if all, _ := TotalUsage(); math.Abs(all.CostUSD-0.5) > 1e-9 {
		t.Fatalf("/cost all read the raw table: %+v", all)
	}
}

func TestRollupsAreBuiltForAnOlderDatabase(t *testing.T) {
	withSessionHome(t)
	seedUsage(t, "s")
	bill(t, 0.75, false)
	// What an older build leaves behind: the raw rows, no totals, no stamp.
	exec(t, `DROP TABLE usage_rollup; DROP TABLE ledger_rollup; DELETE FROM meta WHERE key = 'rollups_version'`)
	db.Close()
	expectTotalsMatchRawRows(t)
}
