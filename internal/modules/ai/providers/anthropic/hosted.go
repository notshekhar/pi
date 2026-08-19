package anthropic

import (
	"encoding/json"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// Anthropic reports a hosted tool as two blocks: a server_tool_use carrying
// the call, then a {tool}_tool_result carrying what it produced. Both arrive
// inside the assistant turn, because the provider ran the tool itself.
//
// This file turns those into spec content. The shapes are per tool and the
// error shapes differ from the success shapes, which is most of the work.

// serverToolNames maps the name in a server_tool_use block onto the tool the
// caller declared. The 2025-08-25 code execution tool reports two internal
// names for what is one tool from the outside.
var serverToolNames = map[string]string{
	"web_search":                 "web_search",
	"web_fetch":                  "web_fetch",
	"code_execution":             "code_execution",
	"bash_code_execution":        "code_execution",
	"text_editor_code_execution": "code_execution",
}

// hostedToolCall converts a server_tool_use block into a provider-executed
// tool call. It reports false for a tool this package does not model.
func hostedToolCall(block apiBlock) (provider.ToolCall, bool) {
	name, ok := serverToolNames[block.Name]
	if !ok {
		return provider.ToolCall{}, false
	}

	input := string(block.Input)
	if input == "" {
		input = "{}"
	}

	return provider.ToolCall{
		ToolCallID: block.ID,
		ToolName:   name,
		Input:      input,
		// The call has already run: executing it locally would be a second,
		// unrelated invocation.
		ProviderExecuted: true,
		ProviderMetadata: provider.ProviderMetadata{
			providerID: {"serverToolName": block.Name},
		},
	}, true
}

// hostedResultBlocks converts a hosted tool's result block into spec content.
//
// Web search yields a tool result plus one Source per hit, so a caller can
// cite the pages the model read without parsing the result payload.
func hostedResultBlocks(block apiBlock, generateID func() string) []provider.Content {
	switch block.Type {
	case "web_search_tool_result":
		return webSearchResult(block, generateID)

	case "web_fetch_tool_result":
		return []provider.Content{toolResultFor("web_fetch", block)}

	case "code_execution_tool_result",
		"bash_code_execution_tool_result",
		"text_editor_code_execution_tool_result":
		return []provider.Content{toolResultFor("code_execution", block)}

	default:
		return nil
	}
}

// webSearchResult renders a web_search_tool_result block.
func webSearchResult(block apiBlock, generateID func() string) []provider.Content {
	results, err := decodeSearchResults(block.Content)
	if err != nil || results == nil {
		// A non-array content is the error shape: {"type":"...error",
		// "error_code":"..."}.
		return []provider.Content{toolResultFor("web_search", block)}
	}

	out := []provider.Content{provider.ToolResult{
		ToolCallID: block.ToolUseID,
		ToolName:   "web_search",
		Result:     block.Content,
	}}

	for _, r := range results {
		out = append(out, provider.Source{
			SourceType: provider.SourceURL,
			ID:         generateID(),
			URL:        r.URL,
			Title:      r.Title,
			ProviderMetadata: provider.ProviderMetadata{
				providerID: {"pageAge": r.PageAge},
			},
		})
	}
	return out
}

// searchResult is one hit in a web_search_tool_result.
type searchResult struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	PageAge string `json:"page_age"`
}

// decodeSearchResults reads the result array, returning nil when the content
// is the error object rather than a list of hits.
func decodeSearchResults(content any) ([]searchResult, error) {
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || raw[0] != '[' {
		return nil, nil
	}

	var results []searchResult
	if err := json.Unmarshal(raw, &results); err != nil {
		return nil, err
	}
	return results, nil
}

// toolResultFor wraps a result block's payload verbatim, which keeps fields
// this package does not model reachable by the caller.
func toolResultFor(toolName string, block apiBlock) provider.ToolResult {
	return provider.ToolResult{
		ToolCallID: block.ToolUseID,
		ToolName:   toolName,
		Result:     block.Content,
		IsError:    isErrorContent(block.Content),
	}
}

// isErrorContent reports whether a result payload is one of Anthropic's error
// objects, which are distinguished only by a "_error" type suffix.
func isErrorContent(content any) bool {
	obj, ok := content.(map[string]any)
	if !ok {
		return false
	}
	kind, _ := obj["type"].(string)
	return len(kind) > 6 && kind[len(kind)-6:] == "_error"
}

// hostedResultBlockTypes maps a hosted tool onto the block type its result
// must be replayed as. Sending a plain tool_result for one of these is
// rejected: the API matches the block type against the tool that produced it.
var hostedResultBlockTypes = map[string]string{
	"web_search":     "web_search_tool_result",
	"web_fetch":      "web_fetch_tool_result",
	"code_execution": "code_execution_tool_result",
}

// hostedResultReplay renders a hosted tool's result for the next turn,
// reporting false when the part belongs to an ordinary client-side tool.
//
// The payload goes back exactly as it arrived, because it carries encrypted
// fields the API validates and cannot be reconstructed from a summary.
func hostedResultReplay(p provider.ToolResultPart) (apiBlock, bool) {
	blockType, ok := hostedResultBlockTypes[p.ToolName]
	if !ok {
		return apiBlock{}, false
	}

	out, ok := p.Output.(provider.ToolOutputJSON)
	if !ok {
		return apiBlock{}, false
	}

	return apiBlock{
		Type:      blockType,
		ToolUseID: p.ToolCallID,
		Content:   out.Value,
	}, true
}

// newSourceID makes an id for a synthesised Source part, which Anthropic does
// not number itself.
func newSourceID() string { return providerutil.GenerateID("src", 12) }

// unknownBlock reports a block type this package does not model, so that a new
// server-side feature is visible rather than silently missing.
func unknownBlock(block apiBlock) provider.CustomContent {
	return provider.CustomContent{
		Kind: providerID + "." + block.Type,
		ProviderMetadata: provider.ProviderMetadata{
			providerID: {"type": block.Type},
		},
	}
}
