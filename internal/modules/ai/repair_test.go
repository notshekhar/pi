package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// pathArgs is the input to the repair tests' tool.
type pathArgs struct {
	Path string `json:"path"`
}

// readTool records the paths it was asked for.
func readTool(paths *[]string) ai.Tool {
	return ai.NewTool("read", "Read a file",
		func(_ context.Context, a pathArgs) (ai.ToolResult, error) {
			*paths = append(*paths, a.Path)
			return ai.ToolText("contents"), nil
		})
}

func TestRepairFixesUnparseableArguments(t *testing.T) {
	var paths []string
	var seen ai.RepairContext

	res, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{
			{parts: []provider.StreamPart{
				// Truncated JSON, which is what a model that hit its token
				// limit mid-call actually produces.
				provider.ToolCall{ToolCallID: "c1", ToolName: "read", Input: `{"path": "/tmp/a`},
			}},
			{},
		}},
		Tools: []ai.Tool{readTool(&paths)},
		RepairToolCall: func(_ context.Context, rc ai.RepairContext) (*ai.ToolCall, error) {
			seen = rc
			fixed := rc.Call
			fixed.Input = json.RawMessage(`{"path":"/tmp/a"}`)
			return &fixed, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(paths) != 1 || paths[0] != "/tmp/a" {
		t.Fatalf("paths = %v, want the repaired call to have run", paths)
	}
	if seen.Tool == nil || seen.Tool.Name() != "read" {
		t.Errorf("repair got tool %v, want the named tool", seen.Tool)
	}
	if seen.Err == nil || !strings.Contains(seen.Err.Error(), "not valid JSON") {
		t.Errorf("repair got err %v, want the parse failure", seen.Err)
	}
	// The result has to answer the call the model made, or the provider
	// rejects the pairing on the next turn.
	if got := res.Steps[0].ToolExecutions[0].ToolCallID; got != "c1" {
		t.Errorf("tool call id = %q, want the original c1", got)
	}
}

func TestRepairCanRedirectAHallucinatedToolName(t *testing.T) {
	var paths []string

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{
			{parts: []provider.StreamPart{
				provider.ToolCall{ToolCallID: "c1", ToolName: "read_file", Input: `{"path":"/tmp/a"}`},
			}},
			{},
		}},
		Tools: []ai.Tool{readTool(&paths)},
		RepairToolCall: func(_ context.Context, rc ai.RepairContext) (*ai.ToolCall, error) {
			// A nil Tool means the name matched nothing, so the repair has to
			// choose among what was offered.
			if rc.Tool != nil {
				return nil, nil
			}
			fixed := rc.Call
			fixed.ToolName = rc.Tools[0].Name()
			return &fixed, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(paths) != 1 || paths[0] != "/tmp/a" {
		t.Errorf("paths = %v, want the redirected call to have run", paths)
	}
}

func TestRepairDecliningLeavesTheFailureToTheModel(t *testing.T) {
	var paths []string

	res, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{
			{parts: []provider.StreamPart{
				provider.ToolCall{ToolCallID: "c1", ToolName: "nope", Input: "{}"},
			}},
			{},
		}},
		Tools: []ai.Tool{readTool(&paths)},
		RepairToolCall: func(context.Context, ai.RepairContext) (*ai.ToolCall, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Declining must not swallow the problem: the model still has to hear it.
	out, ok := res.Steps[0].ToolExecutions[0].Result.Output().(provider.ToolOutputErrorText)
	if !ok {
		t.Fatalf("output = %T, want the failure reported", res.Steps[0].ToolExecutions[0].Result.Output())
	}
	if !strings.Contains(out.Value, "unknown tool") {
		t.Errorf("message = %q", out.Value)
	}
}

func TestRepairErrorEndsTheRun(t *testing.T) {
	sentinel := errors.New("repair model down")
	var paths []string

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{
			{parts: []provider.StreamPart{
				provider.ToolCall{ToolCallID: "c1", ToolName: "read", Input: `{bad`},
			}},
			{},
		}},
		Tools: []ai.Tool{readTool(&paths)},
		RepairToolCall: func(context.Context, ai.RepairContext) (*ai.ToolCall, error) {
			return nil, sentinel
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the repair's error", err)
	}
}

func TestRepairIsNotCalledForGoodCalls(t *testing.T) {
	var paths []string
	called := 0

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{
			{parts: []provider.StreamPart{
				provider.ToolCall{ToolCallID: "c1", ToolName: "read", Input: `{"path":"/tmp/a"}`},
			}},
			{},
		}},
		Tools: []ai.Tool{readTool(&paths)},
		RepairToolCall: func(context.Context, ai.RepairContext) (*ai.ToolCall, error) {
			called++
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if called != 0 {
		t.Errorf("repair called %d times for a valid call", called)
	}
	if len(paths) != 1 {
		t.Errorf("paths = %v, want the call to have run untouched", paths)
	}
}

func TestZeroArgumentCallIsNotTreatedAsBroken(t *testing.T) {
	called := 0
	ran := 0

	tool := ai.NewTool("ping", "no arguments",
		func(context.Context, struct{}) (ai.ToolResult, error) {
			ran++
			return ai.ToolText("pong"), nil
		})

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{
			{parts: []provider.StreamPart{
				// Providers send an empty input for a zero-argument tool.
				provider.ToolCall{ToolCallID: "c1", ToolName: "ping", Input: ""},
			}},
			{},
		}},
		Tools: []ai.Tool{tool},
		RepairToolCall: func(context.Context, ai.RepairContext) (*ai.ToolCall, error) {
			called++
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if called != 0 {
		t.Errorf("repair called %d times for a zero-argument call", called)
	}
	if ran != 1 {
		t.Errorf("tool ran %d times, want 1", ran)
	}
}
