package ai_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// namedTool returns a tool that records that it ran.
func namedTool(name string, ran *[]string) ai.Tool {
	return ai.NewTool(name, "does nothing",
		func(context.Context, struct{}) (ai.ToolResult, error) {
			*ran = append(*ran, name)
			return ai.ToolText("ok"), nil
		})
}

// toolNamesIn lists the tool names a call was offered.
func toolNamesIn(opts provider.CallOptions) []string {
	var names []string
	for _, t := range opts.Tools {
		if fn, ok := t.(provider.FunctionTool); ok {
			names = append(names, fn.Name)
		}
	}
	return names
}

func TestActiveToolsNarrowsWhatTheModelIsOffered(t *testing.T) {
	var ran []string
	model := &mockModel{turns: []mockTurn{{}}}

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model:       model,
		Tools:       []ai.Tool{namedTool("read", &ran), namedTool("write", &ran), namedTool("bash", &ran)},
		ActiveTools: []string{"read", "bash"},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := strings.Join(toolNamesIn(model.options[0]), ",")
	// Order follows the caller's tool list, not the active list, so the prompt
	// stays byte-identical between steps and the provider cache keeps hitting.
	if got != "read,bash" {
		t.Errorf("offered = %q, want read,bash", got)
	}
}

func TestEmptyActiveToolsOffersNothing(t *testing.T) {
	var ran []string
	model := &mockModel{turns: []mockTurn{{}}}

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model:       model,
		Tools:       []ai.Tool{namedTool("read", &ran)},
		ActiveTools: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A non-nil empty list is how a step is made to answer rather than call.
	if names := toolNamesIn(model.options[0]); len(names) != 0 {
		t.Errorf("offered = %v, want none", names)
	}
}

func TestDeactivatedToolCannotBeCalled(t *testing.T) {
	var ran []string

	// The model calls a tool it was not offered, as it might from replayed
	// history. Narrowing the set is a permission boundary, so it must not run.
	res, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{
			{parts: []provider.StreamPart{
				provider.ToolCall{ToolCallID: "c1", ToolName: "write", Input: "{}"},
			}},
			{},
		}},
		Tools:       []ai.Tool{namedTool("read", &ran), namedTool("write", &ran)},
		ActiveTools: []string{"read"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(ran) != 0 {
		t.Fatalf("ran = %v, want the deactivated tool not to have run", ran)
	}

	exec := res.Steps[0].ToolExecutions[0]
	out, ok := exec.Result.Output().(provider.ToolOutputErrorText)
	if !ok {
		t.Fatalf("output = %T, want an error reported to the model", exec.Result.Output())
	}
	// The message must list what is actually available this step.
	if !strings.Contains(out.Value, "read") || strings.Contains(out.Value, "available tools: read, write") {
		t.Errorf("message = %q, want it to list only the active tools", out.Value)
	}
}

func TestPrepareStepSwitchesModelPerStep(t *testing.T) {
	var ran []string
	cheap := &mockModel{turns: []mockTurn{
		{parts: []provider.StreamPart{
			provider.ToolCall{ToolCallID: "c1", ToolName: "read", Input: "{}"},
		}},
	}}
	expensive := &mockModel{turns: []mockTurn{{parts: []provider.StreamPart{
		provider.Text{Text: "done"},
	}}}}

	res, err := ai.GenerateText(context.Background(), ai.Options{
		Model: cheap,
		Tools: []ai.Tool{namedTool("read", &ran)},
		PrepareStep: func(_ context.Context, step ai.StepContext) (ai.StepOverrides, error) {
			if step.Number == 0 {
				return ai.StepOverrides{}, nil
			}
			return ai.StepOverrides{Model: expensive}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if cheap.calls != 1 || expensive.calls != 1 {
		t.Errorf("calls = cheap %d, expensive %d, want one each", cheap.calls, expensive.calls)
	}
	if res.Text != "done" {
		t.Errorf("text = %q, want the second model's answer", res.Text)
	}
}

func TestPrepareStepSeesTheStepNumberAndHistory(t *testing.T) {
	var ran []string
	var seen []int
	var historyLengths []int

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{
			{parts: []provider.StreamPart{
				provider.ToolCall{ToolCallID: "c1", ToolName: "read", Input: "{}"},
			}},
			{},
		}},
		Tools:    []ai.Tool{namedTool("read", &ran)},
		Messages: []provider.Message{ai.UserText("go")},
		PrepareStep: func(_ context.Context, step ai.StepContext) (ai.StepOverrides, error) {
			seen = append(seen, step.Number)
			historyLengths = append(historyLengths, len(step.Messages))
			return ai.StepOverrides{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(seen) != 2 || seen[0] != 0 || seen[1] != 1 {
		t.Errorf("step numbers = %v, want 0 then 1", seen)
	}
	// The second call must see what the first step added: the assistant
	// message and the tool result.
	if historyLengths[0] != 1 || historyLengths[1] != 3 {
		t.Errorf("history lengths = %v, want 1 then 3", historyLengths)
	}
}

func TestPrepareStepCanReplaceThePromptForOneStep(t *testing.T) {
	var ran []string
	model := &mockModel{turns: []mockTurn{{}, {}}}

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model:    model,
		System:   "original",
		Messages: []provider.Message{ai.UserText("first"), ai.UserText("second")},
		Tools:    []ai.Tool{namedTool("read", &ran)},
		PrepareStep: func(_ context.Context, step ai.StepContext) (ai.StepOverrides, error) {
			return ai.StepOverrides{
				System:   "replaced",
				Messages: []provider.Message{ai.UserText("only this")},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	prompt := model.prompts[0]
	if system, ok := prompt[0].(provider.SystemMessage); !ok || system.Content != "replaced" {
		t.Errorf("system = %+v, want the override", prompt[0])
	}
	if len(prompt) != 2 {
		t.Fatalf("prompt has %d messages, want the system plus the one replacement", len(prompt))
	}
}

func TestPrepareStepPruningDoesNotDamageTheRunHistory(t *testing.T) {
	var ran []string
	model := &mockModel{turns: []mockTurn{{}, {}}}

	res, err := ai.GenerateText(context.Background(), ai.Options{
		Model:    model,
		Messages: []provider.Message{ai.UserText("first"), ai.UserText("second")},
		Tools:    []ai.Tool{namedTool("read", &ran)},
		PrepareStep: func(_ context.Context, step ai.StepContext) (ai.StepOverrides, error) {
			return ai.StepOverrides{Messages: []provider.Message{ai.UserText("pruned")}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Pruning changes what one step sends, not what the run is holding: the
	// caller gets their conversation back intact.
	if len(res.Messages) < 2 {
		t.Fatalf("result messages = %d, want the original history preserved", len(res.Messages))
	}
	first, ok := res.Messages[0].(provider.UserMessage)
	if !ok || first.Content[0].(provider.TextPart).Text != "first" {
		t.Errorf("first message = %+v, want the original", res.Messages[0])
	}
}

func TestPrepareStepCanForceAToolForOneStepOnly(t *testing.T) {
	var ran []string
	model := &mockModel{turns: []mockTurn{
		{parts: []provider.StreamPart{
			provider.ToolCall{ToolCallID: "c1", ToolName: "read", Input: "{}"},
		}},
		{},
	}}

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model: model,
		Tools: []ai.Tool{namedTool("read", &ran)},
		PrepareStep: func(_ context.Context, step ai.StepContext) (ai.StepOverrides, error) {
			if step.Number > 0 {
				return ai.StepOverrides{}, nil
			}
			return ai.StepOverrides{
				ToolChoice: &provider.ToolChoice{Type: provider.ToolChoiceRequired},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if model.options[0].ToolChoice == nil ||
		model.options[0].ToolChoice.Type != provider.ToolChoiceRequired {
		t.Errorf("step 0 tool choice = %+v, want required", model.options[0].ToolChoice)
	}
	// Leaving a forced choice set for the whole run loops until MaxSteps.
	if model.options[1].ToolChoice != nil {
		t.Errorf("step 1 tool choice = %+v, want it cleared", model.options[1].ToolChoice)
	}
}

func TestPrepareStepErrorEndsTheRun(t *testing.T) {
	sentinel := errors.New("no plan")

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{{}}},
		PrepareStep: func(context.Context, ai.StepContext) (ai.StepOverrides, error) {
			return ai.StepOverrides{}, sentinel
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the callback's error", err)
	}
}

func TestPrepareStepOverridesSamplingForOneStep(t *testing.T) {
	var ran []string
	model := &mockModel{turns: []mockTurn{
		{parts: []provider.StreamPart{
			provider.ToolCall{ToolCallID: "c1", ToolName: "read", Input: "{}"},
		}},
		{},
	}}

	base := 0.2
	stepTemp := 0.9

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model:       model,
		Tools:       []ai.Tool{namedTool("read", &ran)},
		Temperature: &base,
		PrepareStep: func(_ context.Context, step ai.StepContext) (ai.StepOverrides, error) {
			if step.Number != 0 {
				return ai.StepOverrides{}, nil
			}
			return ai.StepOverrides{
				Temperature: &stepTemp,
				Reasoning:   provider.ReasoningHigh,
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := *model.options[0].Temperature; got != 0.9 {
		t.Errorf("step 0 temperature = %v, want the override", got)
	}
	if model.options[0].Reasoning != provider.ReasoningHigh {
		t.Errorf("step 0 reasoning = %q, want high", model.options[0].Reasoning)
	}
	// The run's own setting comes back once the step is over.
	if got := *model.options[1].Temperature; got != 0.2 {
		t.Errorf("step 1 temperature = %v, want the run's 0.2", got)
	}
}
