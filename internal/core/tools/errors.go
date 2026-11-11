package tools

import (
	"context"
	"errors"

	"github.com/notshekhar/pi/internal/ai"
)

// errAborted is what a tool reports when its turn was cancelled, in loop's words.
var errAborted = errors.New("Operation aborted")

// fail reports err to the model as the tool's result. The conversation
// carries on: the model reads the message and tries something else.
func fail(err error) (ai.ToolResult, error) {
	return ai.ToolError(err.Error()), nil
}

func aborted(ctx context.Context) bool {
	return ctx.Err() != nil
}
