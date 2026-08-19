package google

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// Provider-hosted tools. Google runs these itself, inside the same call, so
// nothing is executed locally and the agent loop does not step for them.
//
//	model := google.New(google.Options{}).LanguageModel("gemini-3-pro")
//	res, err := ai.StreamText(ctx, ai.Options{
//	    Model:         model,
//	    Messages:      []provider.Message{ai.UserText("what shipped in Go 1.26?")},
//	    ProviderTools: []provider.ProviderTool{google.Search()},
//	})
//
// Google does not accept hosted tools alongside ordinary function tools in one
// request; asking for both is an error rather than a warning, because the API
// would reject the call anyway and the message would be worse.

// Tool ids in the spec's "{provider}.{tool}" form.
const (
	toolIDSearch        = "google.google_search"
	toolIDURLContext    = "google.url_context"
	toolIDCodeExecution = "google.code_execution"
)

// Search returns Google's hosted search grounding tool.
//
// The pages behind the answer arrive as Source parts, built from the response's
// grounding metadata.
func Search() provider.ProviderTool {
	return provider.ProviderTool{ID: toolIDSearch, Name: "google_search"}
}

// URLContext returns the tool that lets the model fetch URLs the prompt
// mentions and read them as context.
func URLContext() provider.ProviderTool {
	return provider.ProviderTool{ID: toolIDURLContext, Name: "url_context"}
}

// CodeExecution returns Google's hosted Python execution tool.
//
// What ran and what it produced arrive as tool calls and results named
// "code_execution", so they read like any other tool in a transcript.
func CodeExecution() provider.ProviderTool {
	return provider.ProviderTool{ID: toolIDCodeExecution, Name: "code_execution"}
}

// hostedCodeToolName is what the code execution tool is called in the parts
// this package produces, so a transcript reads the same as for any tool.
const hostedCodeToolName = "code_execution"

// hostedCodeCall renders an executableCode part as a provider-executed call.
//
// Google gives it no id, and the call and its result are separate parts with
// nothing linking them, so one id is synthesised and reused: the pairing is
// positional, which is all the API guarantees.
func hostedCodeCall(code executableCode) provider.ToolCall {
	input, err := json.Marshal(map[string]string{
		"language": code.Language,
		"code":     code.Code,
	})
	if err != nil {
		input = []byte("{}")
	}
	return provider.ToolCall{
		ToolCallID:       hostedCodeCallID(code.Code),
		ToolName:         hostedCodeToolName,
		Input:            string(input),
		ProviderExecuted: true,
	}
}

// hostedCodeResult renders a codeExecutionResult part.
func hostedCodeResult(result codeExecutionResult) provider.ToolResult {
	return provider.ToolResult{
		ToolCallID: "",
		ToolName:   hostedCodeToolName,
		Result: map[string]any{
			"outcome": result.Outcome,
			"output":  result.Output,
		},
		// Anything but OUTCOME_OK is a failure the model has to react to.
		IsError: result.Outcome != "" && result.Outcome != "OUTCOME_OK",
	}
}

// hostedCodeCallID derives a stable id from the code itself, so that replaying
// a turn produces the same id rather than a fresh random one.
func hostedCodeCallID(code string) string {
	sum := sha256.Sum256([]byte(code))
	return "code-" + hex.EncodeToString(sum[:8])
}

// applyHostedTool sets the field for one hosted tool, reporting false when the
// id is unknown or the model is too old to support it.
func applyHostedTool(dst *apiTool, t provider.ProviderTool, gemini2 bool) bool {
	if !gemini2 {
		// Pre-Gemini-2 models only have the older search-retrieval shape.
		if t.ID == toolIDSearch {
			dst.GoogleSearchRetrieval = &hostedToolArgs{}
			return true
		}
		return false
	}

	switch t.ID {
	case toolIDSearch:
		// A nil map marshals as null, and Google reads a null tool as absent:
		// the request is accepted and the model answers ungrounded, which is
		// far worse than an error. It must be an object.
		args := hostedToolArgs{}
		for k, v := range t.Args {
			args[k] = v
		}
		dst.GoogleSearch = &args
	case toolIDURLContext:
		dst.URLContext = &hostedToolArgs{}
	case toolIDCodeExecution:
		dst.CodeExecution = &hostedToolArgs{}
	default:
		return false
	}
	return true
}
