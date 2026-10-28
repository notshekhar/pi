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

// Mistral's reasoning models stream content as parts; thinking surfaces as
// reasoning instead of failing the chunk.
func TestContentPartsDecode(t *testing.T) {
	var m apiMessageOut
	if err := json.Unmarshal([]byte(`{"content":[{"type":"thinking","thinking":[{"type":"text","text":"hmm"}]},{"type":"text","text":"ok"}]}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Content != "ok" || m.reasoningText() != "hmm" {
		t.Errorf("content %q reasoning %q", m.Content, m.reasoningText())
	}
	if err := json.Unmarshal([]byte(`{"content":"plain","tool_calls":[{"id":"x","type":"function","function":{"name":"f","arguments":"{}"}}]}`), &m); err != nil || m.Content != "plain" || len(m.ToolCalls) != 1 {
		t.Errorf("string content: %v %+v", err, m)
	}
}

// @ai-sdk/mistral's request: random_seed, tool_choice any (narrowed to a
// named tool), its effort table, reasoning replayed as thinking parts, a
// trailing assistant turn as a prefix, tool results named.
func TestMistralRequest(t *testing.T) {
	p := New(Options{Name: "mistral", APIKey: "k", Mistral: true})
	m := p.LanguageModel("magistral-medium-latest").(*languageModel)
	seed := int64(7)
	opts := provider.CallOptions{
		Seed: &seed, Reasoning: provider.ReasoningLow,
		Tools:      []provider.Tool{provider.FunctionTool{Name: "a"}, provider.FunctionTool{Name: "b"}},
		ToolChoice: &provider.ToolChoice{Type: provider.ToolChoiceTool, ToolName: "b"},
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "q"}}},
			provider.AssistantMessage{Content: []provider.AssistantPart{provider.ReasoningPart{Text: "t"}, provider.ToolCallPart{ToolCallID: "c1", ToolName: "b", Input: map[string]any{}}}},
			provider.ToolMessage{Content: []provider.ToolPart{provider.ToolResultPart{ToolCallID: "c1", ToolName: "b", Output: provider.ToolOutputText{Value: "r"}}}},
			provider.AssistantMessage{Content: []provider.AssistantPart{provider.TextPart{Text: "partial"}}},
		},
	}
	req, _, err := m.buildRequest(opts, true)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := marshalRequest(req)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["random_seed"] != float64(7) || got["seed"] != nil || got["tool_choice"] != "any" || got["reasoning_effort"] != "high" {
		t.Errorf("args: seed %v/%v choice %v effort %v", got["random_seed"], got["seed"], got["tool_choice"], got["reasoning_effort"])
	}
	if tools := got["tools"].([]any); len(tools) != 1 {
		t.Errorf("tools not narrowed: %v", tools)
	}
	msgs := got["messages"].([]any)
	thinking, _ := json.Marshal(msgs[1].(map[string]any)["content"])
	if string(thinking) != `[{"closed":true,"thinking":[{"text":"t","type":"text"}],"type":"thinking"}]` {
		t.Errorf("reasoned turn = %s", thinking)
	}
	if msgs[2].(map[string]any)["name"] != "b" || msgs[3].(map[string]any)["prefix"] != true {
		t.Errorf("tool name %v, prefix %v", msgs[2], msgs[3])
	}
	if e, _ := mistralEffort("zai-glm-5", provider.ReasoningXHigh); e != "max" {
		t.Errorf("glm xhigh = %q", e)
	}
	if e, _ := mistralEffort("codestral-latest", provider.ReasoningHigh); e != "" {
		t.Errorf("codestral effort = %q", e)
	}
}

func bodyOf(t *testing.T, p *Provider, model string, opts provider.CallOptions) map[string]any {
	t.Helper()
	req, _, err := p.LanguageModel(model).(*languageModel).buildRequest(opts, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := marshalRequest(req)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// @ai-sdk/deepseek: thinking follows the effort (or the caller's toggle),
// efforts are low/high/max and only while thinking, sampling is dropped while
// thinking, and V4 turns always carry reasoning_content.
func TestDeepSeekDialect(t *testing.T) {
	p := New(Options{Name: "deepseek", APIKey: "k", DeepSeek: true, ReplayReasoning: true})
	temp := 0.5
	prompt := provider.Prompt{
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "q"}}},
		provider.AssistantMessage{Content: []provider.AssistantPart{provider.TextPart{Text: "a"}}},
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "q2"}}},
	}
	b := bodyOf(t, p, "deepseek-v4-flash", provider.CallOptions{Prompt: prompt, Reasoning: provider.ReasoningMedium, Temperature: &temp})
	if th, _ := b["thinking"].(map[string]any); th["type"] != "enabled" || b["reasoning_effort"] != "high" || b["temperature"] != nil {
		t.Errorf("v4 medium: thinking %v effort %v temperature %v", b["thinking"], b["reasoning_effort"], b["temperature"])
	}
	if msg := b["messages"].([]any)[1].(map[string]any); msg["reasoning_content"] != "" {
		t.Errorf("v4 turn without reasoning_content: %v", msg)
	}
	b = bodyOf(t, p, "deepseek-v4-flash", provider.CallOptions{Prompt: prompt, Reasoning: provider.ReasoningNone})
	if th, _ := b["thinking"].(map[string]any); th["type"] != "disabled" || b["reasoning_effort"] != nil {
		t.Errorf("off: thinking %v effort %v", b["thinking"], b["reasoning_effort"])
	}
	// Kimi's toggle comes in through provider options and wins.
	kimi := New(Options{Name: "kimi", APIKey: "k", DeepSeek: true, ReplayReasoning: true})
	b = bodyOf(t, kimi, "kimi-k3", provider.CallOptions{Prompt: prompt,
		ProviderOptions: provider.ProviderOptions{"kimi": {"thinking": map[string]any{"type": "enabled"}}}})
	if th, _ := b["thinking"].(map[string]any); th["type"] != "enabled" || b["reasoning_effort"] != nil {
		t.Errorf("kimi: thinking %v effort %v", b["thinking"], b["reasoning_effort"])
	}
}

// @ai-sdk/groq's effort map and Cerebras's max_completion_tokens; both
// replay reasoning as "reasoning".
func TestGroqAndCerebrasDialects(t *testing.T) {
	groq := New(Options{Name: "groq", APIKey: "k", Effort: GroqEffort, ReplayReasoning: true, ReasoningField: "reasoning"})
	prompt := provider.Prompt{
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "q"}}},
		provider.AssistantMessage{Content: []provider.AssistantPart{provider.ReasoningPart{Text: "r"}, provider.TextPart{Text: "a"}}},
	}
	b := bodyOf(t, groq, "openai/gpt-oss-120b", provider.CallOptions{Prompt: prompt, Reasoning: provider.ReasoningXHigh})
	msg := b["messages"].([]any)[1].(map[string]any)
	if b["reasoning_effort"] != "high" || msg["reasoning"] != "r" || msg["reasoning_content"] != nil {
		t.Errorf("groq: effort %v, turn %v", b["reasoning_effort"], msg)
	}
	if b = bodyOf(t, groq, "openai/gpt-oss-120b", provider.CallOptions{Prompt: prompt, Reasoning: provider.ReasoningNone}); b["reasoning_effort"] != nil {
		t.Errorf("groq none on a model without it: %v", b["reasoning_effort"])
	}
	n := int64(100)
	cerebras := New(Options{Name: "cerebras", APIKey: "k", UseMaxCompletionTokens: true, ReplayReasoning: true, ReasoningField: "reasoning"})
	b = bodyOf(t, cerebras, "gpt-oss-120b", provider.CallOptions{Prompt: prompt, MaxOutputTokens: &n, Reasoning: provider.ReasoningXHigh})
	if b["max_completion_tokens"] != float64(100) || b["max_tokens"] != nil || b["reasoning_effort"] != "xhigh" {
		t.Errorf("cerebras: %v %v %v", b["max_completion_tokens"], b["max_tokens"], b["reasoning_effort"])
	}
}
