package extension

import (
	"context"
	"encoding/json"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// Appending to a tool's result.
//
// The seam the LSP extension needs: after a write or an edit, the file is run
// past the language servers and any errors are appended to what the model
// sees. Appending to the RESULT rather than printing to the transcript is the
// whole point — the agent finds out it broke the build without being asked to
// go and check, which is the difference between an extension that informs the
// user and one that changes what the agent does.
//
// Implemented by WRAPPING the tool rather than by a hook inside the model
// layer: the wrapper is ordinary code, it cannot be forgotten by a new code
// path, and a tool that is never called is never wrapped.

// ResultAugmenter appends to a tool's result.
type ResultAugmenter interface {
	// AugmentedTools names the tools it wants to see. Empty means every tool,
	// which nothing should want.
	AugmentedTools() []string
	// Augment returns text to append, or "" to leave the result alone. It is
	// given the decoded arguments and the result's text.
	//
	// It must not fail the call: a diagnostics pass that cannot run is a
	// missing convenience, not a failed edit. Errors are swallowed by design,
	// so an augmenter reports its own problems in the text it returns.
	Augment(ctx context.Context, tool, result string, args map[string]any) string
}

// WrapTools decorates the tools any enabled augmenter asked for.
func WrapTools(list []Extension, tools []ai.Tool) []ai.Tool {
	var augmenters []ResultAugmenter
	for _, e := range list {
		if a, ok := e.(ResultAugmenter); ok {
			augmenters = append(augmenters, a)
		}
	}
	if len(augmenters) == 0 {
		return tools
	}

	out := make([]ai.Tool, len(tools))
	for i, t := range tools {
		var wanted []ResultAugmenter
		for _, a := range augmenters {
			if covers(a.AugmentedTools(), t.Name()) {
				wanted = append(wanted, a)
			}
		}
		if len(wanted) == 0 {
			out[i] = t
			continue
		}
		out[i] = &augmented{Tool: t, augmenters: wanted}
	}
	return out
}

func covers(names []string, tool string) bool {
	if len(names) == 0 {
		return true
	}
	for _, n := range names {
		if n == tool {
			return true
		}
	}
	return false
}

// augmented is a tool whose result is extended.
type augmented struct {
	ai.Tool
	augmenters []ResultAugmenter
}

func (a *augmented) Execute(ctx context.Context, input json.RawMessage) (ai.ToolResult, error) {
	result, err := a.Tool.Execute(ctx, input)
	if err != nil {
		// A failed call has nothing to append to, and running a diagnostics
		// pass over a write that did not happen would report the file as it
		// was before.
		return result, err
	}

	text, ok := resultText(result)
	if !ok {
		// Structured or multi-modal output — appending prose to it would
		// corrupt the shape the model is expecting.
		return result, nil
	}

	var args map[string]any
	_ = json.Unmarshal(input, &args)

	for _, augmenter := range a.augmenters {
		if extra := augmenter.Augment(ctx, a.Tool.Name(), text, args); extra != "" {
			text += extra
		}
	}
	return ai.ToolText(text), nil
}

// resultText is a result's plain text, and whether it had any.
func resultText(r ai.ToolResult) (string, bool) {
	if out, ok := r.Output().(provider.ToolOutputText); ok {
		return out.Value, true
	}
	return "", false
}
