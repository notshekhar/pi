package session

import (
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/core/db"
	"github.com/notshekhar/pi/internal/core/jsonstore"
)

// The cost ledger — a port of loop's sessions/cost-ledger.ts and the stats
// roll-up in agent/cost.ts. One append-only row per billed API round-trip,
// carrying the token quantities AND the $/MTok prices the dollars were
// computed from, so every displayed total is a SUM over auditable rows.
//
// Billing views (today, week, month, lifetime, per folder) exclude estimated
// rows (interrupted-turn guesses) and backfilled ones; a single session's
// total includes everything recorded for it.

// LedgerSource is where a billed round-trip came from.
type LedgerSource string

const (
	SourceTurn          LedgerSource = "turn"
	SourceSubagent      LedgerSource = "subagent"
	SourceRecap         LedgerSource = "recap"
	SourceCompact       LedgerSource = "compact"
	SourceBranchSummary LedgerSource = "branch-summary"
	SourceSessionTitle  LedgerSource = "session-title"
	SourceGoalPlanner   LedgerSource = "goal-planner"
	SourceGoalVerifier  LedgerSource = "goal-verifier"
	SourceHandoff       LedgerSource = "handoff"
	SourceGoalParse     LedgerSource = "goal-parse"
	SourceCommitMessage LedgerSource = "commit-message"
	SourceRecipe        LedgerSource = "recipe"
	SourceTeam          LedgerSource = "team"
)

// Prices is a $/MTok snapshot; nil fields are unknown.
type Prices struct {
	Input, Output, CacheRead, CacheWrite *float64
}

// LedgerRow is one billed round-trip.
type LedgerRow struct {
	Provider, Model string
	Usage           Usage
	USD             float64
	ProviderCost    *float64
	Prices          Prices
	Estimated       bool
	Backfilled      bool
	TS              int64
	Source          LedgerSource
	CWD             string
	SessionPub      string
}

// DayKey is the local-timezone YYYY-MM-DD a cost bucket is filed under.
func DayKey(t time.Time) string { return t.Format("2006-01-02") }

// AddLedgerRow records a billed round-trip and returns its row id. Rows are
// inserted the moment a step finishes; the entry that carried the same usage
// is linked afterwards with AttachLedgerEntry.
func AddLedgerRow(r LedgerRow) (int64, error) {
	h, err := db.Open()
	if err != nil {
		return 0, err
	}
	if r.TS == 0 {
		r.TS = time.Now().UnixMilli()
	}
	n := r.Usage.Normalize()
	var sessionID, sessionPub, cwd any
	if r.SessionPub != "" {
		sessionPub = r.SessionPub
		var id int64
		if h.QueryRow("SELECT id FROM sessions WHERE pub_id = ?", r.SessionPub).Scan(&id) == nil {
			sessionID = id
		}
	}
	if r.CWD != "" {
		cwd = r.CWD
	}
	res, err := h.Exec(`INSERT INTO cost_ledger (
			ts, day, session_id, session_pub, source, cwd, provider, model,
			input_tokens, no_cache_tokens, cache_read_tokens, cache_write_tokens,
			output_tokens, reasoning_tokens,
			price_input, price_output, price_cache_read, price_cache_write,
			usd, provider_cost, estimated, backfilled
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TS, DayKey(time.UnixMilli(r.TS)), sessionID, sessionPub, string(r.Source), cwd, r.Provider, r.Model,
		i64(n.Input), nullable(n.NoCache), nullable(n.CacheRead), nullable(n.CacheWrite),
		i64(n.Output), nullable(n.Reasoning),
		nullable(r.Prices.Input), nullable(r.Prices.Output), nullable(r.Prices.CacheRead), nullable(r.Prices.CacheWrite),
		r.USD, nullable(r.ProviderCost), b2i(r.Estimated), b2i(r.Backfilled))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// AttachLedgerEntry links a billed row to the entry that carried its usage.
func AttachLedgerEntry(rowID int64, entryPub string) error {
	h, err := db.Open()
	if err != nil {
		return err
	}
	_, err = h.Exec(`UPDATE cost_ledger SET entry_pub = ?,
			entry_id = (SELECT e.id FROM entries e WHERE e.pub_id = ? AND e.session_id = cost_ledger.session_id)
		WHERE id = ?`, entryPub, entryPub, rowID)
	return err
}

// SessionSums is the recorded spend of one session.
type SessionSums struct {
	Rows                      int
	USD                       float64
	InputTokens, OutputTokens int64
	CachedInputTokens         int64
	// Estimated is set when any row was a guess — the `~` in front of a total.
	Estimated bool
}

// SumLedgerForSession totals every row recorded for a session.
func SumLedgerForSession(sessionPub string) (SessionSums, error) {
	h, err := db.Open()
	if err != nil {
		return SessionSums{}, err
	}
	var s SessionSums
	var est sql.NullInt64
	err = h.QueryRow(`SELECT COUNT(*), COALESCE(SUM(usd), 0), COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0), COALESCE(SUM(COALESCE(cache_read_tokens, 0)), 0), MAX(estimated)
		FROM cost_ledger WHERE session_pub = ?`, sessionPub).
		Scan(&s.Rows, &s.USD, &s.InputTokens, &s.OutputTokens, &s.CachedInputTokens, &est)
	s.Estimated = est.Valid && est.Int64 == 1
	return s, err
}

// costBaseline is the pre-ledger spend loop froze into meta at its cutover.
type costBaseline struct {
	Lifetime struct {
		USD        float64            `json:"usd"`
		ByProvider map[string]float64 `json:"byProvider"`
	} `json:"lifetime"`
	Daily map[string]float64 `json:"daily"`
	// dailyOrder is Daily's keys in the stored JSON's order.
	dailyOrder []string
	ByCwd      map[string]float64 `json:"byCwd"`
}

func readBaseline(h *sql.DB) costBaseline {
	var b costBaseline
	var raw string
	if h.QueryRow("SELECT value FROM meta WHERE key = 'cost_baseline'").Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &b)
	}
	clean := func(m map[string]float64) map[string]float64 {
		out := map[string]float64{}
		for k, v := range m {
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				out[k] = v
			}
		}
		return out
	}
	b.Lifetime.ByProvider, b.Daily, b.ByCwd = clean(b.Lifetime.ByProvider), clean(b.Daily), clean(b.ByCwd)
	var ordered struct {
		Daily jsonstore.Object `json:"daily"`
	}
	if json.Unmarshal([]byte(raw), &ordered) == nil {
		for _, k := range ordered.Daily.Keys() {
			if _, ok := b.Daily[k]; ok {
				b.dailyOrder = append(b.dailyOrder, k)
			}
		}
	}
	return b
}

// CostStats is loop's roll-up: real billed spend, baseline included.
type CostStats struct {
	Lifetime   float64
	ByProvider map[string]float64
	Today      float64
	Last7      float64
	Month      float64
	Cwd        float64
}

// Stats computes the spend buckets. Windows walk back by calendar day, not
// 24-hour ticks, so a DST-shortened day cannot skip or double a date.
func Stats(cwd string) (CostStats, error) {
	h, err := db.Open()
	if err != nil {
		return CostStats{}, err
	}
	base := readBaseline(h)
	st := CostStats{ByProvider: map[string]float64{}}
	for p, usd := range base.Lifetime.ByProvider {
		st.ByProvider[p] = usd
	}
	life := 0.0

	rows, err := h.Query(`SELECT provider, SUM(usd) FROM cost_ledger
		WHERE estimated = 0 AND backfilled = 0 GROUP BY provider`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var p string
		var usd float64
		if rows.Scan(&p, &usd) == nil {
			st.ByProvider[p] += usd
			life += usd
		}
	}
	rows.Close()
	// loop's order: the ledger summed on its own, then the baseline added.
	st.Lifetime = base.Lifetime.USD + life

	// loop's Map order: ledger days as SQL returns them, then baseline-only
	// days; a day in both is ledger + baseline. Float sums are order-sensitive.
	daily := map[string]float64{}
	var order []string
	rows, err = h.Query(`SELECT day, SUM(usd) FROM cost_ledger
		WHERE estimated = 0 AND backfilled = 0 GROUP BY day`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var d string
		var usd float64
		if rows.Scan(&d, &usd) == nil {
			if _, seen := daily[d]; !seen {
				order = append(order, d)
			}
			daily[d] += usd
		}
	}
	rows.Close()
	for _, d := range base.dailyOrder {
		if _, seen := daily[d]; !seen {
			order = append(order, d)
		}
		daily[d] += base.Daily[d]
	}

	now := time.Now()
	today := DayKey(now)
	last7 := map[string]bool{}
	for i := 0; i < 7; i++ {
		last7[DayKey(now.AddDate(0, 0, -i))] = true
	}
	month := today[:7]
	for _, d := range order {
		usd := daily[d]
		if last7[d] {
			st.Last7 += usd
		}
		if strings.HasPrefix(d, month) {
			st.Month += usd
		}
	}
	st.Today = daily[today]
	if cwd != "" {
		var usd float64
		_ = h.QueryRow(`SELECT COALESCE(SUM(usd), 0) FROM cost_ledger
			WHERE cwd = ? AND estimated = 0 AND backfilled = 0`, cwd).Scan(&usd)
		st.Cwd = usd + base.ByCwd[cwd]
	}
	return st, nil
}

// Totals is every recorded session's tokens and spend, for /cost all.
type Totals struct {
	Sessions                  int
	InputTokens, OutputTokens int64
	CostUSD                   float64
}

// TotalUsage sums the whole ledger.
func TotalUsage() (Totals, error) {
	h, err := db.Open()
	if err != nil {
		return Totals{}, err
	}
	var t Totals
	err = h.QueryRow(`SELECT (SELECT COUNT(*) FROM sessions), COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0), COALESCE(SUM(usd), 0)
		FROM cost_ledger WHERE estimated = 0 AND backfilled = 0`).
		Scan(&t.Sessions, &t.InputTokens, &t.OutputTokens, &t.CostUSD)
	return t, err
}
