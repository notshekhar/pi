package agent

import (
	"strings"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/catalog"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
)

// Billing, ported from loop's agent/cost.ts: a usage block is priced against
// the catalog with the model that PRODUCED it, the dollars are stamped onto
// the block before it is stored (so a session keeps its true historical cost
// across model switches and catalog updates), and every billed round-trip
// gets a ledger row.

// ToUsage converts the AI layer's usage into loop's stored UsageBlock.
func ToUsage(u provider.Usage) session.Usage {
	out := session.Usage{
		InputTokens:  u.InputTokens.Total,
		OutputTokens: u.OutputTokens.Total,
	}
	if u.InputTokens.NoCache != nil || u.InputTokens.CacheRead != nil || u.InputTokens.CacheWrite != nil {
		out.InputTokenDetails = &session.InputTokenDetails{
			NoCacheTokens: u.InputTokens.NoCache, CacheReadTokens: u.InputTokens.CacheRead,
			CacheWriteTokens: u.InputTokens.CacheWrite,
		}
	}
	if u.OutputTokens.Text != nil || u.OutputTokens.Reasoning != nil {
		out.OutputTokenDetails = &session.OutputTokenDetails{
			TextTokens: u.OutputTokens.Text, ReasoningTokens: u.OutputTokens.Reasoning,
		}
	}
	if u.InputTokens.Total != nil || u.OutputTokens.Total != nil {
		total := deref(u.InputTokens.Total) + deref(u.OutputTokens.Total)
		out.TotalTokens = &total
	}
	return out
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// modelInfo resolves a full "provider/model" id in the catalog.
func modelInfo(modelID string) (catalog.Model, bool) {
	prov, short, _ := strings.Cut(modelID, "/")
	return config.ModelInfo(prov, short)
}

// OutputUSD is loop's outputUsd: reasoning-rate models bill the reasoning
// share at their reasoning price, the text share at the output price.
func OutputUSD(cost catalog.Cost, u session.Usage) float64 {
	n := u.Normalize()
	out := deref(n.Output)
	reasoning := deref(n.Reasoning)
	if cost.Reasoning == nil || reasoning <= 0 {
		return float64(float64(out) / 1e6 * cost.Output)
	}
	text := max(0, out-reasoning)
	if n.Text != nil {
		text = *n.Text
	}
	// float64(…) rounds each product, so arm64 cannot fuse it into the sum
	// (an FMA differs from JS in the last bit).
	return float64(float64(text)/1e6*cost.Output) + float64(float64(reasoning)/1e6**cost.Reasoning)
}

// PriceUsage prices a block (loop's priceUsage); ok is false for a model the
// catalog does not know, which callers treat as unpriced rather than free.
func PriceUsage(modelID string, u session.Usage) (usd float64, details session.USDDetails, ok bool) {
	m, found := modelInfo(modelID)
	if !found {
		return 0, details, false
	}
	n := u.Normalize()
	in := deref(n.Input)
	cacheRead := deref(n.CacheRead)
	cacheWrite := deref(n.CacheWrite)
	billedIn := max(0, in-cacheRead-cacheWrite)
	if n.NoCache != nil {
		billedIn = *n.NoCache
	}
	details = session.USDDetails{
		Input:      float64(float64(billedIn) / 1e6 * m.Cost.Input),
		Output:     OutputUSD(m.Cost, u),
		CacheRead:  float64(float64(cacheRead) / 1e6 * m.Cost.CacheRead),
		CacheWrite: float64(float64(cacheWrite) / 1e6 * m.Cost.CacheWrite),
	}
	return details.Input + details.Output + details.CacheRead + details.CacheWrite, details, true
}

// StampUsage stamps the billed dollars onto a block (loop's stampUsageCost).
// OpenRouter's own reported cost wins when present; an unknown model stays
// unstamped so a machine that does know it can still price it later.
func StampUsage(modelID string, u session.Usage) session.Usage {
	usd, details, ok := PriceUsage(modelID, u)
	prov, _, _ := strings.Cut(modelID, "/")
	if prov == "openrouter" && u.Cost != nil {
		v := *u.Cost
		u.USD = &v
	} else if ok {
		u.USD = &usd
	} else {
		return u
	}
	if ok {
		d := details
		u.USDDetails = &d
	}
	return u
}

// BillContext is where a billed round-trip is filed.
type BillContext struct {
	Source     session.LedgerSource
	CWD        string
	SessionPub string
}

// Bill appends a ledger row for one round-trip and returns its id and the
// dollars billed. A failed write never breaks the turn that ran.
func Bill(modelID string, u session.Usage, ctx BillContext, estimated bool) (int64, float64) {
	prov, _, _ := strings.Cut(modelID, "/")
	usd, _, _ := PriceUsage(modelID, u)
	var providerCost *float64
	if prov == "openrouter" && u.Cost != nil {
		v := *u.Cost
		providerCost, usd = &v, v
	}
	var prices session.Prices
	if m, ok := modelInfo(modelID); ok {
		prices = session.Prices{Input: &m.Cost.Input, Output: &m.Cost.Output,
			CacheRead: &m.Cost.CacheRead, CacheWrite: &m.Cost.CacheWrite}
	}
	if ctx.Source == "" {
		ctx.Source = session.SourceTurn
	}
	id, err := session.AddLedgerRow(session.LedgerRow{
		Provider: prov, Model: modelID, Usage: u, USD: usd, ProviderCost: providerCost,
		Prices: prices, Estimated: estimated, Source: ctx.Source, CWD: ctx.CWD, SessionPub: ctx.SessionPub,
	})
	if err != nil {
		return 0, usd
	}
	return id, usd
}
