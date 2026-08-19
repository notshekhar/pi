package google

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
)

// newTestProvider serves a canned body and captures the request.
func newTestProvider(t *testing.T, body string) (*Provider, *[]byte, *string) {
	t.Helper()

	var sent []byte
	var path string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		path = r.URL.RequestURI()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return New(Options{APIKey: "test", BaseURL: srv.URL}), &sent, &path
}

func collect(res *provider.StreamResult) []provider.StreamPart {
	var parts []provider.StreamPart
	for p := range res.Stream {
		parts = append(parts, p)
	}
	return parts
}

func userPrompt(text string) provider.Prompt {
	return provider.Prompt{
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: text}}},
	}
}

const textStream = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}],"modelVersion":"gemini-3-pro","responseId":"r1"}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":", world"}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15,"cachedContentTokenCount":3}}

`

func TestStreamText(t *testing.T) {
	p, _, path := newTestProvider(t, textStream)

	res, err := p.LanguageModel("gemini-3-pro").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("hi"),
	})
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	var finish *provider.Finish
	for _, part := range collect(res) {
		switch v := part.(type) {
		case provider.TextDelta:
			text.WriteString(v.Delta)
		case provider.Finish:
			finish = &v
		case provider.ErrorPart:
			t.Fatalf("unexpected error: %v", v.Err)
		}
	}

	if text.String() != "Hello, world" {
		t.Errorf("text = %q", text.String())
	}
	// Without alt=sse the endpoint returns a JSON array, not events.
	if !strings.Contains(*path, "alt=sse") {
		t.Errorf("streaming path = %q, want alt=sse", *path)
	}
	if !strings.Contains(*path, "models/gemini-3-pro:streamGenerateContent") {
		t.Errorf("path = %q", *path)
	}
	if finish.FinishReason.Unified != provider.FinishStop {
		t.Errorf("finish = %q", finish.FinishReason.Unified)
	}
	if got := *finish.Usage.InputTokens.NoCache; got != 7 {
		t.Errorf("uncached input = %d, want 7 (10 - 3 cached)", got)
	}
}

const toolStream = `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"path":"/tmp/a"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":4}}

`

func TestFinishReasonIsToolCallsDespiteStop(t *testing.T) {
	p, _, _ := newTestProvider(t, toolStream)

	res, err := p.LanguageModel("gemini-3-pro").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("read"),
	})
	if err != nil {
		t.Fatal(err)
	}

	var call *provider.ToolCall
	var finish *provider.Finish
	for _, part := range collect(res) {
		switch v := part.(type) {
		case provider.ToolCall:
			call = &v
		case provider.Finish:
			finish = &v
		}
	}

	if call == nil {
		t.Fatal("no tool call emitted")
	}
	if call.ToolName != "read_file" || call.Input != `{"path":"/tmp/a"}` {
		t.Errorf("call = %+v", call)
	}
	if call.ToolCallID == "" {
		t.Error("a call id must be synthesised; results cannot be matched without one")
	}
	// Google says STOP even when it asked for tools. Reporting that verbatim
	// would end the agent loop before the tool ever ran.
	if finish.FinishReason.Unified != provider.FinishToolCalls {
		t.Errorf("finish = %q, want tool-calls", finish.FinishReason.Unified)
	}
	if finish.FinishReason.Raw != "STOP" {
		t.Errorf("raw finish = %q, want STOP preserved", finish.FinishReason.Raw)
	}
}

const thinkingStream = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"considering","thought":true,"thoughtSignature":"sig-1"}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"The answer"}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":10,"thoughtsTokenCount":20}}

`

func TestThoughtPartsBecomeReasoning(t *testing.T) {
	p, _, _ := newTestProvider(t, thinkingStream)

	res, err := p.LanguageModel("gemini-3-pro").DoStream(context.Background(), provider.CallOptions{
		Prompt:    userPrompt("think"),
		Reasoning: provider.ReasoningHigh,
	})
	if err != nil {
		t.Fatal(err)
	}

	var reasoning, text strings.Builder
	var signature string
	var finish *provider.Finish

	for _, part := range collect(res) {
		switch v := part.(type) {
		case provider.ReasoningDelta:
			reasoning.WriteString(v.Delta)
			if sig, ok := v.ProviderMetadata[providerID]["thoughtSignature"].(string); ok {
				signature = sig
			}
		case provider.TextDelta:
			text.WriteString(v.Delta)
		case provider.Finish:
			finish = &v
		}
	}

	if reasoning.String() != "considering" {
		t.Errorf("reasoning = %q", reasoning.String())
	}
	if text.String() != "The answer" {
		t.Errorf("text = %q", text.String())
	}
	if signature != "sig-1" {
		t.Errorf("thoughtSignature = %q; without it the reasoning cannot be replayed", signature)
	}
	// candidatesTokenCount excludes thinking, so the total is the sum.
	if got := *finish.Usage.OutputTokens.Total; got != 30 {
		t.Errorf("output total = %d, want 30 (10 text + 20 thoughts)", got)
	}
}

func TestGemini3UsesThinkingLevel(t *testing.T) {
	p, body, _ := newTestProvider(t, textStream)

	res, err := p.LanguageModel("gemini-3-pro").DoStream(context.Background(), provider.CallOptions{
		Prompt:    userPrompt("hi"),
		Reasoning: provider.ReasoningHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(res)

	var sent generateRequest
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	cfg := sent.GenerationConfig.ThinkingConfig
	if cfg == nil {
		t.Fatal("no thinkingConfig")
	}
	if cfg.ThinkingLevel != "high" {
		t.Errorf("thinkingLevel = %q, want high", cfg.ThinkingLevel)
	}
	// Gemini 3 rejects a request that carries both.
	if cfg.ThinkingBudget != nil {
		t.Errorf("gemini 3 must not receive thinkingBudget, got %d", *cfg.ThinkingBudget)
	}
}

func TestGemini25UsesThinkingBudget(t *testing.T) {
	p, body, _ := newTestProvider(t, textStream)

	limit := int64(10000)
	res, err := p.LanguageModel("gemini-2.5-pro").DoStream(context.Background(), provider.CallOptions{
		Prompt:          userPrompt("hi"),
		Reasoning:       provider.ReasoningMedium,
		MaxOutputTokens: &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(res)

	var sent generateRequest
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	cfg := sent.GenerationConfig.ThinkingConfig
	if cfg == nil || cfg.ThinkingBudget == nil {
		t.Fatalf("thinkingConfig = %+v, want a budget", cfg)
	}
	if *cfg.ThinkingBudget != 3000 {
		t.Errorf("budget = %d, want 3000 (30%% of 10000)", *cfg.ThinkingBudget)
	}
	if cfg.ThinkingLevel != "" {
		t.Errorf("gemini 2.5 must not receive thinkingLevel, got %q", cfg.ThinkingLevel)
	}
}

func TestToolResultsBecomeFunctionResponseUserTurns(t *testing.T) {
	p, body, _ := newTestProvider(t, textStream)

	prompt := provider.Prompt{
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "go"}}},
		provider.AssistantMessage{Content: []provider.AssistantPart{
			provider.ToolCallPart{ToolCallID: "c1", ToolName: "echo", Input: map[string]any{"v": 1}},
		}},
		provider.ToolMessage{Content: []provider.ToolPart{
			provider.ToolResultPart{ToolCallID: "c1", ToolName: "echo", Output: provider.ToolOutputText{Value: "done"}},
		}},
	}

	res, err := p.LanguageModel("gemini-3-pro").DoStream(context.Background(), provider.CallOptions{Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	collect(res)

	var sent generateRequest
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}

	if len(sent.Contents) != 3 {
		t.Fatalf("contents = %d, want 3", len(sent.Contents))
	}
	if sent.Contents[1].Role != "model" {
		t.Errorf("assistant role = %q, want model", sent.Contents[1].Role)
	}
	// Google has no tool role: results come back as a user turn.
	if sent.Contents[2].Role != "user" {
		t.Errorf("tool result role = %q, want user", sent.Contents[2].Role)
	}
	fr := sent.Contents[2].Parts[0].FunctionResponse
	if fr == nil || fr.Name != "echo" {
		t.Fatalf("functionResponse = %+v", fr)
	}
	// The payload must be an object, so text results are wrapped.
	if fr.Response["result"] != "done" {
		t.Errorf("response = %v, want the text wrapped under a key", fr.Response)
	}
}

func TestSystemInstructionIsSeparated(t *testing.T) {
	p, body, _ := newTestProvider(t, textStream)

	prompt := provider.Prompt{
		provider.SystemMessage{Content: "be terse"},
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "hi"}}},
	}
	res, err := p.LanguageModel("gemini-3-pro").DoStream(context.Background(), provider.CallOptions{Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	collect(res)

	var sent generateRequest
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.SystemInstruction == nil || sent.SystemInstruction.Parts[0].Text != "be terse" {
		t.Errorf("systemInstruction = %+v", sent.SystemInstruction)
	}
	if len(sent.Contents) != 1 {
		t.Errorf("contents = %d, want 1: the system message must not appear inline", len(sent.Contents))
	}
}

func TestToolSchemaIsConvertedToOpenAPI(t *testing.T) {
	type args struct {
		Path  string `json:"path" jsonschema:"description=A path"`
		Depth *int   `json:"depth,omitempty"`
		Mode  string `json:"mode" jsonschema:"enum=read,enum=write"`
	}

	p, body, _ := newTestProvider(t, textStream)

	res, err := p.LanguageModel("gemini-3-pro").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("go"),
		Tools: []provider.Tool{provider.FunctionTool{
			Name:        "read_file",
			Description: "Read a file",
			InputSchema: jsonschema.Reflect[args](),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(res)

	var raw map[string]any
	if err := json.Unmarshal(*body, &raw); err != nil {
		t.Fatal(err)
	}

	tools := raw["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("google takes one tool object holding all declarations, got %d", len(tools))
	}
	decls := tools[0].(map[string]any)["functionDeclarations"].([]any)
	params := decls[0].(map[string]any)["parameters"].(map[string]any)

	// additionalProperties is not in the OpenAPI 3.0 subset and Google
	// rejects a schema carrying it.
	if _, ok := params["additionalProperties"]; ok {
		t.Error("additionalProperties must be stripped for google")
	}
	if _, ok := params["$schema"]; ok {
		t.Error("$schema must be stripped for google")
	}
	props := params["properties"].(map[string]any)
	if len(props) != 3 {
		t.Errorf("properties = %v", props)
	}
	if props["path"].(map[string]any)["description"] != "A path" {
		t.Errorf("description lost: %v", props["path"])
	}
	required := params["required"].([]any)
	if len(required) != 2 {
		t.Errorf("required = %v, want path and mode", required)
	}
}

func TestEmptyToolSchemaOmitsParameters(t *testing.T) {
	p, body, _ := newTestProvider(t, textStream)

	res, err := p.LanguageModel("gemini-3-pro").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("go"),
		Tools: []provider.Tool{provider.FunctionTool{
			Name:        "now",
			Description: "Current time",
			InputSchema: jsonschema.Reflect[struct{}](),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(res)

	var raw map[string]any
	if err := json.Unmarshal(*body, &raw); err != nil {
		t.Fatal(err)
	}
	decl := raw["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)
	// Google rejects a parameters object with no properties.
	if _, ok := decl["parameters"]; ok {
		t.Errorf("a zero-argument tool must omit parameters, got %v", decl["parameters"])
	}
}

func TestNullableFieldBecomesNullableSchema(t *testing.T) {
	s := &jsonschema.Schema{
		AnyOf: []*jsonschema.Schema{
			{Type: jsonschema.String},
			{Type: jsonschema.Null},
		},
	}
	converted := toOpenAPISchema(s, false).(map[string]any)

	if converted["nullable"] != true {
		t.Errorf("expected nullable, got %v", converted)
	}
	// A single non-null branch collapses into the parent rather than staying
	// an anyOf, which Google handles far better.
	if _, ok := converted["anyOf"]; ok {
		t.Errorf("single-branch union should collapse, got %v", converted)
	}
	if converted["type"] != "string" {
		t.Errorf("type = %v, want string", converted["type"])
	}
}

// codeExecutionStream is the hosted code execution tool: Google reports the
// code it ran and the output as ordinary parts, not as a function call.
const codeExecutionStream = `data: {"candidates":[{"content":{"role":"model","parts":[{"executableCode":{"language":"PYTHON","code":"print(6*7)"}}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"42\n"}}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"The answer is 42."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":6}}

`

func TestHostedSearchRequestShape(t *testing.T) {
	p, body, _ := newTestProvider(t, textStream)

	res, err := p.LanguageModel("gemini-3-pro").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("what shipped in go 1.26?"),
		Tools:  []provider.Tool{Search()},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(res)

	var sent struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Tools) != 1 {
		t.Fatalf("tools = %v, want one tool object", sent.Tools)
	}
	// The value has to be an object. A null is accepted by the API and then
	// silently ignored: the model answers ungrounded and nothing reports it.
	if got, ok := sent.Tools[0]["googleSearch"].(map[string]any); !ok {
		t.Errorf("googleSearch = %#v, want an object", sent.Tools[0]["googleSearch"])
	} else if got == nil {
		t.Error("googleSearch is a null object")
	}
	// Google discriminates by which key is present; a declarations list
	// alongside it is what makes the API reject the request.
	if _, ok := sent.Tools[0]["functionDeclarations"]; ok {
		t.Errorf("hosted tool object carries functionDeclarations: %v", sent.Tools[0])
	}
}

func TestHostedToolsCannotBeMixedWithFunctionTools(t *testing.T) {
	p, _, _ := newTestProvider(t, textStream)

	_, err := p.LanguageModel("gemini-3-pro").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("hi"),
		Tools: []provider.Tool{
			provider.FunctionTool{Name: "read_file"},
			Search(),
		},
	})
	// Google rejects the combination, so failing here gives a message that
	// says why instead of an opaque 400.
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("err = %v, want a refusal to mix hosted and function tools", err)
	}
}

func TestHostedSearchUsesLegacyShapeOnOldModels(t *testing.T) {
	p, body, _ := newTestProvider(t, textStream)

	res, err := p.LanguageModel("gemini-1.5-pro").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("hi"),
		Tools:  []provider.Tool{Search()},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(res)

	var sent struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	if _, ok := sent.Tools[0]["googleSearchRetrieval"]; !ok {
		t.Errorf("tool = %v, want the pre-Gemini-2 googleSearchRetrieval", sent.Tools[0])
	}
}

func TestHostedCodeExecutionBecomesCallAndResult(t *testing.T) {
	p, _, _ := newTestProvider(t, codeExecutionStream)

	res, err := p.LanguageModel("gemini-3-pro").DoStream(context.Background(), provider.CallOptions{
		Prompt: userPrompt("what is 6*7?"),
		Tools:  []provider.Tool{CodeExecution()},
	})
	if err != nil {
		t.Fatal(err)
	}

	var call *provider.ToolCall
	var result *provider.ToolResult
	for _, part := range collect(res) {
		switch v := part.(type) {
		case provider.ToolCall:
			call = &v
		case provider.ToolResult:
			result = &v
		}
	}

	if call == nil {
		t.Fatal("no tool call was emitted for the executed code")
	}
	if !call.ProviderExecuted {
		t.Error("the hosted call was not marked provider-executed")
	}
	if !strings.Contains(call.Input, "print(6*7)") {
		t.Errorf("call input = %q, want the code that ran", call.Input)
	}

	if result == nil {
		t.Fatal("no tool result was emitted for the execution output")
	}
	// Google links the result to its code only by position, so the pairing has
	// to be reconstructed or the transcript shows an orphan result.
	if result.ToolCallID != call.ToolCallID {
		t.Errorf("result id = %q, want it paired with the call's %q", result.ToolCallID, call.ToolCallID)
	}
	if result.IsError {
		t.Error("OUTCOME_OK was reported as an error")
	}
}

func TestImagenRequestShapeAndFilteredPredictions(t *testing.T) {
	// One prediction came back, one was withheld by the safety filter.
	body := `{"predictions":[
		{"bytesBase64Encoded":"aW1n","mimeType":"image/png"},
		{"raiFilteredReason":"policy: people"}
	]}`

	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	p := New(Options{APIKey: "test", BaseURL: srv.URL})

	res, err := p.ImageModel("imagen-4.0-generate-001").DoGenerate(context.Background(),
		provider.ImageCallOptions{
			Prompt:      "a lighthouse",
			N:           2,
			AspectRatio: "16:9",
			// Imagen takes a ratio, so a pixel size has to be reported rather
			// than silently converted.
			Size: "1024x1024",
		})
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Images) != 1 {
		t.Fatalf("images = %d, want the one that was not filtered", len(res.Images))
	}
	if res.Images[0].Base64 != "aW1n" || res.Images[0].MediaType != "image/png" {
		t.Errorf("image = %+v", res.Images[0])
	}

	// Two warnings: the unsupported size, and the withheld prediction. Without
	// the latter a caller cannot tell why they got fewer images than they asked for.
	var sawSize, sawFilter bool
	for _, w := range res.Warnings {
		if w.Feature == "size" {
			sawSize = true
		}
		if strings.Contains(w.Details, "policy: people") {
			sawFilter = true
		}
	}
	if !sawSize {
		t.Error("no warning for the unsupported pixel size")
	}
	if !sawFilter {
		t.Errorf("no warning for the filtered prediction: %+v", res.Warnings)
	}

	var req struct {
		Instances []struct {
			Prompt string `json:"prompt"`
		} `json:"instances"`
		Parameters struct {
			SampleCount int    `json:"sampleCount"`
			AspectRatio string `json:"aspectRatio"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(sent, &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Instances) != 1 || req.Instances[0].Prompt != "a lighthouse" {
		t.Errorf("instances = %+v", req.Instances)
	}
	if req.Parameters.SampleCount != 2 || req.Parameters.AspectRatio != "16:9" {
		t.Errorf("parameters = %+v", req.Parameters)
	}
}

func TestEmbeddingRequestCarriesTaskTypePerValue(t *testing.T) {
	body := `{"embeddings":[{"values":[0.1,0.2]},{"values":[0.3,0.4]}]}`

	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	p := New(Options{APIKey: "test", BaseURL: srv.URL})

	res, err := p.EmbeddingModel("gemini-embedding-001").DoEmbed(context.Background(),
		provider.EmbeddingCallOptions{
			Values:     []string{"a", "b"},
			Dimensions: 256,
			ProviderOptions: provider.ProviderOptions{
				providerID: {"taskType": string(TaskRetrievalDocument)},
			},
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Embeddings) != 2 || len(res.Embeddings[0]) != 2 {
		t.Fatalf("embeddings = %v", res.Embeddings)
	}

	var req struct {
		Requests []struct {
			Model                string `json:"model"`
			TaskType             string `json:"taskType"`
			OutputDimensionality *int   `json:"outputDimensionality"`
			Content              struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(sent, &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Requests) != 2 {
		t.Fatalf("requests = %d, want one per value", len(req.Requests))
	}
	for i, r := range req.Requests {
		// Google repeats the model on every entry even though it is in the URL.
		if r.Model != "models/gemini-embedding-001" {
			t.Errorf("request %d model = %q", i, r.Model)
		}
		if r.TaskType != "RETRIEVAL_DOCUMENT" {
			t.Errorf("request %d task type = %q", i, r.TaskType)
		}
		if r.OutputDimensionality == nil || *r.OutputDimensionality != 256 {
			t.Errorf("request %d dimensions = %v", i, r.OutputDimensionality)
		}
	}
	if req.Requests[0].Content.Parts[0].Text != "a" {
		t.Errorf("first value = %q", req.Requests[0].Content.Parts[0].Text)
	}
}
