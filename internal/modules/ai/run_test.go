package ai_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// mockModel replays a scripted list of turns, one per step, and records the
// prompt it was given each time.
type mockModel struct {
	turns []mockTurn

	mu      sync.Mutex
	calls   int
	prompts []provider.Prompt
	// options records the full call options of each step, for assertions about
	// what the core asked the provider for.
	options []provider.CallOptions
}

// mockTurn is one scripted model response.
type mockTurn struct {
	parts []provider.StreamPart
	err   error
}

func (m *mockModel) SpecificationVersion() string { return provider.SpecificationVersion }
func (m *mockModel) Provider() string             { return "mock" }
func (m *mockModel) ModelID() string              { return "mock-1" }

func (m *mockModel) SupportedURLs(context.Context) (map[string][]*regexp.Regexp, error) {
	return nil, nil
}

// next returns the turn for the current call and records the prompt.
func (m *mockModel) next(opts provider.CallOptions) (mockTurn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.prompts = append(m.prompts, opts.Prompt)
	m.options = append(m.options, opts)
	if m.calls >= len(m.turns) {
		return mockTurn{}, fmt.Errorf("mock: unexpected call %d, only %d turns scripted", m.calls+1, len(m.turns))
	}
	turn := m.turns[m.calls]
	m.calls++
	return turn, nil
}

func (m *mockModel) DoStream(ctx context.Context, opts provider.CallOptions) (*provider.StreamResult, error) {
	turn, err := m.next(opts)
	if err != nil {
		return nil, err
	}
	if turn.err != nil {
		return nil, turn.err
	}

	ch := make(chan provider.StreamPart, len(turn.parts)+1)
	for _, p := range turn.parts {
		ch <- p
	}
	close(ch)
	return &provider.StreamResult{Stream: ch}, nil
}

func (m *mockModel) DoGenerate(ctx context.Context, opts provider.CallOptions) (*provider.GenerateResult, error) {
	turn, err := m.next(opts)
	if err != nil {
		return nil, err
	}
	if turn.err != nil {
		return nil, turn.err
	}

	res := &provider.GenerateResult{
		FinishReason: provider.FinishReason{Unified: provider.FinishStop},
	}

	// The scripted turns are written as stream parts, so collapse the deltas
	// into the completed content a non-streaming call would return.
	var text, reasoning strings.Builder
	for _, p := range turn.parts {
		switch v := p.(type) {
		case provider.TextDelta:
			text.WriteString(v.Delta)
		case provider.ReasoningDelta:
			reasoning.WriteString(v.Delta)
		case provider.Text:
			text.WriteString(v.Text)
		case provider.Reasoning:
			reasoning.WriteString(v.Text)
		case provider.ToolCall:
			res.Content = append(res.Content, v)
		case provider.Finish:
			res.FinishReason = v.FinishReason
			res.Usage = v.Usage
		}
	}
	if text.Len() > 0 {
		res.Content = append([]provider.Content{provider.Text{Text: text.String()}}, res.Content...)
	}
	if reasoning.Len() > 0 {
		res.Content = append([]provider.Content{provider.Reasoning{Text: reasoning.String()}}, res.Content...)
	}
	return res, nil
}

// textTurn scripts a plain text response.
func textTurn(text string) mockTurn {
	return mockTurn{parts: []provider.StreamPart{
		provider.TextStart{ID: "0"},
		provider.TextDelta{ID: "0", Delta: text},
		provider.TextEnd{ID: "0"},
		provider.Finish{
			FinishReason: provider.FinishReason{Unified: provider.FinishStop, Raw: "end_turn"},
			Usage:        usage(10, 5),
		},
	}}
}

// toolTurn scripts a response that calls one tool.
func toolTurn(id, name, input string) mockTurn {
	return mockTurn{parts: []provider.StreamPart{
		provider.ToolInputStart{ID: id, ToolName: name},
		provider.ToolInputDelta{ID: id, Delta: input},
		provider.ToolInputEnd{ID: id},
		provider.ToolCall{ToolCallID: id, ToolName: name, Input: input},
		provider.Finish{
			FinishReason: provider.FinishReason{Unified: provider.FinishToolCalls, Raw: "tool_use"},
			Usage:        usage(20, 8),
		},
	}}
}

// usage builds a Usage with the given input and output totals.
func usage(in, out int64) provider.Usage {
	return provider.Usage{
		InputTokens:  provider.InputTokens{Total: &in, NoCache: &in},
		OutputTokens: provider.OutputTokens{Total: &out},
	}
}

// echoArgs is the input type for the test tool.
type echoArgs struct {
	Value string `json:"value" jsonschema:"description=The value to echo"`
}

func TestStreamTextPlainResponse(t *testing.T) {
	model := &mockModel{turns: []mockTurn{textTurn("Hello there")}}

	res, err := ai.StreamText(context.Background(), ai.Options{
		Model:    model,
		Messages: []provider.Message{ai.UserText("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	var sawRunFinish bool
	for part := range res.Stream {
		switch v := part.(type) {
		case provider.TextDelta:
			text.WriteString(v.Delta)
		case ai.RunFinish:
			sawRunFinish = true
		case provider.ErrorPart:
			t.Fatalf("unexpected error: %v", v.Err)
		}
	}

	final, err := res.Final()
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "Hello there" {
		t.Errorf("streamed text = %q", text.String())
	}
	if final.Text != "Hello there" {
		t.Errorf("final text = %q", final.Text)
	}
	if !sawRunFinish {
		t.Error("no RunFinish part")
	}
	if len(final.Steps) != 1 {
		t.Errorf("steps = %d, want 1", len(final.Steps))
	}
}

func TestStreamTextExecutesToolAndContinues(t *testing.T) {
	model := &mockModel{turns: []mockTurn{
		toolTurn("call_1", "echo", `{"value":"ping"}`),
		textTurn("done"),
	}}

	echo := ai.NewTool("echo", "Echo a value",
		func(ctx context.Context, a echoArgs) (ai.ToolResult, error) {
			return ai.ToolText("echoed: " + a.Value), nil
		})

	res, err := ai.StreamText(context.Background(), ai.Options{
		Model:    model,
		Messages: []provider.Message{ai.UserText("echo ping")},
		Tools:    []ai.Tool{echo},
	})
	if err != nil {
		t.Fatal(err)
	}

	var executed []ai.ToolExecution
	for part := range res.Stream {
		if v, ok := part.(ai.ToolExecuted); ok {
			executed = append(executed, v.Execution)
		}
	}

	final, err := res.Final()
	if err != nil {
		t.Fatal(err)
	}

	if len(executed) != 1 {
		t.Fatalf("tool executions = %d, want 1", len(executed))
	}
	if executed[0].Err != nil {
		t.Errorf("tool error: %v", executed[0].Err)
	}
	out, ok := executed[0].Result.Output().(provider.ToolOutputText)
	if !ok || out.Value != "echoed: ping" {
		t.Errorf("tool output = %#v", executed[0].Result.Output())
	}
	if len(final.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(final.Steps))
	}
	if final.Text != "done" {
		t.Errorf("final text = %q, want done", final.Text)
	}

	// The second call must see the tool result, or the model is answering
	// without knowing what the tool returned.
	if len(model.prompts) != 2 {
		t.Fatalf("model calls = %d, want 2", len(model.prompts))
	}
	second := model.prompts[1]
	var sawToolResult bool
	for _, msg := range second {
		if tm, ok := msg.(provider.ToolMessage); ok {
			for _, p := range tm.Content {
				if rp, ok := p.(provider.ToolResultPart); ok && rp.ToolCallID == "call_1" {
					sawToolResult = true
				}
			}
		}
	}
	if !sawToolResult {
		t.Errorf("second call did not carry the tool result; prompt was %#v", second)
	}

	// Total usage must span both steps.
	if got := *final.Usage.OutputTokens.Total; got != 13 {
		t.Errorf("total output tokens = %d, want 13 (8 + 5)", got)
	}
}

func TestUnknownToolIsReportedToModel(t *testing.T) {
	model := &mockModel{turns: []mockTurn{
		toolTurn("call_1", "nonexistent", `{}`),
		textTurn("sorry"),
	}}

	echo := ai.NewTool("echo", "Echo a value",
		func(ctx context.Context, a echoArgs) (ai.ToolResult, error) {
			return ai.ToolText(a.Value), nil
		})

	res, err := ai.StreamText(context.Background(), ai.Options{
		Model: model, Tools: []ai.Tool{echo},
		Messages: []provider.Message{ai.UserText("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range res.Stream {
	}

	final, err := res.Final()
	if err != nil {
		t.Fatalf("a hallucinated tool name should not fail the run: %v", err)
	}
	if len(final.Steps) != 2 {
		t.Fatalf("steps = %d, want 2: the run should continue so the model can recover", len(final.Steps))
	}

	exec := final.Steps[0].ToolExecutions[0]
	out, ok := exec.Result.Output().(provider.ToolOutputErrorText)
	if !ok {
		t.Fatalf("output = %#v, want an error text", exec.Result.Output())
	}
	if !strings.Contains(out.Value, "echo") {
		t.Errorf("the error should list the available tools, got %q", out.Value)
	}
}

func TestMalformedToolArgumentsAreReportedNotFatal(t *testing.T) {
	model := &mockModel{turns: []mockTurn{
		toolTurn("call_1", "echo", `{"value": ` /* truncated JSON */),
		textTurn("recovered"),
	}}

	echo := ai.NewTool("echo", "Echo a value",
		func(ctx context.Context, a echoArgs) (ai.ToolResult, error) {
			t.Error("execute should not run with unparseable arguments")
			return ai.ToolText(""), nil
		})

	res, err := ai.StreamText(context.Background(), ai.Options{
		Model: model, Tools: []ai.Tool{echo},
		Messages: []provider.Message{ai.UserText("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range res.Stream {
	}

	final, err := res.Final()
	if err != nil {
		t.Fatal(err)
	}
	out, ok := final.Steps[0].ToolExecutions[0].Result.Output().(provider.ToolOutputErrorText)
	if !ok {
		t.Fatalf("output = %#v, want an error text", final.Steps[0].ToolExecutions[0].Result.Output())
	}
	if !strings.Contains(out.Value, "could not parse") {
		t.Errorf("error = %q", out.Value)
	}
}

func TestToolPanicIsContained(t *testing.T) {
	model := &mockModel{turns: []mockTurn{
		toolTurn("call_1", "boom", `{"value":"x"}`),
		textTurn("survived"),
	}}

	boom := ai.NewTool("boom", "Panics",
		func(ctx context.Context, a echoArgs) (ai.ToolResult, error) {
			panic("kaboom")
		})

	res, err := ai.StreamText(context.Background(), ai.Options{
		Model: model, Tools: []ai.Tool{boom},
		Messages: []provider.Message{ai.UserText("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range res.Stream {
	}

	final, err := res.Final()
	if err != nil {
		t.Fatal(err)
	}
	if final.Text != "survived" {
		t.Errorf("a panicking tool should not end the run, final text = %q", final.Text)
	}
	if final.Steps[0].ToolExecutions[0].Err == nil {
		t.Error("the panic should be recorded as an execution error")
	}
}

func TestAbortRunStopsEverything(t *testing.T) {
	model := &mockModel{turns: []mockTurn{
		toolTurn("call_1", "stop", `{"value":"x"}`),
		textTurn("should never run"),
	}}

	stop := ai.NewTool("stop", "Aborts",
		func(ctx context.Context, a echoArgs) (ai.ToolResult, error) {
			return ai.ToolResult{}, fmt.Errorf("user cancelled: %w", ai.ErrAbortRun)
		})

	res, err := ai.StreamText(context.Background(), ai.Options{
		Model: model, Tools: []ai.Tool{stop},
		Messages: []provider.Message{ai.UserText("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range res.Stream {
	}

	_, err = res.Final()
	if !errors.Is(err, ai.ErrAbortRun) {
		t.Fatalf("error = %v, want ErrAbortRun", err)
	}
	if model.calls != 1 {
		t.Errorf("model calls = %d, want 1: the run should not step again", model.calls)
	}
}

func TestMaxStepsCapsTheLoop(t *testing.T) {
	// A model that always calls a tool would loop forever without the cap.
	turns := make([]mockTurn, 10)
	for i := range turns {
		turns[i] = toolTurn(fmt.Sprintf("call_%d", i), "echo", `{"value":"x"}`)
	}
	model := &mockModel{turns: turns}

	echo := ai.NewTool("echo", "Echo", func(ctx context.Context, a echoArgs) (ai.ToolResult, error) {
		return ai.ToolText(a.Value), nil
	})

	res, err := ai.StreamText(context.Background(), ai.Options{
		Model: model, Tools: []ai.Tool{echo}, MaxSteps: 3,
		Messages: []provider.Message{ai.UserText("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range res.Stream {
	}

	final, err := res.Final()
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Steps) != 3 {
		t.Errorf("steps = %d, want 3", len(final.Steps))
	}
}

func TestStopWhenEndsRunEarly(t *testing.T) {
	model := &mockModel{turns: []mockTurn{
		toolTurn("call_1", "echo", `{"value":"x"}`),
		textTurn("unreached"),
	}}

	echo := ai.NewTool("echo", "Echo", func(ctx context.Context, a echoArgs) (ai.ToolResult, error) {
		return ai.ToolText(a.Value), nil
	})

	res, err := ai.StreamText(context.Background(), ai.Options{
		Model: model, Tools: []ai.Tool{echo},
		Messages: []provider.Message{ai.UserText("go")},
		StopWhen: func(step ai.Step) bool { return len(step.ToolExecutions) > 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	for range res.Stream {
	}

	final, _ := res.Final()
	if len(final.Steps) != 1 {
		t.Errorf("steps = %d, want 1", len(final.Steps))
	}
}

func TestReasoningSignatureSurvivesReplay(t *testing.T) {
	// The signature arrives on an empty delta and again on the end part; it
	// must reach the next call's prompt or Anthropic rejects the history.
	model := &mockModel{turns: []mockTurn{
		{parts: []provider.StreamPart{
			provider.ReasoningStart{ID: "0"},
			provider.ReasoningDelta{ID: "0", Delta: "thinking hard"},
			provider.ReasoningDelta{ID: "0", Delta: "", ProviderMetadata: provider.ProviderMetadata{
				"anthropic": {"signature": "sig-xyz"},
			}},
			provider.ReasoningEnd{ID: "0"},
			provider.ToolCall{ToolCallID: "call_1", ToolName: "echo", Input: `{"value":"x"}`},
			provider.Finish{FinishReason: provider.FinishReason{Unified: provider.FinishToolCalls}},
		}},
		textTurn("done"),
	}}

	echo := ai.NewTool("echo", "Echo", func(ctx context.Context, a echoArgs) (ai.ToolResult, error) {
		return ai.ToolText(a.Value), nil
	})

	res, err := ai.StreamText(context.Background(), ai.Options{
		Model: model, Tools: []ai.Tool{echo},
		Messages: []provider.Message{ai.UserText("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range res.Stream {
	}
	if _, err := res.Final(); err != nil {
		t.Fatal(err)
	}

	var found string
	for _, msg := range model.prompts[1] {
		am, ok := msg.(provider.AssistantMessage)
		if !ok {
			continue
		}
		for _, part := range am.Content {
			rp, ok := part.(provider.ReasoningPart)
			if !ok {
				continue
			}
			if rp.Text != "thinking hard" {
				t.Errorf("reasoning text = %q", rp.Text)
			}
			if sig, ok := rp.ProviderOptions["anthropic"]["signature"].(string); ok {
				found = sig
			}
		}
	}
	if found != "sig-xyz" {
		t.Errorf("signature = %q, want sig-xyz", found)
	}
}

func TestParallelToolCallsPreserveOrder(t *testing.T) {
	// Two calls in one turn: they run concurrently but must be reported and
	// replayed in the order the model asked for them.
	model := &mockModel{turns: []mockTurn{
		{parts: []provider.StreamPart{
			provider.ToolCall{ToolCallID: "a", ToolName: "echo", Input: `{"value":"first"}`},
			provider.ToolCall{ToolCallID: "b", ToolName: "echo", Input: `{"value":"second"}`},
			provider.Finish{FinishReason: provider.FinishReason{Unified: provider.FinishToolCalls}},
		}},
		textTurn("done"),
	}}

	echo := ai.NewTool("echo", "Echo", func(ctx context.Context, a echoArgs) (ai.ToolResult, error) {
		return ai.ToolText(a.Value), nil
	})

	res, err := ai.StreamText(context.Background(), ai.Options{
		Model: model, Tools: []ai.Tool{echo},
		Messages: []provider.Message{ai.UserText("go")},
	})
	if err != nil {
		t.Fatal(err)
	}

	var order []string
	for part := range res.Stream {
		if v, ok := part.(ai.ToolExecuted); ok {
			order = append(order, v.Execution.ToolCallID)
		}
	}
	if _, err := res.Final(); err != nil {
		t.Fatal(err)
	}

	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Errorf("execution order = %v, want [a b]", order)
	}
}

func TestGenerateTextMatchesStreamText(t *testing.T) {
	model := &mockModel{turns: []mockTurn{
		toolTurn("call_1", "echo", `{"value":"ping"}`),
		textTurn("done"),
	}}

	echo := ai.NewTool("echo", "Echo", func(ctx context.Context, a echoArgs) (ai.ToolResult, error) {
		return ai.ToolText("echoed: " + a.Value), nil
	})

	res, err := ai.GenerateText(context.Background(), ai.Options{
		Model: model, Tools: []ai.Tool{echo},
		Messages: []provider.Message{ai.UserText("echo ping")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" {
		t.Errorf("text = %q", res.Text)
	}
	if len(res.Steps) != 2 {
		t.Errorf("steps = %d, want 2", len(res.Steps))
	}
}

func TestCallerMessagesAreNotMutated(t *testing.T) {
	model := &mockModel{turns: []mockTurn{textTurn("hi")}}

	messages := []provider.Message{ai.UserText("hello")}
	res, err := ai.GenerateText(context.Background(), ai.Options{
		Model: model, Messages: messages,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(messages) != 1 {
		t.Errorf("the caller's slice was mutated: len = %d", len(messages))
	}
	// The result's history is the caller's plus what the run added.
	if len(res.Messages) != 2 {
		t.Errorf("result messages = %d, want 2", len(res.Messages))
	}
}

func TestDuplicateToolNamesRejected(t *testing.T) {
	echo := ai.NewTool("echo", "Echo", func(ctx context.Context, a echoArgs) (ai.ToolResult, error) {
		return ai.ToolText(a.Value), nil
	})

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{}, Tools: []ai.Tool{echo, echo},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate tool name") {
		t.Errorf("err = %v, want a duplicate tool name error", err)
	}
}

func TestSystemPromptIsPrepended(t *testing.T) {
	model := &mockModel{turns: []mockTurn{textTurn("ok")}}

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model:    model,
		System:   "be terse",
		Messages: []provider.Message{ai.UserText("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}

	first := model.prompts[0]
	sys, ok := first[0].(provider.SystemMessage)
	if !ok {
		t.Fatalf("first message = %T, want SystemMessage", first[0])
	}
	if sys.Content != "be terse" {
		t.Errorf("system = %q", sys.Content)
	}
}
