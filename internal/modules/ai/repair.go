package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/notshekhar/pi/internal/modules/ai/jsonschema"
)

// RepairContext describes a tool call that could not be used as it stands.
type RepairContext struct {
	// Call is what the model produced, with Input exactly as it arrived.
	Call ToolCall

	// Tool is the tool the call named, or nil when the name matched nothing.
	// A nil Tool means the repair has to pick a different tool, not just fix
	// the arguments.
	Tool Tool

	// Tools are the tools this step offered, for a repair that has to choose
	// among them.
	Tools []Tool

	// Err is why the call was unusable: unparseable arguments, or a name that
	// matched no tool.
	Err error
}

// RepairToolCallFunc gets a second chance at a broken tool call.
//
// Return the corrected call to retry it, or nil to let the failure be reported
// to the model as usual. Returning an error ends the run.
//
// The usual implementation asks a cheap model to rewrite the arguments against
// the tool's schema, which is worth doing because the alternative — feeding the
// error back — costs a whole extra step of the expensive model.
type RepairToolCallFunc func(ctx context.Context, rc RepairContext) (*ToolCall, error)

// repairCall gives a broken call one chance to be fixed.
//
// It reports the call to use and whether a repair happened. A repair that
// fails, returns nothing, or produces something still broken leaves the
// original call to be reported to the model.
func (r *runner) repairCall(ctx context.Context, plan stepPlan, call ToolCall, cause error) (ToolCall, bool, error) {
	if r.opts.RepairToolCall == nil {
		return call, false, nil
	}

	repaired, err := r.opts.RepairToolCall(ctx, RepairContext{
		Call:  call,
		Tool:  plan.dispatch()[call.ToolName],
		Tools: plan.tools,
		Err:   cause,
	})
	if err != nil {
		return call, false, fmt.Errorf("pi: repairing tool call %s: %w", call.ToolCallID, err)
	}
	if repaired == nil {
		return call, false, nil
	}

	// The repair keeps the original call id: the result has to answer the call
	// the model actually made, or the provider rejects the pairing.
	repaired.ToolCallID = call.ToolCallID
	if len(repaired.Input) == 0 {
		repaired.Input = json.RawMessage("{}")
	}

	return *repaired, true, nil
}

// brokenCall reports why a call cannot be executed as it stands, or nil when
// it is fine.
//
// This runs before execution so that a repair happens once, rather than the
// tool discovering the problem and reporting it to the model.
func (r *runner) brokenCall(plan stepPlan, call ToolCall) error {
	tool, ok := plan.dispatch()[call.ToolName]
	if !ok {
		return fmt.Errorf("unknown tool %q; available tools: %s", call.ToolName, plan.toolNames())
	}

	// An empty or null input is legitimate for a tool with no arguments.
	if len(call.Input) == 0 || string(call.Input) == "null" {
		return nil
	}
	if !json.Valid(call.Input) {
		return fmt.Errorf("arguments for %s are not valid JSON: %s",
			call.ToolName, truncate(string(call.Input), 200))
	}

	// A schema mismatch is left to the tool: the reflector's schema is
	// advisory, and rejecting on it here would refuse calls the tool would
	// have accepted.
	_ = tool
	return nil
}

// SchemaFor returns a tool's input schema, which a repair function needs to
// tell the model what shape to produce.
func SchemaFor(tool Tool) *jsonschema.Schema {
	if tool == nil {
		return nil
	}
	return tool.InputSchema()
}

// repairCalls gives every broken call in a step one chance to be fixed.
//
// It returns the calls to execute. Anything still broken is left as it is, to
// be reported to the model by executeTool.
func (r *runner) repairCalls(ctx context.Context, plan stepPlan, calls []ToolCall) ([]ToolCall, error) {
	if r.opts.RepairToolCall == nil || len(calls) == 0 {
		return calls, nil
	}

	var out []ToolCall
	for i, call := range calls {
		cause := r.brokenCall(plan, call)
		if cause == nil {
			continue
		}

		repaired, ok, err := r.repairCall(ctx, plan, call, cause)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}

		// Copy lazily: a step whose calls are all fine allocates nothing.
		if out == nil {
			out = make([]ToolCall, len(calls))
			copy(out, calls)
		}
		out[i] = repaired
	}

	if out == nil {
		return calls, nil
	}
	return out, nil
}
