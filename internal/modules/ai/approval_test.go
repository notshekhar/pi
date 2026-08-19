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

// dangerousArgs is the input to the test's shell-like tool.
type dangerousArgs struct {
	Command string `json:"command"`
}

// newShellTool returns a tool that ran-log every call it executed.
func newShellTool(ran *[]string) ai.Tool {
	return ai.NewTool("shell", "Run a shell command",
		func(_ context.Context, a dangerousArgs) (ai.ToolResult, error) {
			*ran = append(*ran, a.Command)
			return ai.ToolText("ok"), nil
		})
}

// writeOnlyApproval is a tool that asks for approval only for calls that write.
type writeOnlyApproval struct{ ai.Tool }

func (writeOnlyApproval) NeedsApproval(_ context.Context, input json.RawMessage) bool {
	var a dangerousArgs
	if err := json.Unmarshal(input, &a); err != nil {
		// An unparseable call is treated as dangerous; the safe reading of
		// "I could not tell" is not "let it run".
		return true
	}
	return strings.HasPrefix(a.Command, "rm ")
}

// callTool scripts a model that calls shell once with the given command.
func callTool(command string) mockTurn {
	input, _ := json.Marshal(dangerousArgs{Command: command})
	return mockTurn{parts: []provider.StreamPart{
		provider.ToolCall{ToolCallID: "c1", ToolName: "shell", Input: string(input)},
	}}
}

func TestApprovedCallRuns(t *testing.T) {
	var ran []string
	var asked []ai.ToolCall

	res, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{callTool("ls"), {}}},
		Tools: []ai.Tool{ai.RequireApproval(newShellTool(&ran))},
		ApproveTool: func(_ context.Context, call ai.ToolCall) (ai.ApprovalDecision, error) {
			asked = append(asked, call)
			return ai.Approve(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(asked) != 1 || asked[0].ToolName != "shell" {
		t.Fatalf("approvals asked = %+v, want one for shell", asked)
	}
	if len(ran) != 1 || ran[0] != "ls" {
		t.Errorf("ran = %v, want the approved command", ran)
	}
	if exec := res.Steps[0].ToolExecutions[0]; exec.Denied {
		t.Error("an approved call was marked denied")
	}
}

func TestDeniedCallDoesNotRunAndTellsTheModelWhy(t *testing.T) {
	var ran []string

	res, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{callTool("rm -rf /"), {}}},
		Tools: []ai.Tool{ai.RequireApproval(newShellTool(&ran))},
		ApproveTool: func(context.Context, ai.ToolCall) (ai.ApprovalDecision, error) {
			return ai.Deny("that would delete everything"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(ran) != 0 {
		t.Fatalf("ran = %v, want nothing executed", ran)
	}

	exec := res.Steps[0].ToolExecutions[0]
	if !exec.Denied {
		t.Error("the refused call was not marked denied")
	}

	// The refusal has to reach the model, or it retries the same thing.
	out, ok := exec.Result.Output().(provider.ToolOutputExecutionDenied)
	if !ok {
		t.Fatalf("output = %T, want an execution-denied result", exec.Result.Output())
	}
	if out.Reason != "that would delete everything" {
		t.Errorf("reason = %q", out.Reason)
	}

	// The run continues so the model can choose something else.
	if len(res.Steps) != 2 {
		t.Errorf("steps = %d, want the run to continue after a refusal", len(res.Steps))
	}
}

func TestToolDecidesWhichCallsNeedApproval(t *testing.T) {
	var ran []string
	var asked int

	tool := writeOnlyApproval{Tool: newShellTool(&ran)}

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{callTool("ls"), callTool("rm -rf /"), {}}},
		Tools: []ai.Tool{tool},
		ApproveTool: func(context.Context, ai.ToolCall) (ai.ApprovalDecision, error) {
			asked++
			return ai.Deny("no"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Only the write was asked about; the read ran unprompted.
	if asked != 1 {
		t.Errorf("approvals asked = %d, want 1 (only the rm)", asked)
	}
	if len(ran) != 1 || ran[0] != "ls" {
		t.Errorf("ran = %v, want only the read", ran)
	}
}

func TestCallerPolicyCanRequireApprovalForAnyTool(t *testing.T) {
	var ran []string
	var asked int

	// The tool asks for nothing; the caller's policy demands approval anyway.
	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{callTool("ls"), {}}},
		Tools: []ai.Tool{newShellTool(&ran)},
		NeedsApproval: func(_ context.Context, call ai.ToolCall) (bool, error) {
			return call.ToolName == "shell", nil
		},
		ApproveTool: func(context.Context, ai.ToolCall) (ai.ApprovalDecision, error) {
			asked++
			return ai.Deny("policy"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if asked != 1 {
		t.Errorf("approvals asked = %d, want 1", asked)
	}
	if len(ran) != 0 {
		t.Errorf("ran = %v, want nothing", ran)
	}
}

func TestMissingApproverFailsRatherThanRunning(t *testing.T) {
	var ran []string

	// No ApproveTool. Executing anyway would run a call the tool said was
	// dangerous, because the program forgot to wire up an approver.
	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{callTool("rm -rf /"), {}}},
		Tools: []ai.Tool{ai.RequireApproval(newShellTool(&ran))},
	})

	if !errors.Is(err, ai.ErrNoApprover) {
		t.Fatalf("err = %v, want ErrNoApprover", err)
	}
	if len(ran) != 0 {
		t.Errorf("ran = %v, want nothing executed", ran)
	}
}

func TestApproverErrorEndsTheRun(t *testing.T) {
	sentinel := errors.New("the UI went away")
	var ran []string

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{callTool("ls"), {}}},
		Tools: []ai.Tool{ai.RequireApproval(newShellTool(&ran))},
		ApproveTool: func(context.Context, ai.ToolCall) (ai.ApprovalDecision, error) {
			return ai.ApprovalDecision{}, sentinel
		},
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the approver's error", err)
	}
	if len(ran) != 0 {
		t.Errorf("ran = %v, want nothing executed", ran)
	}
}

func TestStreamCarriesTheApprovalRequestBeforeTheResult(t *testing.T) {
	var ran []string

	res, err := ai.StreamText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{callTool("ls"), {}}},
		Tools: []ai.Tool{ai.RequireApproval(newShellTool(&ran))},
		ApproveTool: func(context.Context, ai.ToolCall) (ai.ApprovalDecision, error) {
			return ai.Approve(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var request *provider.ToolApprovalRequest
	var order []string

	for part := range res.Stream {
		switch v := part.(type) {
		case provider.ToolApprovalRequest:
			request = &v
			order = append(order, "request")
		case ai.ToolExecuted:
			order = append(order, "executed")
		}
	}
	if _, err := res.Final(); err != nil {
		t.Fatal(err)
	}

	if request == nil {
		t.Fatal("no approval request reached the stream")
	}
	if request.ToolCallID != "c1" {
		t.Errorf("request = %+v, want it keyed to the call", request)
	}
	if request.ApprovalID == "" {
		t.Error("the request carries no approval id")
	}
	// A UI cannot render the question after it has already seen the answer.
	if strings.Join(order, ",") != "request,executed" {
		t.Errorf("stream order = %v, want the request before the result", order)
	}
}

func TestApprovalsAreAskedInCallOrder(t *testing.T) {
	var ran []string
	var asked []string

	first, _ := json.Marshal(dangerousArgs{Command: "one"})
	second, _ := json.Marshal(dangerousArgs{Command: "two"})

	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{
			{parts: []provider.StreamPart{
				provider.ToolCall{ToolCallID: "c1", ToolName: "shell", Input: string(first)},
				provider.ToolCall{ToolCallID: "c2", ToolName: "shell", Input: string(second)},
			}},
			{},
		}},
		Tools: []ai.Tool{ai.RequireApproval(newShellTool(&ran))},
		ApproveTool: func(_ context.Context, call ai.ToolCall) (ai.ApprovalDecision, error) {
			var a dangerousArgs
			json.Unmarshal(call.Input, &a)
			asked = append(asked, a.Command)
			// Refuse the second, so the pairing of answer to question matters.
			return ai.ApprovalDecision{Approved: a.Command == "one"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A person answering cannot attribute answers to questions asked at once.
	if strings.Join(asked, ",") != "one,two" {
		t.Errorf("asked = %v, want them in call order", asked)
	}
	if len(ran) != 1 || ran[0] != "one" {
		t.Errorf("ran = %v, want only the approved call", ran)
	}
}

// An approver may rewrite a call's arguments, and the rewritten input is what
// runs. This is the seam a command-compressing wrapper needs.
func TestApproverCanRewriteTheInput(t *testing.T) {
	var ran []string
	res, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{callTool("git status"), {}}},
		Tools: []ai.Tool{ai.RequireApproval(newShellTool(&ran))},
		ApproveTool: func(context.Context, ai.ToolCall) (ai.ApprovalDecision, error) {
			return ai.ApproveWith(json.RawMessage(`{"command":"rtk git status"}`)), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != "rtk git status" {
		t.Errorf("ran = %v, want the rewritten command", ran)
	}
	// The STEP keeps what the model actually said. Rewriting that too would
	// tell the model on the next turn that it asked for something it did not.
	if got := string(res.Steps[0].ToolCalls[0].Input); !strings.Contains(got, `"git status"`) {
		t.Errorf("the recorded call was rewritten: %s", got)
	}
}

// A rewrite that is not valid JSON must leave the original call alone rather
// than failing a call the approver meant to allow.
func TestInvalidRewriteIsIgnored(t *testing.T) {
	var ran []string
	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{callTool("git status"), {}}},
		Tools: []ai.Tool{ai.RequireApproval(newShellTool(&ran))},
		ApproveTool: func(context.Context, ai.ToolCall) (ai.ApprovalDecision, error) {
			return ai.ApproveWith(json.RawMessage("not json")), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != "git status" {
		t.Errorf("ran = %v, want the original command", ran)
	}
}

// A rewrite on a DENIED call is ignored — the call does not run at all.
func TestRewriteOnDenialChangesNothing(t *testing.T) {
	var ran []string
	_, err := ai.GenerateText(context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{callTool("git status"), {}}},
		Tools: []ai.Tool{ai.RequireApproval(newShellTool(&ran))},
		ApproveTool: func(context.Context, ai.ToolCall) (ai.ApprovalDecision, error) {
			d := ai.Deny("no")
			d.UpdatedInput = json.RawMessage(`{"command":"rtk git status"}`)
			return d, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Errorf("a denied call ran: %v", ran)
	}
}
