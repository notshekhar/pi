package ai_test

import (
	"context"
	"testing"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/provider"
)

func finish(reason provider.FinishReasonKind) provider.Finish {
	return provider.Finish{FinishReason: provider.FinishReason{Unified: reason}}
}

func streamSteps(t *testing.T, tools []ai.Tool, turns ...mockTurn) []ai.Step {
	t.Helper()
	res, err := ai.StreamText(context.Background(), ai.Options{Model: &mockModel{turns: turns}, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	for range res.Stream { //nolint:revive // draining
	}
	final, err := res.Final()
	if err != nil {
		t.Fatal(err)
	}
	return final.Steps
}

// The step goes back to the provider in the order it streamed — reasoning,
// call, reasoning, call — as the AI SDK assembles it. A reused id after its
// end is a new block, and text and reasoning ids are separate spaces.
func TestStepContentKeepsStreamOrder(t *testing.T) {
	// No tools: the calls fail as unknown, which is all the order needs.
	steps := streamSteps(t, nil, mockTurn{parts: []provider.StreamPart{
		provider.ReasoningStart{ID: "0"}, provider.ReasoningDelta{ID: "0", Delta: "first"}, provider.ReasoningEnd{ID: "0"},
		provider.ToolCall{ToolCallID: "c1", ToolName: "shell", Input: `{"command":"ls"}`},
		provider.ReasoningStart{ID: "0"}, provider.ReasoningDelta{ID: "0", Delta: "second"}, provider.ReasoningEnd{ID: "0"},
		provider.TextStart{ID: "0"}, provider.TextDelta{ID: "0", Delta: "and"}, provider.TextEnd{ID: "0"},
		provider.ToolCall{ToolCallID: "c2", ToolName: "shell", Input: `{"command":"pwd"}`},
		finish(provider.FinishToolCalls),
	}}, mockTurn{})
	msg := steps[0].Messages[0].(provider.AssistantMessage)
	var got []string
	for _, p := range msg.Content {
		switch v := p.(type) {
		case provider.ReasoningPart:
			got = append(got, "reasoning:"+v.Text)
		case provider.TextPart:
			got = append(got, "text:"+v.Text)
		case provider.ToolCallPart:
			got = append(got, "call:"+v.ToolCallID)
		}
	}
	want := []string{"reasoning:first", "call:c1", "reasoning:second", "text:and", "call:c2"}
	if len(got) != len(want) {
		t.Fatalf("content = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("content = %v, want %v", got, want)
		}
	}
}

// A step the model did not finish runs none of its calls, and keeps none
// unanswered for the next request; one it finished runs them.
func TestUnfinishedStepRunsNoCalls(t *testing.T) {
	call := provider.ToolCall{ToolCallID: "c1", ToolName: "shell", Input: `{"command":"rm -rf b`}
	for _, reason := range []provider.FinishReasonKind{provider.FinishInterrupted, provider.FinishLength, provider.FinishContentFilter} {
		var ran []string
		steps := streamSteps(t, []ai.Tool{newShellTool(&ran)},
			mockTurn{parts: []provider.StreamPart{provider.TextDelta{ID: "t", Delta: "on it"}, call, finish(reason)}})
		if len(ran) != 0 || len(steps) != 1 {
			t.Fatalf("%s: ran %v over %d steps", reason, ran, len(steps))
		}
		for _, p := range steps[0].Messages[0].(provider.AssistantMessage).Content {
			if _, ok := p.(provider.ToolCallPart); ok {
				t.Errorf("%s: an unanswered call was kept", reason)
			}
		}
	}
	var ran []string
	streamSteps(t, []ai.Tool{newShellTool(&ran)},
		mockTurn{parts: []provider.StreamPart{provider.ToolCall{ToolCallID: "c1", ToolName: "shell", Input: `{"command":"ls"}`}, finish(provider.FinishToolCalls)}},
		mockTurn{})
	if len(ran) != 1 {
		t.Errorf("a finished step's call did not run: %v", ran)
	}
}
