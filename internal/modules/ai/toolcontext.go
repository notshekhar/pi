package ai

import "context"

// The tool call's identity, carried to the tool that is running.
//
// A tool sometimes needs to say something about ITSELF while it runs — a
// subagent reporting what it is doing, a long download reporting progress —
// and the only stable handle on "this call" is the id the model assigned it.
// Passing it through the context keeps `Tool` a plain function of its
// arguments, which is what makes tools trivial to write and test.

type toolCallIDKey struct{}

// withToolCallID tags a context with the call being executed.
func withToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDKey{}, id)
}

// ToolCallID is the id of the call currently executing, or "" outside a tool.
func ToolCallID(ctx context.Context) string {
	id, _ := ctx.Value(toolCallIDKey{}).(string)
	return id
}
