package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// ApprovalDecision is the answer to an approval request.
type ApprovalDecision struct {
	// Approved lets the call run.
	Approved bool

	// Reason is reported to the model when the call is refused, so it can
	// choose something else instead of retrying the same thing. It is ignored
	// when Approved is true.
	Reason string

	// UpdatedInput replaces the call's arguments, when the approver wants the
	// call to run as something other than what the model asked for.
	//
	// It changes what EXECUTES, not what was recorded: the assistant message
	// carrying the original call has already been built by the time approval
	// runs, and rewriting it there would tell the model it said something it
	// did not. That asymmetry is deliberate and is what a rewriting approver
	// wants — `git status` compressed by a wrapper is still `git status` as
	// far as the conversation is concerned.
	//
	// Ignored when Approved is false, and when it is not valid JSON for the
	// tool — a bad rewrite must leave the original call alone rather than
	// failing it.
	UpdatedInput json.RawMessage
}

// Approve allows a tool call.
func Approve() ApprovalDecision { return ApprovalDecision{Approved: true} }

// ApproveWith allows a tool call, running it with different arguments.
func ApproveWith(input json.RawMessage) ApprovalDecision {
	return ApprovalDecision{Approved: true, UpdatedInput: input}
}

// Deny refuses a tool call and tells the model why.
func Deny(reason string) ApprovalDecision {
	return ApprovalDecision{Approved: false, Reason: reason}
}

// ApprovalTool is a Tool that decides for itself whether a call needs
// approval, which is how a tool declares that some of its inputs are dangerous
// and others are not — a shell tool asking only for commands that write.
//
// Implement it alongside Tool; NewTool's tools do not need approval unless
// wrapped with RequireApproval or matched by Options.NeedsApproval.
type ApprovalTool interface {
	Tool

	// NeedsApproval reports whether this particular call must be approved.
	// input is the raw JSON the model produced.
	NeedsApproval(ctx context.Context, input json.RawMessage) bool
}

// RequireApproval wraps a tool so that every call to it needs approval.
func RequireApproval(tool Tool) Tool {
	return alwaysApprove{Tool: tool}
}

// alwaysApprove is a Tool that always requires approval.
type alwaysApprove struct{ Tool }

// NeedsApproval implements ApprovalTool.
func (alwaysApprove) NeedsApproval(context.Context, json.RawMessage) bool { return true }

// ErrNoApprover is returned when a call needs approval but Options.ApproveTool
// is nil.
//
// The run fails rather than executing the call: a tool marked as needing
// approval must never run unapproved because the program forgot to wire up an
// approver.
var ErrNoApprover = fmt.Errorf(
	"pi: a tool call requires approval but Options.ApproveTool is not set")

// needsApproval reports whether a call has to be approved before it runs.
//
// The tool's own judgement and the caller's policy are ORed: either can demand
// approval, and neither can waive what the other requires.
func (r *runner) needsApproval(ctx context.Context, call ToolCall) (bool, error) {
	if tool, ok := r.tools[call.ToolName].(ApprovalTool); ok {
		if tool.NeedsApproval(ctx, call.Input) {
			return true, nil
		}
	}

	if r.opts.NeedsApproval == nil {
		return false, nil
	}
	return r.opts.NeedsApproval(ctx, call)
}

// approve asks for a decision on one call.
func (r *runner) approve(ctx context.Context, call ToolCall) (ApprovalDecision, error) {
	if r.opts.ApproveTool == nil {
		return ApprovalDecision{}, ErrNoApprover
	}
	return r.opts.ApproveTool(ctx, call)
}

// resolveApprovals decides which calls may run.
//
// Requests go out one at a time in the order the model made them: an approver
// is usually a person, and asking three questions at once produces answers
// nobody can attribute. Execution afterwards is still parallel.
//
// emit may be nil for a non-streaming run.
func (r *runner) resolveApprovals(
	ctx context.Context,
	calls []ToolCall,
	emit func(StreamPart) bool,
) (resolved []ToolCall, denials map[string]ToolExecution, err error) {
	// Copied, because an approver may rewrite a call's input and the slice
	// the caller handed us is the same one already recorded in the step.
	resolved = append([]ToolCall{}, calls...)
	for i, call := range resolved {
		// An unknown tool is reported to the model by executeTool, which gives
		// a better message than an approval prompt for a tool that cannot run.
		if _, known := r.tools[call.ToolName]; !known {
			continue
		}

		required, err := r.needsApproval(ctx, call)
		if err != nil {
			return nil, nil, fmt.Errorf("pi: deciding approval for %s: %w", call.ToolName, err)
		}
		if !required {
			continue
		}

		approvalID := newApprovalID()
		if emit != nil {
			// The request reaches the UI before the approver blocks on it,
			// which is what lets a renderer show what is being asked.
			emit(provider.ToolApprovalRequest{
				ApprovalID: approvalID,
				ToolCallID: call.ToolCallID,
			})
		}

		decision, err := r.approve(ctx, call)
		if err != nil {
			return nil, nil, err
		}
		if decision.Approved {
			// A rewrite has to be valid JSON, or the tool would be handed
			// something it cannot decode and fail a call the approver meant
			// to allow.
			if len(decision.UpdatedInput) > 0 && json.Valid(decision.UpdatedInput) {
				resolved[i].Input = decision.UpdatedInput
			}
			continue
		}

		reason := decision.Reason
		if reason == "" {
			reason = "The user did not approve this tool call."
		}

		if denials == nil {
			denials = make(map[string]ToolExecution, 1)
		}
		denials[call.ToolCallID] = ToolExecution{
			ToolCall: call,
			Result:   ToolDenied(reason),
			Denied:   true,
		}
	}

	return resolved, denials, nil
}

// newApprovalID makes an id for one approval exchange.
func newApprovalID() string { return providerutil.GenerateID("approval", 16) }
