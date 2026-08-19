package bedrock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/jsonschema"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

func testProvider(t *testing.T, handler http.HandlerFunc) (*Provider, *[]byte, *string) {
	t.Helper()

	var sent []byte
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		path = r.URL.RequestURI()
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	p := New(Options{
		Region:      "us-east-1",
		BaseURL:     srv.URL,
		Credentials: StaticCredentials{AccessKeyID: "AKIATEST", SecretAccessKey: "secret"},
		Retry:       providerutil.RetryConfig{MaxRetries: 0},
	})
	return p, &sent, &path
}

func jsonHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
}

func userPrompt(text string) provider.Prompt {
	return provider.Prompt{
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: text}}},
	}
}

const generateTextBody = `{
  "output": {"message": {"role": "assistant", "content": [{"text": "Hello, world"}]}},
  "stopReason": "end_turn",
  "usage": {"inputTokens": 10, "outputTokens": 5, "cacheReadInputTokens": 3}
}`

func TestGenerateText(t *testing.T) {
	p, _, path := testProvider(t, jsonHandler(generateTextBody))

	res, err := p.LanguageModel("anthropic.claude-sonnet-4-5-20250929-v1:0").DoGenerate(
		context.Background(), provider.CallOptions{Prompt: userPrompt("hi")})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(*path, "/model/") || !strings.HasSuffix(*path, "/converse") {
		t.Errorf("path = %q", *path)
	}
	if !strings.Contains(*path, "anthropic.claude-sonnet-4-5-20250929-v1") {
		t.Errorf("model id missing from path %q", *path)
	}
	if len(res.Content) != 1 {
		t.Fatalf("content = %#v", res.Content)
	}
	text, ok := res.Content[0].(provider.Text)
	if !ok || text.Text != "Hello, world" {
		t.Errorf("text = %#v", res.Content[0])
	}
	if res.FinishReason.Unified != provider.FinishStop {
		t.Errorf("finish = %q", res.FinishReason.Unified)
	}
	if got := *res.Usage.InputTokens.Total; got != 13 {
		t.Errorf("input total = %d, want 13 (10 + 3 cached)", got)
	}
	if got := *res.Usage.InputTokens.NoCache; got != 10 {
		t.Errorf("noCache = %d", got)
	}
}

func TestGenerateToolCall(t *testing.T) {
	body := `{
	  "output": {"message": {"role": "assistant", "content": [
	    {"toolUse": {"toolUseId": "c1", "name": "read_file", "input": {"path": "/tmp/a"}}}
	  ]}},
	  "stopReason": "tool_use",
	  "usage": {"inputTokens": 8, "outputTokens": 4}
	}`
	p, sent, _ := testProvider(t, jsonHandler(body))

	res, err := p.LanguageModel("anthropic.claude-sonnet-4-5").DoGenerate(context.Background(), provider.CallOptions{
		Prompt: userPrompt("read"),
		Tools: []provider.Tool{provider.FunctionTool{
			Name:        "read_file",
			Description: "Read a file",
			InputSchema: &jsonschema.Schema{Type: jsonschema.Object},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	call, ok := res.Content[0].(provider.ToolCall)
	if !ok {
		t.Fatalf("content = %#v", res.Content[0])
	}
	if call.ToolName != "read_file" || call.ToolCallID != "c1" {
		t.Errorf("call = %+v", call)
	}
	if !strings.Contains(call.Input, "/tmp/a") {
		t.Errorf("input = %s", call.Input)
	}
	if res.FinishReason.Unified != provider.FinishToolCalls {
		t.Errorf("finish = %q", res.FinishReason.Unified)
	}

	var req converseRequest
	if err := json.Unmarshal(*sent, &req); err != nil {
		t.Fatal(err)
	}
	if req.ToolConfig == nil || len(req.ToolConfig.Tools) != 1 {
		t.Fatalf("tools = %+v", req.ToolConfig)
	}
}

func TestStructuredOutputUsesJSONTool(t *testing.T) {
	body := `{
	  "output": {"message": {"role": "assistant", "content": [
	    {"toolUse": {"toolUseId": "j1", "name": "json", "input": {"ok": true}}}
	  ]}},
	  "stopReason": "tool_use",
	  "usage": {"inputTokens": 4, "outputTokens": 2}
	}`
	p, sent, _ := testProvider(t, jsonHandler(body))

	res, err := p.LanguageModel("amazon.nova-pro-v1:0").DoGenerate(context.Background(), provider.CallOptions{
		Prompt: userPrompt("shape"),
		ResponseFormat: &provider.ResponseFormat{
			Type:   "json",
			Schema: &jsonschema.Schema{Type: jsonschema.Object},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	text, ok := res.Content[0].(provider.Text)
	if !ok || !strings.Contains(text.Text, `"ok"`) {
		t.Errorf("content = %#v, want the json tool rewritten as text", res.Content)
	}
	if res.FinishReason.Unified != provider.FinishStop {
		t.Errorf("finish = %q, want stop (json tool is not a real tool call)", res.FinishReason.Unified)
	}

	var req converseRequest
	if err := json.Unmarshal(*sent, &req); err != nil {
		t.Fatal(err)
	}
	if req.ToolConfig == nil || req.ToolConfig.Tools[0].ToolSpec.Name != jsonToolName {
		t.Fatalf("expected forced json tool, got %+v", req.ToolConfig)
	}
}

func TestNativeStructuredOutputOnSonnet45(t *testing.T) {
	p, sent, _ := testProvider(t, jsonHandler(generateTextBody))

	_, err := p.LanguageModel("anthropic.claude-sonnet-4-5").DoGenerate(context.Background(), provider.CallOptions{
		Prompt: userPrompt("shape"),
		ResponseFormat: &provider.ResponseFormat{
			Type:   "json",
			Schema: &jsonschema.Schema{Type: jsonschema.Object},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var req converseRequest
	if err := json.Unmarshal(*sent, &req); err != nil {
		t.Fatal(err)
	}
	cfg, _ := req.AdditionalModelRequestFields["output_config"].(map[string]any)
	format, _ := cfg["format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Fatalf("output_config = %#v, want native json_schema", cfg)
	}
	if req.ToolConfig != nil {
		t.Error("native structured output must not also send a json tool")
	}
}

func TestThinkingOnClaude45UsesBudget(t *testing.T) {
	p, sent, _ := testProvider(t, jsonHandler(generateTextBody))

	limit := int64(10000)
	_, err := p.LanguageModel("us.anthropic.claude-sonnet-4-5-20250929-v1:0").DoGenerate(
		context.Background(), provider.CallOptions{
			Prompt:          userPrompt("hi"),
			Reasoning:       provider.ReasoningMedium,
			MaxOutputTokens: &limit,
		})
	if err != nil {
		t.Fatal(err)
	}

	var req converseRequest
	if err := json.Unmarshal(*sent, &req); err != nil {
		t.Fatal(err)
	}
	thinking, _ := req.AdditionalModelRequestFields["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking = %#v", thinking)
	}
	budget, ok := toInt64(thinking["budget_tokens"])
	if !ok || budget != 3000 {
		t.Errorf("budget = %v, want 3000 (30%% of 10000)", thinking["budget_tokens"])
	}
}

func TestThinkingOnClaude5UsesAdaptive(t *testing.T) {
	p, sent, _ := testProvider(t, jsonHandler(generateTextBody))

	_, err := p.LanguageModel("anthropic.claude-opus-5").DoGenerate(
		context.Background(), provider.CallOptions{
			Prompt:    userPrompt("hi"),
			Reasoning: provider.ReasoningHigh,
		})
	if err != nil {
		t.Fatal(err)
	}

	var req converseRequest
	if err := json.Unmarshal(*sent, &req); err != nil {
		t.Fatal(err)
	}
	thinking, _ := req.AdditionalModelRequestFields["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" {
		t.Fatalf("thinking = %#v", thinking)
	}
	cfg, _ := req.AdditionalModelRequestFields["output_config"].(map[string]any)
	if cfg["effort"] != "high" {
		t.Errorf("effort = %#v", cfg)
	}
	if req.InferenceConfig != nil && req.InferenceConfig.Temperature != nil {
		t.Error("opus-5 must not receive temperature")
	}
}

func TestTemperatureClamped(t *testing.T) {
	p, sent, _ := testProvider(t, jsonHandler(generateTextBody))

	hot := 1.5
	res, err := p.LanguageModel("amazon.nova-pro-v1:0").DoGenerate(
		context.Background(), provider.CallOptions{
			Prompt:      userPrompt("hi"),
			Temperature: &hot,
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected a clamp warning")
	}

	var req converseRequest
	if err := json.Unmarshal(*sent, &req); err != nil {
		t.Fatal(err)
	}
	if req.InferenceConfig == nil || req.InferenceConfig.Temperature == nil || *req.InferenceConfig.Temperature != 1 {
		t.Errorf("temperature = %+v", req.InferenceConfig)
	}
}

func TestHostedToolWarns(t *testing.T) {
	p, sent, _ := testProvider(t, jsonHandler(generateTextBody))

	res, err := p.LanguageModel("anthropic.claude-sonnet-4-5").DoGenerate(
		context.Background(), provider.CallOptions{
			Prompt: userPrompt("search"),
			Tools:  []provider.Tool{provider.ProviderTool{ID: "anthropic.web_search", Name: "web_search"}},
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected a hosted-tool warning")
	}

	var req converseRequest
	if err := json.Unmarshal(*sent, &req); err != nil {
		t.Fatal(err)
	}
	if req.ToolConfig != nil {
		t.Errorf("hosted tool must not be sent, got %+v", req.ToolConfig)
	}
}

func TestEmptyPromptRejected(t *testing.T) {
	p, _, _ := testProvider(t, jsonHandler(generateTextBody))

	_, err := p.LanguageModel("anthropic.claude-sonnet-4-5").DoGenerate(
		context.Background(), provider.CallOptions{
			Prompt: provider.Prompt{provider.SystemMessage{Content: "only system"}},
		})
	if err == nil {
		t.Fatal("expected an error for a system-only prompt")
	}
}

func TestModelCapabilities(t *testing.T) {
	opus5 := modelCapabilities("us.anthropic.claude-opus-5-20260301-v1:0")
	if !opus5.IsAnthropic || !opus5.SupportsAdaptiveThinking || !opus5.RejectsSamplingParameters {
		t.Errorf("opus-5 = %+v", opus5)
	}
	if !opus5.RejectsNewerSchemaFields {
		t.Error("opus-5 should reject output_config.format")
	}

	sonnet45 := modelCapabilities("anthropic.claude-sonnet-4-5-20250929-v1:0")
	if !sonnet45.SupportsStructuredOutput || sonnet45.SupportsAdaptiveThinking {
		t.Errorf("sonnet-4-5 = %+v", sonnet45)
	}

	mistral := modelCapabilities("mistral.mistral-large-2407-v1:0")
	if !mistral.IsMistral || mistral.IsAnthropic {
		t.Errorf("mistral = %+v", mistral)
	}
}
