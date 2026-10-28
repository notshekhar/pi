package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
)

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

func usageOf(t *testing.T, raw string) provider.Usage {
	t.Helper()
	var u apiUsage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	return convertUsage(&u)
}

// xAI's chat completions report reasoning beside completion_tokens (a real
// grok-4.7 frame): output is their sum, and the cache read is split out.
func TestUsageReasoningOutsideCompletionTokens(t *testing.T) {
	u := usageOf(t, `{"prompt_tokens":1258,"completion_tokens":1,"total_tokens":1317,
		"prompt_tokens_details":{"text_tokens":1258,"cached_tokens":1152},
		"completion_tokens_details":{"reasoning_tokens":58}}`)
	if *u.OutputTokens.Total != 59 || *u.OutputTokens.Reasoning != 58 || *u.OutputTokens.Text != 1 {
		t.Errorf("output = %d (reasoning %d, text %d), want 59 (58, 1)",
			*u.OutputTokens.Total, *u.OutputTokens.Reasoning, *u.OutputTokens.Text)
	}
	if *u.InputTokens.Total != 1258 || *u.InputTokens.CacheRead != 1152 || *u.InputTokens.NoCache != 106 {
		t.Errorf("input = %d (cached %d, fresh %d), want 1258 (1152, 106)",
			*u.InputTokens.Total, *u.InputTokens.CacheRead, *u.InputTokens.NoCache)
	}
}

// OpenAI's own convention counts reasoning inside completion_tokens.
func TestUsageReasoningInsideCompletionTokens(t *testing.T) {
	u := usageOf(t, `{"prompt_tokens":100,"completion_tokens":80,"total_tokens":180,
		"prompt_tokens_details":{"cached_tokens":64},"completion_tokens_details":{"reasoning_tokens":50}}`)
	if *u.OutputTokens.Total != 80 || *u.OutputTokens.Text != 30 {
		t.Errorf("output = %d (text %d), want 80 (30)", *u.OutputTokens.Total, *u.OutputTokens.Text)
	}
	if *u.InputTokens.NoCache != 36 {
		t.Errorf("fresh input = %d, want 36", *u.InputTokens.NoCache)
	}
}

// A cache count larger than prompt_tokens means the gateway counted it
// outside the prompt; nothing goes negative.
func TestUsageCacheOutsidePromptTokens(t *testing.T) {
	u := usageOf(t, `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":900}}`)
	if *u.InputTokens.Total != 910 || *u.InputTokens.NoCache != 10 || *u.InputTokens.CacheRead != 900 {
		t.Errorf("input = %d (fresh %d, cached %d), want 910 (10, 900)",
			*u.InputTokens.Total, *u.InputTokens.NoCache, *u.InputTokens.CacheRead)
	}
}
