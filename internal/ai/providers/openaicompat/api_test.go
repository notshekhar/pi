package openaicompat

import "testing"

func TestCachedInputTokensSpellings(t *testing.T) {
	hit := int64(4)
	if got := (&apiUsage{PromptCacheHitTokens: &hit}).cachedInputTokens(); got != 4 {
		t.Errorf("deepseek spelling = %d", got)
	}
	if got := (&apiUsage{PromptTokensDetails: &struct {
		CachedTokens int64 `json:"cached_tokens"`
	}{CachedTokens: 3}}).cachedInputTokens(); got != 3 {
		t.Errorf("openai nested spelling = %d", got)
	}
	if got := (&apiUsage{CachedTokens: &hit}).cachedInputTokens(); got != 4 {
		t.Errorf("kimi top-level spelling = %d", got)
	}
}
