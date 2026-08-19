package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/core/permissions"
)

// Gating tool calls on the policy.
//
// The `ai` module already splits this in two: `NeedsApproval` decides whether
// a call must be put to somebody, and `ApproveTool` is that somebody. The
// policy answers the first; the UI answers the second. A `deny` never reaches
// the UI at all — it is refused here, with a reason the model can act on.

// Ask is how the app asks a person about a call. It returns true to allow.
//
// It is called on the goroutine driving the run and may block for as long as
// it needs; cancelling the context abandons the wait.
type Ask func(ctx context.Context, tool string, args map[string]any, reason string) bool

// approvalHooks builds the pair of callbacks the model layer wants.
//
// A nil `ask` means nothing can be approved interactively, so an `ask` rule
// behaves as a deny. That is the safe reading: the one-shot `run` path has
// nobody to prompt, and running the call anyway because no one was listening
// is exactly the failure this package exists to prevent.
func (r *Run) approvalHooks() (
	needs func(context.Context, ai.ToolCall) (bool, error),
	approve func(context.Context, ai.ToolCall) (ai.ApprovalDecision, error),
) {
	policy := r.policy()

	decide := func(call ai.ToolCall) permissions.Decision {
		return policy.Decide(call.ToolName, decodeArgs(call.Input))
	}

	needs = func(_ context.Context, call ai.ToolCall) (bool, error) {
		// A PreToolUse hook has to see EVERY call, including the ones the
		// policy allows outright — the whole point of such a hook is to
		// refuse something the policy is happy with. So when one is
		// installed, every call is routed through approve.
		if r.PreTool != nil {
			return true, nil
		}
		return decide(call).Mode != permissions.Allow, nil
	}

	approve = func(ctx context.Context, call ai.ToolCall) (ai.ApprovalDecision, error) {
		args := decodeArgs(call.Input)
		// Hooks run BEFORE the policy: a hook's refusal is the user's own
		// rule, and it should not be possible to reach a call the policy
		// happens to allow by way of a hook that meant to stop it.
		rewritten := false
		if r.PreTool != nil {
			updated, reason := r.PreTool(ctx, call.ToolName, args)
			if reason != "" {
				return ai.Deny(reason), nil
			}
			if updated != nil {
				args, rewritten = updated, true
			}
		}
		if r.RewriteCall != nil {
			if updated := r.RewriteCall(call.ToolName, args); updated != nil {
				args, rewritten = updated, true
			}
		}
		// The POLICY sees the rewritten call, not the original. A rewrite
		// that turned `ls` into `rm -rf /` must be judged as what will
		// actually run — anything else makes the rewrite seam a way around
		// the permission rules.
		if rewritten {
			if encoded, err := json.Marshal(args); err == nil {
				call.Input = encoded
			} else {
				// Unencodable — run the original rather than a half-applied
				// rewrite the policy never saw.
				args, rewritten = decodeArgs(call.Input), false
			}
		}
		d := decide(call)
		approved := ai.Approve()
		if rewritten {
			approved = ai.ApproveWith(call.Input)
		}
		switch d.Mode {
		case permissions.Allow:
			return approved, nil

		case permissions.Deny:
			reason := d.Reason
			if reason == "" {
				reason = fmt.Sprintf("blocked by policy (%s)", d.Rule)
			}
			return ai.Deny(reason), nil
		}

		if r.Ask == nil {
			return ai.Deny("this call needs approval and there is nobody to ask"), nil
		}
		if r.OnPermissionRequest != nil {
			r.OnPermissionRequest(ctx, call.ToolName, args, d.Reason)
		}
		if r.Ask(ctx, call.ToolName, args, d.Reason) {
			return approved, nil
		}
		return ai.Deny("the user declined this call"), nil
	}
	return needs, approve
}

// policy is the run's effective policy, defaulted if unset.
//
// Plan mode REPLACES the policy rather than adding to it. Layering it on top
// would leave a stored `allow write` able to cancel the very restriction the
// mode exists to impose.
func (r *Run) policy() permissions.Policy {
	if r.Planning {
		return permissions.Plan(r.Config.CWD)
	}
	p := r.Permissions
	if p.Default == "" {
		p = permissions.Default(r.Config.CWD)
	}
	// The working directory can move mid-session (/cd), and the confinement
	// check has to follow it.
	p.CWD = r.Config.CWD
	return p
}

// decodeArgs turns a call's raw JSON into the map the policy matches on.
// Unparseable input yields nil, which matches no pattern — so a malformed
// call falls through to the policy's default rather than dodging a rule.
func decodeArgs(input json.RawMessage) map[string]any {
	if len(input) == 0 {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal(input, &args); err != nil {
		return nil
	}
	return args
}
