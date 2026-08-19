// Package pi is the core of pigo: a Go port of the Vercel AI SDK's model
// layer, aimed at command-line agents.
//
// The entry points are GenerateText and StreamText. Both take a model from a
// provider package, run the model, execute any tools it calls, and keep
// stepping until the model stops asking for tools.
//
//	model := anthropic.New(anthropic.Options{}).LanguageModel("claude-opus-5")
//
//	res, err := ai.StreamText(ctx, ai.Options{
//	    Model:    model,
//	    System:   "You are a terse assistant.",
//	    Messages: []provider.Message{ai.UserText("what is in /tmp?")},
//	    Tools:    []ai.Tool{listDir},
//	})
//	for part := range res.Stream {
//	    if d, ok := part.(provider.TextDelta); ok {
//	        fmt.Print(d.Delta)
//	    }
//	}
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/notshekhar/pi/internal/modules/ai/jsonschema"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// Tool is something the model can call.
//
// Implement it directly for full control, or use NewTool, which derives the
// input schema from a Go type.
type Tool interface {
	// Name is how the model refers to the tool. It must be unique within a
	// call and stable across turns.
	Name() string

	// Description tells the model when the tool applies. It is the model's
	// only guidance, so it is worth writing carefully.
	Description() string

	// InputSchema describes the arguments. Returning nil means the tool takes
	// no arguments.
	InputSchema() *jsonschema.Schema

	// Execute runs the tool. input is the raw JSON the model produced.
	//
	// A returned error is reported to the model as a tool failure and the
	// conversation continues, which is almost always what an agent wants: the
	// model can read the message and try something else. To abort the whole
	// run instead, return an error wrapping ErrAbortRun.
	Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
}

// ToolResult is what a tool reports back to the model.
//
// Build one with ToolText, ToolJSON, ToolError or ToolContent.
type ToolResult struct {
	// output is the spec-level payload. It is unexported so that the
	// constructors stay the only way to build a well-formed result.
	output provider.ToolResultOutput
}

// Output returns the spec-level payload, for callers assembling a prompt by
// hand. The zero ToolResult reports empty text.
func (r ToolResult) Output() provider.ToolResultOutput {
	if r.output == nil {
		return provider.ToolOutputText{}
	}
	return r.output
}

// ToolText reports a plain-text result.
func ToolText(text string) ToolResult {
	return ToolResult{output: provider.ToolOutputText{Value: text}}
}

// ToolTextf reports a formatted plain-text result.
func ToolTextf(format string, args ...any) ToolResult {
	return ToolText(fmt.Sprintf(format, args...))
}

// ToolJSON reports a structured result. The value must be JSON-serializable.
func ToolJSON(value any) ToolResult {
	return ToolResult{output: provider.ToolOutputJSON{Value: value}}
}

// ToolError reports a failure the model should see and react to.
func ToolError(message string) ToolResult {
	return ToolResult{output: provider.ToolOutputErrorText{Value: message}}
}

// ToolErrorf reports a formatted failure.
func ToolErrorf(format string, args ...any) ToolResult {
	return ToolError(fmt.Sprintf(format, args...))
}

// ToolContent reports multi-modal output, such as text plus a screenshot.
// Not every provider accepts every part; unsupported ones are replaced with a
// placeholder and a warning.
func ToolContent(parts ...provider.ToolContentPart) ToolResult {
	return ToolResult{output: provider.ToolOutputContent{Value: parts}}
}

// ToolDenied reports that the user refused to let the tool run.
func ToolDenied(reason string) ToolResult {
	return ToolResult{output: provider.ToolOutputExecutionDenied{Reason: reason}}
}

// typedTool adapts a typed function to the Tool interface.
type typedTool[T any] struct {
	name        string
	description string
	schema      *jsonschema.Schema
	execute     func(ctx context.Context, args T) (ToolResult, error)
}

// NewTool builds a tool whose input schema is derived from T.
//
// T should be a struct whose fields carry json tags. A field is required
// unless it is a pointer or its json tag has omitempty; `jsonschema` tags add
// descriptions and constraints:
//
//	type ReadFileArgs struct {
//	    Path   string `json:"path" jsonschema:"description=Absolute path to read"`
//	    Offset *int   `json:"offset,omitempty" jsonschema:"description=Line to start at,minimum=0"`
//	}
//
//	readFile := ai.NewTool("read_file", "Read a file from disk",
//	    func(ctx context.Context, a ReadFileArgs) (ai.ToolResult, error) {
//	        b, err := os.ReadFile(a.Path)
//	        if err != nil {
//	            return ai.ToolError(err.Error()), nil
//	        }
//	        return ai.ToolText(string(b)), nil
//	    })
//
// Use struct{} for a tool that takes no arguments.
func NewTool[T any](
	name, description string,
	execute func(ctx context.Context, args T) (ToolResult, error),
) Tool {
	return &typedTool[T]{
		name:        name,
		description: description,
		schema:      jsonschema.Reflect[T](),
		execute:     execute,
	}
}

// NewToolWithSchema builds a tool with a hand-written input schema, for shapes
// reflection cannot express such as mutually exclusive argument sets.
//
// The schema is what the model sees; args are still decoded into T, so the two
// must agree.
func NewToolWithSchema[T any](
	name, description string,
	schema *jsonschema.Schema,
	execute func(ctx context.Context, args T) (ToolResult, error),
) Tool {
	return &typedTool[T]{
		name:        name,
		description: description,
		schema:      schema,
		execute:     execute,
	}
}

// Name implements Tool.
func (t *typedTool[T]) Name() string { return t.name }

// Description implements Tool.
func (t *typedTool[T]) Description() string { return t.description }

// InputSchema implements Tool.
func (t *typedTool[T]) InputSchema() *jsonschema.Schema { return t.schema }

// Execute implements Tool by decoding the model's arguments into T.
func (t *typedTool[T]) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var args T

	// A tool with no fields is called with no arguments, and some providers
	// send an empty string rather than "{}" for that case.
	if len(input) > 0 && string(input) != "null" {
		if err := json.Unmarshal(input, &args); err != nil {
			// Malformed arguments are the model's mistake, not the program's,
			// so report them back rather than failing the run. Naming the
			// offending payload lets the model correct itself.
			return ToolErrorf(
				"could not parse arguments for %s: %v (received %s)",
				t.name, err, truncate(string(input), 200),
			), nil
		}
	}

	return t.execute(ctx, args)
}

// truncate shortens a string for inclusion in an error message.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}

// toolSet indexes tools by name for dispatch during a run.
type toolSet map[string]Tool

// newToolSet indexes tools, rejecting duplicates because a duplicate name
// makes dispatch ambiguous and the model's choice unpredictable.
func newToolSet(tools []Tool) (toolSet, error) {
	set := make(toolSet, len(tools))
	for _, t := range tools {
		name := t.Name()
		if name == "" {
			return nil, fmt.Errorf("pi: tool %s has an empty name", reflect.TypeOf(t))
		}
		if _, exists := set[name]; exists {
			return nil, fmt.Errorf("pi: duplicate tool name %q", name)
		}
		set[name] = t
	}
	return set, nil
}

// specTools renders the tools as provider definitions, preserving the caller's
// order so the prompt stays byte-identical between turns and the provider's
// prompt cache keeps hitting.
//
// Hosted tools come last, after the client-executed ones, for the same reason:
// a stable order.
func specTools(tools []Tool, hosted []provider.ProviderTool) []provider.Tool {
	if len(tools) == 0 && len(hosted) == 0 {
		return nil
	}

	out := make([]provider.Tool, 0, len(tools)+len(hosted))
	for _, t := range tools {
		out = append(out, provider.FunctionTool{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	for _, t := range hosted {
		out = append(out, t)
	}
	return out
}
