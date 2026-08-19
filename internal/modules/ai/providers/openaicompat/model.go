package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// languageModel implements provider.LanguageModel over the chat completions API.
type languageModel struct {
	modelID  string
	provider *Provider
}

// SpecificationVersion implements provider.LanguageModel.
func (m *languageModel) SpecificationVersion() string { return provider.SpecificationVersion }

// Provider implements provider.LanguageModel.
func (m *languageModel) Provider() string { return m.provider.name }

// ModelID implements provider.LanguageModel.
func (m *languageModel) ModelID() string { return m.modelID }

// httpURLPattern matches any http(s) URL.
var httpURLPattern = regexp.MustCompile(`^https?://.*`)

// SupportedURLs implements provider.LanguageModel.
//
// Image URLs are passed through, since the chat API fetches them server-side.
// Everything else is downloaded and inlined by core.
func (m *languageModel) SupportedURLs(context.Context) (map[string][]*regexp.Regexp, error) {
	return map[string][]*regexp.Regexp{
		"image/*": {httpURLPattern},
	}, nil
}

// buildRequest renders call options into a chat completions body.
func (m *languageModel) buildRequest(opts provider.CallOptions, stream bool) (*chatRequest, []provider.Warning, error) {
	messages, warnings, err := convertPrompt(opts.Prompt)
	if err != nil {
		return nil, nil, err
	}

	req := &chatRequest{
		Model:            m.modelID,
		Messages:         messages,
		Temperature:      opts.Temperature,
		TopP:             opts.TopP,
		FrequencyPenalty: opts.FrequencyPenalty,
		PresencePenalty:  opts.PresencePenalty,
		Seed:             opts.Seed,
		Stop:             opts.StopSequences,
		Stream:           stream,
	}

	if stream && m.provider.includeUsage {
		// Without this a streaming response reports no usage at all.
		req.StreamOptions = &streamOptions{IncludeUsage: true}
	}

	if opts.MaxOutputTokens != nil {
		if m.provider.useMaxCompletionTokens {
			req.MaxCompletionTokens = opts.MaxOutputTokens
		} else {
			req.MaxTokens = opts.MaxOutputTokens
		}
	}

	if opts.TopK != nil {
		warnings = append(warnings, provider.Unsupported(
			"topK", "the chat completions API has no top_k parameter",
		))
	}

	if effort := opts.Reasoning; effort != "" && effort != provider.ReasoningDefault {
		req.ReasoningEffort = mapReasoningEffort(effort)
	}

	toolWarnings, err := m.applyTools(req, opts)
	if err != nil {
		return nil, nil, err
	}
	warnings = append(warnings, toolWarnings...)

	warnings = append(warnings, m.applyResponseFormat(req, opts)...)

	// Provider-specific extras are merged last so a caller can override
	// anything computed above.
	if extra := opts.ProviderOptions.Get(m.provider.optionsKey); extra != nil {
		req.Extra = extra
	}

	return req, warnings, nil
}

// mapReasoningEffort maps the unified effort onto the strings the chat API
// accepts. "xhigh" has no equivalent, so it saturates at "high".
func mapReasoningEffort(effort provider.ReasoningEffort) string {
	switch effort {
	case provider.ReasoningNone:
		return "none"
	case provider.ReasoningMinimal:
		return "minimal"
	case provider.ReasoningLow:
		return "low"
	case provider.ReasoningMedium:
		return "medium"
	case provider.ReasoningHigh, provider.ReasoningXHigh:
		return "high"
	default:
		return ""
	}
}

// applyTools renders the tool list and tool choice.
func (m *languageModel) applyTools(req *chatRequest, opts provider.CallOptions) ([]provider.Warning, error) {
	var warnings []provider.Warning

	for _, tool := range opts.Tools {
		switch t := tool.(type) {
		case provider.FunctionTool:
			params, err := schemaForTool(t)
			if err != nil {
				return nil, err
			}
			fn := apiFunction{Name: t.Name, Description: t.Description, Parameters: params}
			if t.Strict {
				strict := true
				fn.Strict = &strict
			}
			req.Tools = append(req.Tools, apiTool{Type: "function", Function: fn})

		case provider.ProviderTool:
			warnings = append(warnings, provider.Unsupported(
				"provider-executed tools",
				fmt.Sprintf("tool %q was dropped: this provider does not host tools", t.Name),
			))
		}
	}

	if opts.ToolChoice == nil {
		return warnings, nil
	}

	switch opts.ToolChoice.Type {
	case provider.ToolChoiceAuto:
		req.ToolChoice = "auto"
	case provider.ToolChoiceNone:
		req.ToolChoice = "none"
	case provider.ToolChoiceRequired:
		req.ToolChoice = "required"
	case provider.ToolChoiceTool:
		req.ToolChoice = map[string]any{
			"type":     "function",
			"function": map[string]any{"name": opts.ToolChoice.ToolName},
		}
	}

	return warnings, nil
}

// emptyObjectSchema is what a tool with no arguments must send.
var emptyObjectSchema = map[string]any{
	"type":       "object",
	"properties": map[string]any{},
}

// schemaForTool renders a tool's input schema for the wire.
func schemaForTool(t provider.FunctionTool) (any, error) {
	if t.InputSchema == nil {
		return emptyObjectSchema, nil
	}
	raw, err := json.Marshal(t.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: encoding schema for tool %q: %w", t.Name, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("openaicompat: decoding schema for tool %q: %w", t.Name, err)
	}
	delete(out, "$schema")
	return out, nil
}

// applyResponseFormat handles JSON output requests.
func (m *languageModel) applyResponseFormat(req *chatRequest, opts provider.CallOptions) []provider.Warning {
	if opts.ResponseFormat == nil || opts.ResponseFormat.Type != "json" {
		return nil
	}

	if opts.ResponseFormat.Schema == nil {
		req.ResponseFormat = map[string]any{"type": "json_object"}
		return nil
	}

	if m.provider.noJSONSchema {
		// The gateway has json_object but not json_schema (DeepSeek answers
		// "This response_format type is unavailable now"). The schema still
		// has to reach the model, so it goes in the prompt instead.
		return m.applySchemaViaPrompt(req, opts)
	}

	raw, err := json.Marshal(opts.ResponseFormat.Schema)
	if err != nil {
		return []provider.Warning{provider.Unsupported("responseFormat schema", err.Error())}
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return []provider.Warning{provider.Unsupported("responseFormat schema", err.Error())}
	}
	delete(schema, "$schema")

	name := opts.ResponseFormat.Name
	if name == "" {
		name = "response"
	}

	req.ResponseFormat = map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":        name,
			"description": opts.ResponseFormat.Description,
			"schema":      schema,
			"strict":      true,
		},
	}
	return nil
}

// applySchemaViaPrompt is the fallback for gateways with only a schema-less
// JSON mode: ask for JSON on the wire, and describe the shape in a system
// message the model reads.
//
// It is weaker than a real json_schema — nothing enforces the shape — which is
// why it is opt-in per provider rather than a silent default.
func (m *languageModel) applySchemaViaPrompt(req *chatRequest, opts provider.CallOptions) []provider.Warning {
	raw, err := json.MarshalIndent(opts.ResponseFormat.Schema, "", "  ")
	if err != nil {
		return []provider.Warning{provider.Unsupported("responseFormat schema", err.Error())}
	}

	req.ResponseFormat = map[string]any{"type": "json_object"}

	instruction := "Respond with a single JSON object matching this JSON Schema. " +
		"Output only the object, with no explanation and no markdown fence.\n\n" + string(raw)
	if d := opts.ResponseFormat.Description; d != "" {
		instruction = d + "\n\n" + instruction
	}

	// The instruction goes last so it is the nearest thing to the model's
	// turn, which is where models follow formatting rules most reliably.
	req.Messages = append(req.Messages, apiMessage{Role: "system", Content: instruction})

	return []provider.Warning{{
		Type:    provider.WarningOther,
		Feature: "responseFormat schema",
		Details: "this provider has no json_schema mode; the schema was described in the prompt and is not enforced",
	}}
}

// DoGenerate implements provider.LanguageModel.
func (m *languageModel) DoGenerate(ctx context.Context, opts provider.CallOptions) (*provider.GenerateResult, error) {
	req, warnings, err := m.buildRequest(opts, false)
	if err != nil {
		return nil, err
	}
	body, err := marshalRequest(req)
	if err != nil {
		return nil, err
	}

	resp, err := providerutil.Retry(ctx, m.provider.retry, func(ctx context.Context) (*chatResponse, error) {
		httpResp, err := m.provider.client.PostJSON(ctx, m.provider.chatPath, json.RawMessage(body), opts.Headers)
		if err != nil {
			return nil, err
		}
		var decoded chatResponse
		if err := providerutil.DecodeJSON(httpResp, &decoded); err != nil {
			return nil, err
		}
		return &decoded, nil
	})
	if err != nil {
		return nil, err
	}

	if len(resp.Choices) == 0 {
		return nil, &provider.InvalidResponseError{
			Message: "the provider returned no choices",
		}
	}
	c := resp.Choices[0]
	if c.Message == nil {
		return nil, &provider.InvalidResponseError{
			Message: "the provider returned a choice with no message",
		}
	}

	var content []provider.Content
	if reasoning := c.Message.reasoningText(); reasoning != "" {
		content = append(content, provider.Reasoning{Text: reasoning})
	}
	if c.Message.Content != "" {
		content = append(content, provider.Text{Text: c.Message.Content})
	}
	for _, call := range c.Message.ToolCalls {
		input := call.Function.Arguments
		if strings.TrimSpace(input) == "" {
			input = "{}"
		}
		content = append(content, provider.ToolCall{
			ToolCallID: call.ID,
			ToolName:   call.Function.Name,
			Input:      input,
		})
	}

	return &provider.GenerateResult{
		Content:      content,
		FinishReason: mapFinishReason(c.FinishReason),
		Usage:        convertUsage(resp.Usage),
		Warnings:     warnings,
		Request:      &provider.RequestInfo{Body: string(body)},
		Response: &provider.ResponseInfo{
			ResponseMetadata: provider.ResponseMetadata{ID: resp.ID, ModelID: resp.Model},
		},
	}, nil
}
