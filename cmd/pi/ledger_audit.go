package main

import (
	"database/sql"
	"math"
)

// The cost ledger's self-audit — loop's auditLedger (sessions/cost-ledger.ts),
// run against the shared agent.db.
//
// It lives beside its only caller rather than in internal/core/session
// because that package is being edited in parallel; it is plain SQL over
// loop's schema and moves there unchanged.

// priceViolation is a ledger row whose usd does not recompute from its
// snapshot prices.
type priceViolation struct {
	id            int64
	model         string
	usd, computed float64
}

// sessionMismatch is a session whose ledger token sums disagree with the
// usage recorded on its transcript entries.
type sessionMismatch struct {
	sessionPub                  string
	ledgerInput, entriesInput   int64
	ledgerOutput, entriesOutput int64
}

type ledgerAudit struct {
	rows              int64
	priceViolations   []priceViolation
	sessionMismatches []sessionMismatch
}

// Rounding allowance for the price check: an absolute floor for float
// accumulation, 0.1% relative for rounding in the stored figure.
const (
	auditAbsEpsilon = 1e-6
	auditRelEpsilon = 0.001
)

func auditLedger(h *sql.DB) (ledgerAudit, error) {
	var out ledgerAudit
	if err := h.QueryRow("SELECT COUNT(*) FROM cost_ledger").Scan(&out.rows); err != nil {
		return out, err
	}
	var err error
	if out.priceViolations, err = auditPrices(h); err != nil {
		return out, err
	}
	out.sessionMismatches, err = auditSessions(h)
	return out, err
}

// auditPrices recomputes every row priced from a snapshot. Rows billed at
// the provider's own figure, and stamp-priced backfill rows with no
// snapshot, are not recomputable and are skipped.
func auditPrices(h *sql.DB) ([]priceViolation, error) {
	rows, err := h.Query(`SELECT id, model, usd, no_cache_tokens, input_tokens, cache_read_tokens,
	        cache_write_tokens, output_tokens, price_input, price_output, price_cache_read, price_cache_write
	     FROM cost_ledger WHERE provider_cost IS NULL AND price_input IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []priceViolation
	for rows.Next() {
		var (
			v                              priceViolation
			noCache, cacheRead, cacheWrite sql.NullInt64
			input, output                  int64
			pIn, pOut, pRead, pWrite       sql.NullFloat64
		)
		if err := rows.Scan(&v.id, &v.model, &v.usd, &noCache, &input, &cacheRead, &cacheWrite, &output,
			&pIn, &pOut, &pRead, &pWrite); err != nil {
			return nil, err
		}
		uncached := noCache.Int64
		if !noCache.Valid {
			uncached = max(0, input-cacheRead.Int64-cacheWrite.Int64)
		}
		v.computed = float64(uncached)/1e6*pIn.Float64 + float64(output)/1e6*pOut.Float64 +
			float64(cacheRead.Int64)/1e6*pRead.Float64 + float64(cacheWrite.Int64)/1e6*pWrite.Float64
		if math.Abs(v.computed-v.usd) > math.Max(auditAbsEpsilon, v.usd*auditRelEpsilon) {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}

// auditSessions checks that per session, turn+subagent ledger token sums
// equal the token sums of its message/subagent entries. Compaction, branch
// summary and recap entries carry usage too, but their rows have their own
// sources and are left out on both sides.
func auditSessions(h *sql.DB) ([]sessionMismatch, error) {
	rows, err := h.Query(`SELECT l.session_pub,
	        SUM(l.input_tokens) AS l_in,
	        (SELECT COALESCE(SUM(e.usage_input), 0) FROM entries e
	         WHERE e.session_id = l.session_id AND e.usage_input IS NOT NULL
	           AND e.type IN ('message', 'subagent')) AS e_in,
	        SUM(l.output_tokens) AS l_out,
	        (SELECT COALESCE(SUM(e.usage_output), 0) FROM entries e
	         WHERE e.session_id = l.session_id AND e.usage_input IS NOT NULL
	           AND e.type IN ('message', 'subagent')) AS e_out
	     FROM cost_ledger l
	     WHERE l.source IN ('turn', 'subagent') AND l.session_id IS NOT NULL
	     GROUP BY l.session_pub, l.session_id
	     HAVING l_in != e_in OR l_out != e_out`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sessionMismatch
	for rows.Next() {
		var m sessionMismatch
		var pub sql.NullString
		if err := rows.Scan(&pub, &m.ledgerInput, &m.entriesInput, &m.ledgerOutput, &m.entriesOutput); err != nil {
			return nil, err
		}
		m.sessionPub = pub.String
		out = append(out, m)
	}
	return out, rows.Err()
}
