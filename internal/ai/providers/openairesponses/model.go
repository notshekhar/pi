package openairesponses

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/ai/providerutil"
)

type languageModel struct {
	modelID  string
	provider *Provider
}

func (m *languageModel) SpecificationVersion() string { return provider.SpecificationVersion }
func (m *languageModel) Provider() string             { return m.provider.name }
func (m *languageModel) ModelID() string              { return m.modelID }

var httpURL = regexp.MustCompile(`^https?://.*`)

// SupportedURLs: the API fetches image and PDF URLs itself.
func (m *languageModel) SupportedURLs(context.Context) (map[string][]*regexp.Regexp, error) {
	return map[string][]*regexp.Regexp{"image/*": {httpURL}, "application/pdf": {httpURL}}, nil
}

// reasoningModel reports whether the request takes reasoning settings.
func (m *languageModel) reasoningModel() (capabilities, bool) {
	c := capabilitiesOf(m.modelID)
	if !c.reasoning && m.provider.forceReasoning != nil && m.provider.forceReasoning(m.modelID) {
		c.reasoning, c.developerRole = true, true
	}
	return c, c.reasoning
}

func effortString(e provider.ReasoningEffort) string {
	switch e {
	case provider.ReasoningNone:
		return "none"
	case provider.ReasoningMinimal:
		return "minimal"
	case provider.ReasoningLow:
		return "low"
	case provider.ReasoningMedium:
		return "medium"
	case provider.ReasoningHigh:
		return "high"
	case provider.ReasoningXHigh:
		return "xhigh"
	}
	return ""
}

// buildRequest renders call options as a Responses body (the AI SDK's
// getArgs, with loop's reasoningSummary "auto").
func (m *languageModel) buildRequest(opts provider.CallOptions) (map[string]any, []provider.Warning, error) {
	if m.provider.xai {
		return m.buildXAIRequest(opts)
	}
	caps, reasoning := m.reasoningModel()
	// loop's forceReasoning, per call.
	if force, _ := opts.ProviderOptions.Get(providerID)["forceReasoning"].(bool); force {
		reasoning, caps.developerRole = true, true
	}
	input, warnings, err := convertPrompt(opts.Prompt, providerID, reasoning, false)
	if err != nil {
		return nil, nil, err
	}
	req := map[string]any{"model": m.modelID, "input": input, "stream": true}
	if len(opts.StopSequences) > 0 {
		warnings = append(warnings, provider.Unsupported("stopSequences", "the Responses API has no stop sequences"))
	}
	if opts.MaxOutputTokens != nil {
		req["max_output_tokens"] = *opts.MaxOutputTokens
	}

	effort := effortString(opts.Reasoning)
	if effort != "" && caps.supportedEfforts != nil && !slices.Contains(caps.supportedEfforts, effort) {
		warnings = append(warnings, provider.Unsupported("reasoningEffort",
			fmt.Sprintf("%s only supports %s", m.modelID, strings.Join(caps.supportedEfforts, ", "))))
		effort = ""
	}
	if reasoning {
		r := map[string]any{}
		if effort != "" {
			r["effort"] = effort
		}
		if effort != "" && effort != "none" {
			r["summary"] = "auto"
		}
		if len(r) > 0 {
			req["reasoning"] = r
		}
		// Reasoning models take no sampling knobs.
		if opts.Temperature != nil || opts.TopP != nil {
			warnings = append(warnings, provider.Unsupported("temperature/topP", "not supported for reasoning models"))
		}
	} else {
		if opts.Temperature != nil {
			req["temperature"] = *opts.Temperature
		}
		if opts.TopP != nil {
			req["top_p"] = *opts.TopP
		}
		if effort != "" && effort != "none" {
			warnings = append(warnings, provider.Unsupported("reasoningEffort", "not supported for non-reasoning models"))
		}
	}

	if err := applyTools(req, opts); err != nil {
		return nil, nil, err
	}
	applyResponseFormat(req, opts, false)

	// Provider options, in the SDK's camelCase, then anything raw.
	for k, v := range opts.ProviderOptions.Get(providerID) {
		switch k {
		case "promptCacheKey":
			req["prompt_cache_key"] = v
		case "store", "user", "metadata", "include", "instructions", "truncation":
			req[k] = v
		case "parallelToolCalls":
			req["parallel_tool_calls"] = v
		case "serviceTier":
			req["service_tier"] = v
		case "reasoningSummary":
			if r, ok := req["reasoning"].(map[string]any); ok {
				r["summary"] = v
			}
		}
	}
	return req, warnings, nil
}

// xaiEffort is @ai-sdk/xai's effort map: xAI takes low, medium and high.
var xaiEffort = map[string]string{"none": "none", "minimal": "low", "low": "low", "medium": "medium", "high": "high", "xhigh": "high"}

// xaiNoEffort is @ai-sdk/xai's modelsWithoutReasoningEffort.
var xaiNoEffort = regexp.MustCompile(`^grok-4\.20(-\d{4})?-(non-)?reasoning$`)

// buildXAIRequest is @ai-sdk/xai's responses getArgs.
func (m *languageModel) buildXAIRequest(opts provider.CallOptions) (map[string]any, []provider.Warning, error) {
	input, warnings, err := convertPrompt(opts.Prompt, "xai", false, true)
	if err != nil {
		return nil, nil, err
	}
	req := map[string]any{"model": m.modelID, "input": input, "stream": true}
	if len(opts.StopSequences) > 0 {
		warnings = append(warnings, provider.Unsupported("stopSequences", "xAI's Responses API has no stop sequences"))
	}
	if opts.MaxOutputTokens != nil {
		req["max_output_tokens"] = *opts.MaxOutputTokens
	}
	if opts.Temperature != nil {
		req["temperature"] = *opts.Temperature
	}
	if opts.TopP != nil {
		req["top_p"] = *opts.TopP
	}
	if opts.Seed != nil {
		req["seed"] = *opts.Seed
	}
	reasoning := map[string]any{}
	if effort := effortString(opts.Reasoning); effort != "" {
		if xaiNoEffort.MatchString(m.modelID) {
			warnings = append(warnings, provider.Unsupported("reasoning", fmt.Sprintf("reasoning %q is not supported by this model", effort)))
		} else {
			reasoning["effort"] = xaiEffort[effort]
		}
	}
	xo := opts.ProviderOptions.Get("xai")
	if v, ok := xo["reasoningSummary"]; ok {
		reasoning["summary"] = v
	}
	if len(reasoning) > 0 {
		req["reasoning"] = reasoning
	}
	if store, ok := xo["store"].(bool); ok && !store {
		req["store"] = false
		req["include"] = []string{"reasoning.encrypted_content"}
	}
	if err := applyTools(req, opts); err != nil {
		return nil, nil, err
	}
	applyResponseFormat(req, opts, true)
	return req, warnings, nil
}

func applyTools(req map[string]any, opts provider.CallOptions) error {
	tools, err := convertTools(opts.Tools)
	if err != nil {
		return err
	}
	if len(tools) > 0 {
		req["tools"] = tools
	}
	if tc := opts.ToolChoice; tc != nil && len(tools) > 0 {
		switch tc.Type {
		case provider.ToolChoiceAuto, provider.ToolChoiceNone, provider.ToolChoiceRequired:
			req["tool_choice"] = string(tc.Type)
		case provider.ToolChoiceTool:
			req["tool_choice"] = map[string]any{"type": "function", "name": tc.ToolName}
		}
	}
	return nil
}

// applyResponseFormat asks for JSON; xAI's SDK makes the schema strict.
func applyResponseFormat(req map[string]any, opts provider.CallOptions, strict bool) {
	if rf := opts.ResponseFormat; rf != nil && rf.Type == "json" {
		format := map[string]any{"type": "json_object"}
		if rf.Schema != nil {
			name := rf.Name
			if name == "" {
				name = "response"
			}
			format = map[string]any{"type": "json_schema", "name": name, "schema": rf.Schema, "strict": strict}
			if rf.Description != "" {
				format["description"] = rf.Description
			}
		}
		req["text"] = map[string]any{"format": format}
	}
}

func convertTools(tools []provider.Tool) ([]any, error) {
	var out []any
	for _, tool := range tools {
		t, ok := tool.(provider.FunctionTool)
		if !ok {
			continue
		}
		params := map[string]any{"type": "object", "properties": map[string]any{}}
		if t.InputSchema != nil {
			raw, err := json.Marshal(t.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("openairesponses: encoding schema for tool %q: %w", t.Name, err)
			}
			params = map[string]any{}
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, err
			}
			delete(params, "$schema")
		}
		fn := map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": params}
		if t.Strict {
			fn["strict"] = true
		}
		out = append(out, fn)
	}
	return out, nil
}

// DoStream implements provider.LanguageModel.
func (m *languageModel) DoStream(ctx context.Context, opts provider.CallOptions) (*provider.StreamResult, error) {
	req, warnings, err := m.buildRequest(opts)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := providerutil.Retry(ctx, m.provider.retry, func(ctx context.Context) (io.ReadCloser, error) {
		httpResp, err := m.provider.client.PostJSON(ctx, responsesPath, json.RawMessage(body), opts.Headers)
		if err != nil {
			return nil, err
		}
		return httpResp.Body, nil
	})
	if err != nil {
		return nil, err
	}
	out := make(chan provider.StreamPart, 64)
	go func() {
		defer close(out)
		s := newStreamState(ctx, out, opts.IncludeRawChunks, m.provider.client.BaseURL+responsesPath, m.provider.key, m.provider.xai)
		s.emit(provider.StreamStart{Warnings: warnings})
		s.run(resp)
	}()
	return &provider.StreamResult{Stream: out, Request: &provider.RequestInfo{Body: string(body)}}, nil
}

// DoGenerate is served by streaming and folding the parts, as loop serves
// generate calls on routes where streaming is mandatory.
func (m *languageModel) DoGenerate(ctx context.Context, opts provider.CallOptions) (*provider.GenerateResult, error) {
	res, err := m.DoStream(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := &provider.GenerateResult{Request: res.Request, Response: &provider.ResponseInfo{}}
	var (
		text, reasoning strings.Builder
		reasoningMeta   provider.ProviderMetadata
		textMeta        provider.ProviderMetadata
		streamErr       error
	)
	flushReasoning := func() {
		if reasoning.Len() > 0 || reasoningMeta != nil {
			out.Content = append(out.Content, provider.Reasoning{Text: reasoning.String(), ProviderMetadata: reasoningMeta})
			reasoning.Reset()
			reasoningMeta = nil
		}
	}
	for part := range res.Stream {
		switch p := part.(type) {
		case provider.StreamStart:
			out.Warnings = p.Warnings
		case provider.ResponseMetadataPart:
			out.Response.ResponseMetadata = p.ResponseMetadata
		case provider.ReasoningDelta:
			reasoning.WriteString(p.Delta)
		case provider.ReasoningEnd:
			reasoningMeta = p.ProviderMetadata
			flushReasoning()
		case provider.TextDelta:
			text.WriteString(p.Delta)
		case provider.TextEnd:
			textMeta = p.ProviderMetadata
			out.Content = append(out.Content, provider.Text{Text: text.String(), ProviderMetadata: textMeta})
			text.Reset()
		case provider.ToolCall:
			out.Content = append(out.Content, p)
		case provider.Finish:
			out.Usage, out.FinishReason = p.Usage, p.FinishReason
		case provider.ErrorPart:
			if streamErr == nil {
				streamErr = p.Err
			}
		}
	}
	flushReasoning()
	if text.Len() > 0 {
		out.Content = append(out.Content, provider.Text{Text: text.String()})
	}
	if streamErr != nil {
		return nil, streamErr
	}
	return out, nil
}
