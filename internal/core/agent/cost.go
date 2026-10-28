package agent

import (
	"fmt"
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/core/session"
)

// loop's CostTracker (agent/cost.ts): a session's running total, billed into
// the ledger one round-trip at a time, and the lifetime/window roll-ups read
// back from it.

// CostBreakdown is loop's CostBreakdown — what `step-usage` carries and
// `cost.session` returns.
type CostBreakdown struct {
	InputTokens       int64   `json:"inputTokens"`
	OutputTokens      int64   `json:"outputTokens"`
	CachedInputTokens int64   `json:"cachedInputTokens"`
	USD               float64 `json:"usd"`
	Estimated         bool    `json:"estimated,omitempty"`
}

// CostStats is loop's CostStats (`cost.stats`).
type CostStats struct {
	LifetimeUSD float64            `json:"lifetimeUsd"`
	ByProvider  map[string]float64 `json:"byProvider"`
	TodayUSD    float64            `json:"todayUsd"`
	Last7USD    float64            `json:"last7Usd"`
	MonthUSD    float64            `json:"monthUsd"`
	CwdUSD      float64            `json:"cwdUsd"`
}

// Lifetime is loop's lifetimeBreakdown (`cost.lifetime`).
type Lifetime struct {
	USD        float64            `json:"usd"`
	ByProvider map[string]float64 `json:"byProvider"`
}

// CostTracker accumulates one session's spend.
type CostTracker struct {
	mu        sync.Mutex
	session   CostBreakdown
	estimated bool
	lastRowID int64
}

// NewCostTracker is an empty tracker.
func NewCostTracker() *CostTracker { return &CostTracker{} }

func providerOf(modelID string) string {
	p, _, _ := strings.Cut(modelID, "/")
	return p
}

// computeUSD trusts a stamped block over re-pricing (catalogs drift).
func computeUSD(modelID string, u session.Usage) float64 {
	if u.USD != nil {
		return *u.USD
	}
	if u.Cost != nil && providerOf(modelID) == "openrouter" {
		return *u.Cost
	}
	usd, _, _ := PriceUsage(modelID, u)
	return usd
}

func (t *CostTracker) accumulate(modelID string, u session.Usage) float64 {
	usd := computeUSD(modelID, u)
	n := u.Normalize()
	t.session.InputTokens += deref(u.InputTokens)
	t.session.OutputTokens += deref(u.OutputTokens)
	t.session.CachedInputTokens += deref(n.CacheRead)
	t.session.USD += usd
	return usd
}

// Add bills one round-trip: in-memory and a ledger row.
func (t *CostTracker) Add(modelID string, u session.Usage, ctx BillContext) CostBreakdown {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.accumulate(modelID, u)
	t.lastRowID, _ = Bill(modelID, u, ctx, false)
	return t.session
}

// AddEstimated bills an interrupted request's estimate (session total only).
func (t *CostTracker) AddEstimated(modelID string, u session.Usage, ctx BillContext) CostBreakdown {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.accumulate(modelID, u)
	t.lastRowID, _ = Bill(modelID, u, ctx, true)
	t.estimated = true
	out := t.session
	out.Estimated = true
	return out
}

// TakeLastLedgerRowID is the last add's ledger row, consumed.
func (t *CostTracker) TakeLastLedgerRowID() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	id := t.lastRowID
	t.lastRowID = 0
	return id
}

// SessionCost is sessionBreakdown(): the total with `estimated` always set.
type SessionCost struct {
	InputTokens       int64   `json:"inputTokens"`
	OutputTokens      int64   `json:"outputTokens"`
	CachedInputTokens int64   `json:"cachedInputTokens"`
	USD               float64 `json:"usd"`
	Estimated         bool    `json:"estimated"`
}

// SessionBreakdown is the running total.
func (t *CostTracker) SessionBreakdown() SessionCost {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.session
	return SessionCost{InputTokens: s.InputTokens, OutputTokens: s.OutputTokens, CachedInputTokens: s.CachedInputTokens, USD: s.USD, Estimated: t.estimated}
}

// Reset clears the session total.
func (t *CostTracker) Reset() {
	t.mu.Lock()
	t.session, t.estimated = CostBreakdown{}, false
	t.mu.Unlock()
}

// SeedFromSession rebuilds the total for a reopened session: the ledger's
// billed dollars when it has rows, the entries' stamped usage otherwise.
// Returns the last reply's context size for the meter.
func (t *CostTracker) SeedFromSession(s *session.Session) (ctxTokens int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rows := 0
	if sums, err := session.SumLedgerForSession(s.ID); err == nil && sums.Rows > 0 {
		rows = sums.Rows
		t.session = CostBreakdown{InputTokens: sums.InputTokens, OutputTokens: sums.OutputTokens,
			CachedInputTokens: sums.CachedInputTokens, USD: sums.USD}
		t.estimated = sums.Estimated
	}
	var last *session.Usage
	if rows == 0 {
		t.session, t.estimated = CostBreakdown{}, false
	}
	for _, e := range s.Entries() {
		switch {
		case e.Type == session.TypeMessage && e.Role == "assistant" && e.Usage != nil:
			if rows == 0 {
				model := e.Model
				if model == "" {
					model = s.Info.Model
				}
				t.accumulate(model, *e.Usage)
				if e.Usage.Estimated {
					t.estimated = true
				}
			}
			last = e.Usage
		case e.Type == session.TypeCompact:
			last = nil
		case e.Type == session.TypeSubagent && e.Usage != nil && rows == 0:
			model := e.Model
			if model == "" {
				model = s.Info.Model
			}
			t.accumulate(model, *e.Usage)
		}
	}
	return ContextTokensFromUsage(last)
}

// ContextTokensFromUsage is loop's contextTokensFromUsage: the block's total
// when it reports one, else input + output + legacy cached.
func ContextTokensFromUsage(u *session.Usage) int64 {
	if u == nil {
		return 0
	}
	if u.TotalTokens != nil && *u.TotalTokens > 0 {
		return *u.TotalTokens
	}
	return deref(u.InputTokens) + deref(u.OutputTokens) + deref(u.CachedInputTokens)
}

// Stats is the ledger roll-up (`cost.stats`).
func (t *CostTracker) Stats(cwd string) CostStats {
	st, _ := session.Stats(cwd)
	if st.ByProvider == nil {
		st.ByProvider = map[string]float64{}
	}
	return CostStats{LifetimeUSD: st.Lifetime, ByProvider: st.ByProvider, TodayUSD: st.Today,
		Last7USD: st.Last7, MonthUSD: st.Month, CwdUSD: st.Cwd}
}

// LifetimeBreakdown is every billed dollar, by provider.
func (t *CostTracker) LifetimeBreakdown() Lifetime {
	st := t.Stats("")
	return Lifetime{USD: st.LifetimeUSD, ByProvider: st.ByProvider}
}

// Format is loop's CostTracker.format: the status line's cost segment, with
// a leading ~ when the total includes an estimated (interrupted) amount.
func (c *CostTracker) Format() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := ""
	if c.estimated {
		prefix = "~"
	}
	s := c.session
	return fmt.Sprintf("%s$%.4f · in:%d out:%d cache:%d", prefix, s.USD, s.InputTokens, s.OutputTokens, s.CachedInputTokens)
}
