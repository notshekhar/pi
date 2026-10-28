package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
)

func TestCachedInputTokensSpellings(t *testing.T) {
	hit := int64(4)
	if got := (&apiUsage{PromptCacheHitTokens: &hit}).cachedInputTokens(); got != 4 {
		t.Errorf("deepseek spelling = %d", got)
	}
	if got := (&apiUsage{PromptTokensDetails: &promptTokensDetails{CachedTokens: 3}}).cachedInputTokens(); got != 3 {
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

// A cacheControl in the provider's options block becomes cache_control on
// the message's last content part (string content becomes a text part) —
// how OpenRouter takes Claude's breakpoints. Unmarked messages keep the
// plain string form.
func TestCacheControlMarksTheLastPart(t *testing.T) {
	mark := provider.ProviderOptions{"openrouter": {"cacheControl": map[string]any{"type": "ephemeral"}}}
	msgs, _, err := convertPrompt(provider.Prompt{
		provider.SystemMessage{Content: "sys", ProviderOptions: mark},
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "plain"}}},
		provider.ToolMessage{Content: []provider.ToolPart{
			provider.ToolResultPart{ToolCallID: "a", Output: provider.ToolOutputText{Value: "one"}},
			provider.ToolResultPart{ToolCallID: "b", Output: provider.ToolOutputText{Value: "two"}},
		}, ProviderOptions: mark},
	}, "openrouter", replayNever)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(msgs)
	want := `[{"role":"system","content":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"user","content":"plain"},{"role":"tool","content":"one","tool_call_id":"a"},` +
		`{"role":"tool","content":[{"type":"text","text":"two","cache_control":{"type":"ephemeral"}}],"tool_call_id":"b"}]`
	if string(raw) != want {
		t.Errorf("messages =\n%s\nwant\n%s", raw, want)
	}
	// Another provider's key is not this one's.
	other, _, _ := convertPrompt(provider.Prompt{provider.SystemMessage{Content: "sys", ProviderOptions: mark}}, "xai", replayNever)
	if _, ok := other[0].Content.(string); !ok {
		t.Error("a mark for openrouter was applied by another provider")
	}
}

// OpenRouter reports an explicit cache write beside the read (a real Claude
// frame): it is split out of the fresh input so it bills at the write price.
func TestUsageCacheWriteTokens(t *testing.T) {
	u := usageOf(t, `{"prompt_tokens":3017,"completion_tokens":5,"total_tokens":3022,
		"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":3009}}`)
	in := u.InputTokens
	if *in.Total != 3017 || *in.CacheWrite != 3009 || *in.NoCache != 8 || *in.CacheRead != 0 {
		t.Errorf("input = %d (write %v, fresh %d, read %d), want 3017 (3009, 8, 0)",
			*in.Total, in.CacheWrite, *in.NoCache, *in.CacheRead)
	}
}

// Reasoning goes back as reasoning_content when the provider replays it:
// present when the turn had some, always (empty) for a model that requires
// it, and never by default.
func TestReasoningReplay(t *testing.T) {
	prompt := provider.Prompt{
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "hi"}}},
		provider.AssistantMessage{Content: []provider.AssistantPart{provider.ReasoningPart{Text: "think"}, provider.TextPart{Text: "a"}}},
		provider.AssistantMessage{Content: []provider.AssistantPart{provider.TextPart{Text: "b"}}},
	}
	for replay, want := range map[reasoningReplay][2]string{
		replayNever:   {"", ""},
		replayPresent: {`"reasoning_content":"think"`, ""},
		replayAlways:  {`"reasoning_content":"think"`, `"reasoning_content":""`},
	} {
		msgs, _, err := convertPrompt(prompt, "deepseek", replay)
		if err != nil {
			t.Fatal(err)
		}
		for i, w := range want {
			raw, _ := json.Marshal(msgs[i+1])
			got := strings.Contains(string(raw), "reasoning_content")
			if (w == "") == got && !(w != "" && strings.Contains(string(raw), w)) {
				t.Errorf("replay %d, turn %d: %s", replay, i, raw)
			}
			if w != "" && !strings.Contains(string(raw), w) {
				t.Errorf("replay %d, turn %d: %s, want %s", replay, i, raw, w)
			}
		}
	}
}
