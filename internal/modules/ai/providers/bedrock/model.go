package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// languageModel is the Bedrock implementation of provider.LanguageModel.
type languageModel struct {
	modelID  string
	provider *Provider
	caps     capabilities
}

// SpecificationVersion implements provider.LanguageModel.
func (m *languageModel) SpecificationVersion() string { return provider.SpecificationVersion }

// Provider implements provider.LanguageModel.
func (m *languageModel) Provider() string { return providerID }

// ModelID implements provider.LanguageModel.
func (m *languageModel) ModelID() string { return m.modelID }

// s3URL matches the URLs Bedrock fetches server-side.
var s3URL = regexp.MustCompile(`^s3://`)

// SupportedURLs implements provider.LanguageModel.
func (m *languageModel) SupportedURLs(context.Context) (map[string][]*regexp.Regexp, error) {
	return map[string][]*regexp.Regexp{
		"image/*": {s3URL},
		"video/*": {s3URL},
	}, nil
}

// capabilities records the model traits that change the request shape.
type capabilities struct {
	IsAnthropic bool
	IsOpenAI    bool
	IsMistral   bool

	MaxOutputTokens           int64
	SupportsStructuredOutput  bool
	SupportsAdaptiveThinking  bool
	RejectsSamplingParameters bool
	SupportsXHighEffort       bool
	// RejectsNewerSchemaFields reports Claude models whose Bedrock schema
	// copy 400s on output_config.format and tool strict.
	RejectsNewerSchemaFields bool
}

// modelsRejectingNewerSchemaFields is Bedrock's copy of the Messages schema,
// which lags Anthropic's own API for the newest Claude ids.
var modelsRejectingNewerSchemaFields = []string{
	"claude-opus-4-7",
	"claude-opus-4-8",
	"claude-opus-5",
	"claude-fable-5",
	"claude-sonnet-5",
}

// modelCapabilities classifies a model id. Matching is by substring so that
// dated ids, aliases and inference-profile prefixes
// ("us.anthropic.claude-sonnet-4-5-20250929-v1:0") all resolve the same way.
func modelCapabilities(modelID string) capabilities {
	id := strings.ToLower(modelID)
	has := func(s string) bool { return strings.Contains(id, s) }

	caps := capabilities{
		IsAnthropic:     has("anthropic."),
		IsOpenAI:        has("openai."),
		IsMistral:       has("mistral."),
		MaxOutputTokens: 4096,
	}

	for _, name := range modelsRejectingNewerSchemaFields {
		if has(name) {
			caps.RejectsNewerSchemaFields = true
			break
		}
	}

	if !caps.IsAnthropic {
		return caps
	}

	switch {
	case has("claude-opus-5"):
		caps.MaxOutputTokens = 128000
		caps.SupportsStructuredOutput = true
		caps.SupportsAdaptiveThinking = true
		caps.RejectsSamplingParameters = true
		caps.SupportsXHighEffort = true

	case has("claude-opus-4-8"), has("claude-opus-4-7"),
		has("claude-fable-5"), has("claude-sonnet-5"):
		caps.MaxOutputTokens = 128000
		caps.SupportsStructuredOutput = true
		caps.SupportsAdaptiveThinking = true
		caps.RejectsSamplingParameters = true
		caps.SupportsXHighEffort = true

	case has("claude-sonnet-4-6"), has("claude-opus-4-6"):
		caps.MaxOutputTokens = 128000
		caps.SupportsStructuredOutput = true
		caps.SupportsAdaptiveThinking = true

	case has("claude-sonnet-4-5"), has("claude-opus-4-5"), has("claude-haiku-4-5"):
		caps.MaxOutputTokens = 64000
		caps.SupportsStructuredOutput = true

	case has("claude-opus-4-1"):
		caps.MaxOutputTokens = 32000
		caps.SupportsStructuredOutput = true

	case has("claude-sonnet-4-"):
		caps.MaxOutputTokens = 64000

	case has("claude-opus-4-"):
		caps.MaxOutputTokens = 32000

	default:
		// An unrecognised Claude id is assumed recent enough to think, but
		// without claiming features whose absence would produce a 400.
		caps.MaxOutputTokens = 64000
	}

	if caps.RejectsNewerSchemaFields {
		caps.SupportsStructuredOutput = false
	}
	return caps
}

// builtRequest is a Converse body plus the warnings raised while building it.
type builtRequest struct {
	body     *converseRequest
	warnings []provider.Warning
	// jsonTool is set when structured output is emulated with a forced tool,
	// so the response path can rewrite that tool's arguments as text.
	jsonTool bool
}

// buildRequest renders call options into a Converse request.
func (m *languageModel) buildRequest(opts provider.CallOptions) (*builtRequest, error) {
	converted, err := convertPrompt(opts.Prompt, m.caps.IsMistral)
	if err != nil {
		return nil, err
	}
	warnings := converted.Warnings

	req := &converseRequest{
		Messages: converted.Messages,
		System:   converted.System,
	}

	if m.caps.IsAnthropic {
		req.AdditionalModelResponseFieldPaths = []string{"/delta/stop_sequence"}
	}

	warnings = append(warnings, m.applySampling(req, opts)...)
	warnings = append(warnings, m.applyThinking(req, opts)...)
	warnings = append(warnings, m.applyTools(req, opts)...)
	jsonTool, formatWarnings := m.applyResponseFormat(req, opts)
	warnings = append(warnings, formatWarnings...)
	warnings = append(warnings, m.applyProviderOptions(req, opts)...)

	if len(req.Messages) == 0 {
		return nil, &provider.InvalidPromptError{
			Message: "bedrock converse requires at least one user or assistant message",
		}
	}

	return &builtRequest{body: req, warnings: warnings, jsonTool: jsonTool}, nil
}

// applySampling writes the sampling settings Converse understands and warns
// about the ones it does not.
func (m *languageModel) applySampling(req *converseRequest, opts provider.CallOptions) []provider.Warning {
	var warnings []provider.Warning

	if opts.FrequencyPenalty != nil {
		warnings = append(warnings, provider.Unsupported("frequencyPenalty", "bedrock converse has no frequency penalty"))
	}
	if opts.PresencePenalty != nil {
		warnings = append(warnings, provider.Unsupported("presencePenalty", "bedrock converse has no presence penalty"))
	}
	if opts.Seed != nil {
		warnings = append(warnings, provider.Unsupported("seed", "bedrock converse has no seed"))
	}

	temperature := opts.Temperature
	if temperature != nil {
		switch {
		case *temperature > 1:
			warnings = append(warnings, provider.Unsupported(
				"temperature",
				fmt.Sprintf("%v exceeds bedrock maximum of 1.0; clamped to 1.0", *temperature),
			))
			temperature = ptr(1.0)
		case *temperature < 0:
			warnings = append(warnings, provider.Unsupported(
				"temperature",
				fmt.Sprintf("%v is below bedrock minimum of 0; clamped to 0", *temperature),
			))
			temperature = ptr(0.0)
		}
	}

	if m.caps.RejectsSamplingParameters && (temperature != nil || opts.TopP != nil || opts.TopK != nil) {
		warnings = append(warnings, provider.Unsupported(
			"sampling",
			fmt.Sprintf("%s rejects temperature, topP and topK; they were dropped", m.modelID),
		))
		temperature = nil
		opts.TopP = nil
		opts.TopK = nil
	}

	if opts.MaxOutputTokens == nil && temperature == nil && opts.TopP == nil && opts.TopK == nil && len(opts.StopSequences) == 0 {
		return warnings
	}

	req.InferenceConfig = &inferenceConfig{
		MaxTokens:     opts.MaxOutputTokens,
		Temperature:   temperature,
		TopP:          opts.TopP,
		TopK:          opts.TopK,
		StopSequences: opts.StopSequences,
	}
	return warnings
}

// reasoningBudgetFraction is the share of the output budget given to thinking
// at each effort level, for models that take an explicit token budget.
var reasoningBudgetFraction = map[provider.ReasoningEffort]float64{
	provider.ReasoningMinimal: 0.02,
	provider.ReasoningLow:     0.10,
	provider.ReasoningMedium:  0.30,
	provider.ReasoningHigh:    0.60,
	provider.ReasoningXHigh:   0.90,
}

// minReasoningBudget is Anthropic's floor for an explicit thinking budget.
const minReasoningBudget = 1024

// defaultAnswerTokens is added to a thinking budget when the caller set no
// max tokens, so the model still has room to answer.
const defaultAnswerTokens = 4096

// applyThinking translates the unified reasoning effort into Converse's
// additionalModelRequestFields, which is where every family puts thinking
// because Converse has no field for it.
func (m *languageModel) applyThinking(req *converseRequest, opts provider.CallOptions) []provider.Warning {
	var warnings []provider.Warning
	extra := req.AdditionalModelRequestFields
	if extra == nil {
		extra = map[string]any{}
	}

	effort := opts.Reasoning
	if effort == "" {
		effort = provider.ReasoningDefault
	}

	if effort != provider.ReasoningDefault {
		switch {
		case m.caps.IsAnthropic:
			warnings = append(warnings, m.applyAnthropicThinking(req, extra, effort)...)

		case m.caps.IsOpenAI:
			if effort != provider.ReasoningNone {
				extra["reasoning_effort"] = m.adaptiveEffort(effort)
			}

		default:
			if effort != provider.ReasoningNone {
				extra["reasoningConfig"] = map[string]any{
					"type":               "enabled",
					"maxReasoningEffort": m.adaptiveEffort(effort),
				}
			}
		}
	}

	if thinkingEnabled(extra) {
		if req.InferenceConfig != nil && req.InferenceConfig.Temperature != nil {
			req.InferenceConfig.Temperature = nil
			warnings = append(warnings, provider.Unsupported(
				"temperature", "temperature is not supported when thinking is enabled"))
		}
		if req.InferenceConfig != nil && req.InferenceConfig.TopP != nil {
			req.InferenceConfig.TopP = nil
			warnings = append(warnings, provider.Unsupported(
				"topP", "topP is not supported when thinking is enabled"))
		}
		if req.InferenceConfig != nil && req.InferenceConfig.TopK != nil {
			req.InferenceConfig.TopK = nil
			warnings = append(warnings, provider.Unsupported(
				"topK", "topK is not supported when thinking is enabled"))
		}
	}

	if len(extra) > 0 {
		req.AdditionalModelRequestFields = extra
	}
	return warnings
}

// applyAnthropicThinking writes the thinking shape Claude on Bedrock accepts.
func (m *languageModel) applyAnthropicThinking(req *converseRequest, extra map[string]any, effort provider.ReasoningEffort) []provider.Warning {
	if effort == provider.ReasoningNone {
		extra["thinking"] = map[string]any{"type": "disabled"}
		return nil
	}

	if m.caps.SupportsAdaptiveThinking {
		extra["thinking"] = map[string]any{"type": "adaptive", "display": "summarized"}
		mergeOutputConfig(extra, map[string]any{"effort": m.adaptiveEffort(effort)})
		return nil
	}

	fraction, ok := reasoningBudgetFraction[effort]
	if !ok {
		return []provider.Warning{provider.Unsupported(
			"reasoning",
			fmt.Sprintf("reasoning level %q is not supported by %s", effort, m.modelID),
		)}
	}

	maxTokens := m.caps.MaxOutputTokens
	if req.InferenceConfig != nil && req.InferenceConfig.MaxTokens != nil {
		maxTokens = *req.InferenceConfig.MaxTokens
	}
	budget := int64(math.Round(float64(maxTokens) * fraction))
	budget = min(max(budget, minReasoningBudget), maxTokens)

	extra["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}

	// Anthropic requires the thinking budget to leave room for an answer.
	raised := budget + minReasoningBudget
	if req.InferenceConfig == nil {
		req.InferenceConfig = &inferenceConfig{MaxTokens: ptr(max(raised, defaultAnswerTokens))}
	} else if req.InferenceConfig.MaxTokens == nil {
		req.InferenceConfig.MaxTokens = ptr(max(raised, defaultAnswerTokens))
	} else if *req.InferenceConfig.MaxTokens <= budget {
		req.InferenceConfig.MaxTokens = ptr(raised)
	}
	return nil
}

// thinkingEnabled reports whether additional fields asked the model to think.
func thinkingEnabled(extra map[string]any) bool {
	raw, ok := extra["thinking"].(map[string]any)
	if !ok {
		return false
	}
	t, _ := raw["type"].(string)
	return t == "enabled" || t == "adaptive"
}

// adaptiveEffort maps a unified effort level onto the strings Bedrock accepts.
func (m *languageModel) adaptiveEffort(effort provider.ReasoningEffort) string {
	switch effort {
	case provider.ReasoningMinimal, provider.ReasoningLow:
		return "low"
	case provider.ReasoningMedium:
		return "medium"
	case provider.ReasoningHigh:
		return "high"
	case provider.ReasoningXHigh:
		if m.caps.SupportsXHighEffort {
			return "xhigh"
		}
		return "max"
	default:
		return "medium"
	}
}

// mergeOutputConfig writes fields into additionalModelRequestFields.output_config
// without clobbering a format or effort already set.
func mergeOutputConfig(extra map[string]any, fields map[string]any) {
	cfg, _ := extra["output_config"].(map[string]any)
	if cfg == nil {
		cfg = map[string]any{}
	}
	for k, v := range fields {
		cfg[k] = v
	}
	extra["output_config"] = cfg
}

// applyTools renders the tool list and tool choice.
func (m *languageModel) applyTools(req *converseRequest, opts provider.CallOptions) []provider.Warning {
	var (
		warnings []provider.Warning
		tools    []toolEntry
	)

	for _, tool := range opts.Tools {
		switch t := tool.(type) {
		case provider.FunctionTool:
			schema, err := schemaForTool(t)
			if err != nil {
				warnings = append(warnings, provider.Unsupported("tool "+t.Name, err.Error()))
				continue
			}
			spec := &toolSpec{
				Name:        sanitizeToolName(t.Name),
				Description: t.Description,
				InputSchema: toolSchema{JSON: schema},
			}
			tools = append(tools, toolEntry{ToolSpec: spec})

		case provider.ProviderTool:
			// Hosted tools ride on per-model APIs rather than Converse, and
			// a null or silently-dropped tool is worse than a warning: the
			// model answers ungrounded with nothing to say search never ran.
			warnings = append(warnings, provider.Unsupported(
				"provider tool "+t.ID,
				"bedrock converse has no hosted tools; the tool was dropped",
			))
		}
	}

	if len(tools) == 0 {
		return warnings
	}

	choice := opts.ToolChoice
	var spec *toolChoiceSpec
	if choice != nil {
		switch choice.Type {
		case provider.ToolChoiceAuto:
			spec = &toolChoiceSpec{Auto: &struct{}{}}
		case provider.ToolChoiceRequired:
			spec = &toolChoiceSpec{Any: &struct{}{}}
		case provider.ToolChoiceTool:
			spec = &toolChoiceSpec{Tool: &namedToolRef{Name: sanitizeToolName(choice.ToolName)}}
		case provider.ToolChoiceNone:
			// Forbidding tools means omitting them: Converse has no "none".
			return warnings
		}
	}

	req.ToolConfig = &toolConfig{Tools: tools, ToolChoice: spec}
	return warnings
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
		return nil, fmt.Errorf("bedrock: encoding schema for tool %q: %w", t.Name, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("bedrock: decoding schema for tool %q: %w", t.Name, err)
	}
	delete(out, "$schema")
	return out, nil
}

// jsonToolName is the tool injected to emulate structured output. Converse
// has no response-format field for most models, so the schema is presented
// as the only callable tool and the model's arguments are the object.
const jsonToolName = "json"

// applyResponseFormat handles JSON output requests.
func (m *languageModel) applyResponseFormat(req *converseRequest, opts provider.CallOptions) (bool, []provider.Warning) {
	if opts.ResponseFormat == nil || opts.ResponseFormat.Type == "text" {
		return false, nil
	}
	if opts.ResponseFormat.Type != "json" {
		return false, []provider.Warning{provider.Unsupported(
			"responseFormat",
			"only text and json response formats are supported",
		)}
	}

	if opts.ResponseFormat.Schema == nil {
		return false, []provider.Warning{provider.Unsupported(
			"responseFormat json",
			"bedrock has no schema-less JSON mode; ask for JSON in the prompt instead",
		)}
	}

	if m.caps.IsAnthropic && m.caps.SupportsStructuredOutput && !m.caps.RejectsNewerSchemaFields {
		schema, err := schemaForTool(provider.FunctionTool{
			Name:        jsonToolName,
			InputSchema: opts.ResponseFormat.Schema,
		})
		if err != nil {
			return false, []provider.Warning{provider.Unsupported("responseFormat json", err.Error())}
		}
		if req.AdditionalModelRequestFields == nil {
			req.AdditionalModelRequestFields = map[string]any{}
		}
		mergeOutputConfig(req.AdditionalModelRequestFields, map[string]any{
			"format": map[string]any{"type": "json_schema", "schema": schema},
		})
		return false, nil
	}

	if req.ToolConfig != nil && len(req.ToolConfig.Tools) > 0 {
		return false, []provider.Warning{provider.Unsupported(
			"responseFormat json",
			"structured output cannot be combined with tools on bedrock; the response format was ignored",
		)}
	}

	schema, err := schemaForTool(provider.FunctionTool{
		Name:        jsonToolName,
		InputSchema: opts.ResponseFormat.Schema,
	})
	if err != nil {
		return false, []provider.Warning{provider.Unsupported("responseFormat json", err.Error())}
	}

	description := opts.ResponseFormat.Description
	if description == "" {
		description = "Respond with a JSON object matching the schema."
	}

	req.ToolConfig = &toolConfig{
		Tools: []toolEntry{{ToolSpec: &toolSpec{
			Name:        jsonToolName,
			Description: description,
			InputSchema: toolSchema{JSON: schema},
		}}},
		ToolChoice: &toolChoiceSpec{Any: &struct{}{}},
	}
	return true, nil
}

// applyProviderOptions copies escape-hatch fields the caller set explicitly.
func (m *languageModel) applyProviderOptions(req *converseRequest, opts provider.CallOptions) []provider.Warning {
	block := optionBlock(opts.ProviderOptions)
	if block == nil {
		return nil
	}

	if raw, ok := block["additionalModelRequestFields"].(map[string]any); ok {
		if req.AdditionalModelRequestFields == nil {
			req.AdditionalModelRequestFields = map[string]any{}
		}
		for k, v := range raw {
			req.AdditionalModelRequestFields[k] = v
		}
	}

	if raw, ok := block["thinking"].(map[string]any); ok {
		if req.AdditionalModelRequestFields == nil {
			req.AdditionalModelRequestFields = map[string]any{}
		}
		req.AdditionalModelRequestFields["thinking"] = raw
	}

	if tier, ok := block["serviceTier"].(string); ok && tier != "" {
		req.ServiceTier = &serviceTier{Type: tier}
	}

	return nil
}

// DoGenerate implements provider.LanguageModel.
func (m *languageModel) DoGenerate(ctx context.Context, opts provider.CallOptions) (*provider.GenerateResult, error) {
	built, err := m.buildRequest(opts)
	if err != nil {
		return nil, err
	}

	path := modelPath(m.modelID, "converse")
	resp, err := providerutil.Retry(ctx, m.provider.retry, func(ctx context.Context) (*converseResponse, error) {
		httpResp, err := m.postJSON(ctx, path, built.body, opts.Headers)
		if err != nil {
			return nil, err
		}
		var decoded converseResponse
		if err := providerutil.DecodeJSON(httpResp, &decoded); err != nil {
			return nil, err
		}
		return &decoded, nil
	})
	if err != nil {
		return nil, err
	}

	var content []provider.Content
	if resp.Output.Message != nil {
		content = m.convertContent(resp.Output.Message.Content)
	}

	jsonFromTool := built.jsonTool && contentHasJSONTool(content)
	if jsonFromTool {
		content = jsonToolAsText(content)
	}

	requestBody, _ := json.Marshal(built.body)

	return &provider.GenerateResult{
		Content:      content,
		FinishReason: mapStopReason(resp.StopReason, jsonFromTool),
		Usage:        convertUsage(resp.Usage),
		Warnings:     built.warnings,
		Request:      &provider.RequestInfo{Body: string(requestBody)},
		Response: &provider.ResponseInfo{
			ResponseMetadata: provider.ResponseMetadata{ModelID: m.modelID},
		},
	}, nil
}

// postJSON signs and sends a JSON body.
func (m *languageModel) postJSON(ctx context.Context, path string, body any, extra provider.Headers) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("bedrock: encoding request body: %w", err)
	}
	return m.provider.post(ctx, path, payload, extra)
}

// convertContent maps response blocks onto spec content.
func (m *languageModel) convertContent(blocks []apiBlock) []provider.Content {
	out := make([]provider.Content, 0, len(blocks))

	for _, block := range blocks {
		switch {
		case block.Text != "":
			out = append(out, provider.Text{Text: block.Text})

		case block.ReasoningContent != nil:
			out = append(out, convertReasoning(*block.ReasoningContent))

		case block.ToolUse != nil:
			raw, _ := json.Marshal(block.ToolUse.Input)
			if len(raw) == 0 {
				raw = []byte("{}")
			}
			out = append(out, provider.ToolCall{
				ToolCallID: normalizeToolCallID(block.ToolUse.ToolUseID, m.caps.IsMistral),
				ToolName:   block.ToolUse.Name,
				Input:      string(raw),
			})
		}
	}
	return out
}

// convertReasoning maps a reasoning block, preserving the signature that
// has to survive a round trip.
func convertReasoning(block reasoningBlock) provider.Reasoning {
	if block.ReasoningText != nil {
		meta := provider.JSONObject{}
		if block.ReasoningText.Signature != "" {
			meta["signature"] = block.ReasoningText.Signature
		}
		return provider.Reasoning{
			Text:             block.ReasoningText.Text,
			ProviderMetadata: reasoningMeta(meta),
		}
	}
	return provider.Reasoning{
		Text: "",
		ProviderMetadata: reasoningMeta(provider.JSONObject{
			"redactedData": string(block.RedactedContent),
		}),
	}
}

// jsonToolAsText rewrites the forced structured-output call into text.
func jsonToolAsText(content []provider.Content) []provider.Content {
	out := make([]provider.Content, 0, len(content))
	for _, c := range content {
		call, ok := c.(provider.ToolCall)
		if !ok || call.ToolName != jsonToolName {
			out = append(out, c)
			continue
		}
		out = append(out, provider.Text{Text: call.Input})
	}
	return out
}

// contentHasJSONTool reports whether the model answered via the forced json tool.
func contentHasJSONTool(content []provider.Content) bool {
	for _, c := range content {
		if call, ok := c.(provider.ToolCall); ok && call.ToolName == jsonToolName {
			return true
		}
	}
	return false
}

// mapStopReason converts Converse's stopReason to the unified finish reason.
func mapStopReason(raw string, jsonFromTool bool) provider.FinishReason {
	unified := provider.FinishOther
	switch raw {
	case "end_turn", "stop_sequence":
		unified = provider.FinishStop
	case "max_tokens":
		unified = provider.FinishLength
	case "content_filtered", "guardrail_intervened":
		unified = provider.FinishContentFilter
	case "tool_use":
		if jsonFromTool {
			unified = provider.FinishStop
		} else {
			unified = provider.FinishToolCalls
		}
	}
	return provider.FinishReason{Unified: unified, Raw: raw}
}

// convertUsage maps Converse usage onto the spec's breakdown.
//
// InputTokens is the uncached count; cache-read and cache-write are reported
// separately and the spec total is the sum, matching the TypeScript SDK.
func convertUsage(u *apiUsage) provider.Usage {
	if u == nil {
		return provider.Usage{}
	}

	input := u.InputTokens
	output := u.OutputTokens
	cacheRead := int64(0)
	if u.CacheReadInputTokens != nil {
		cacheRead = *u.CacheReadInputTokens
	}
	cacheWrite := int64(0)
	if u.CacheWriteInputTokens != nil {
		cacheWrite = *u.CacheWriteInputTokens
	}
	total := input + cacheRead + cacheWrite

	usage := provider.Usage{
		InputTokens: provider.InputTokens{
			Total:      &total,
			NoCache:    &input,
			CacheRead:  &cacheRead,
			CacheWrite: &cacheWrite,
		},
		OutputTokens: provider.OutputTokens{Total: &output, Text: &output},
	}

	if raw, err := json.Marshal(u); err == nil {
		var obj provider.JSONObject
		if json.Unmarshal(raw, &obj) == nil {
			usage.Raw = obj
		}
	}
	return usage
}
