package session

import "encoding/json"

// Usage is loop's UsageBlock: what one billed round-trip consumed, in the
// AI SDK v7 shape, plus the dollars it was billed at.
//
// Fields loop writes that pi has no use for (`raw`, the provider's own usage
// payload) are kept in Extra and written back untouched.
type Usage struct {
	InputTokens  *int64 `json:"inputTokens,omitempty"`
	OutputTokens *int64 `json:"outputTokens,omitempty"`
	TotalTokens  *int64 `json:"totalTokens,omitempty"`
	// Flat v6 fields: readers prefer the nested details and fall back to these.
	CachedInputTokens *int64   `json:"cachedInputTokens,omitempty"`
	ReasoningTokens   *int64   `json:"reasoningTokens,omitempty"`
	Cost              *float64 `json:"cost,omitempty"`

	InputTokenDetails  *InputTokenDetails  `json:"inputTokenDetails,omitempty"`
	OutputTokenDetails *OutputTokenDetails `json:"outputTokenDetails,omitempty"`

	// Estimated marks a guess, not a provider report (an interrupted turn's
	// in-flight request). /cost never bills these.
	Estimated bool `json:"estimated,omitempty"`
	// USD is what the block was billed at when it ran.
	USD        *float64    `json:"usd,omitempty"`
	USDDetails *USDDetails `json:"usdDetails,omitempty"`

	Raw json.RawMessage `json:"raw,omitempty"`
}

// InputTokenDetails splits input by how it was billed.
type InputTokenDetails struct {
	NoCacheTokens    *int64 `json:"noCacheTokens,omitempty"`
	CacheReadTokens  *int64 `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens *int64 `json:"cacheWriteTokens,omitempty"`
}

// OutputTokenDetails splits output into text and reasoning.
type OutputTokenDetails struct {
	TextTokens      *int64 `json:"textTokens,omitempty"`
	ReasoningTokens *int64 `json:"reasoningTokens,omitempty"`
}

// USDDetails is the billed dollars by component.
type USDDetails struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// NormalizedUsage is loop's normalizeUsage: one flat view, with the v7
// nested details preferred over the v6 flat fields. Cost math, the derived
// entry columns and display all read usage through it.
type NormalizedUsage struct {
	Input, Output, Total, NoCache, CacheRead, CacheWrite, Text, Reasoning *int64
	Cost, USD                                                             *float64
	Estimated                                                             bool
}

// Normalize flattens the block.
func (u Usage) Normalize() NormalizedUsage {
	n := NormalizedUsage{
		Input: u.InputTokens, Output: u.OutputTokens, Total: u.TotalTokens,
		Cost: u.Cost, USD: u.USD, Estimated: u.Estimated,
	}
	if d := u.InputTokenDetails; d != nil {
		n.NoCache, n.CacheRead, n.CacheWrite = d.NoCacheTokens, d.CacheReadTokens, d.CacheWriteTokens
	}
	if n.CacheRead == nil {
		n.CacheRead = u.CachedInputTokens
	}
	if d := u.OutputTokenDetails; d != nil {
		n.Text, n.Reasoning = d.TextTokens, d.ReasoningTokens
	}
	if n.Reasoning == nil {
		n.Reasoning = u.ReasoningTokens
	}
	return n
}

func i64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func f64(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func ptr[T any](v T) *T { return &v }
