package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// languageModel is the Anthropic implementation of provider.LanguageModel.
type languageModel struct {
	modelID  string
	client   *providerutil.Client
	caps     capabilities
	provider *Provider
}

// SpecificationVersion implements provider.LanguageModel.
func (m *languageModel) SpecificationVersion() string { return provider.SpecificationVersion }

// Provider implements provider.LanguageModel.
func (m *languageModel) Provider() string { return m.provider.name }

// ModelID implements provider.LanguageModel.
func (m *languageModel) ModelID() string { return m.modelID }

// anthropicFetchableURL matches the URLs Anthropic fetches server-side, so
// core does not download and re-upload them.
var anthropicFetchableURL = regexp.MustCompile(`^https?://.*`)

// SupportedURLs implements provider.LanguageModel.
func (m *languageModel) SupportedURLs(context.Context) (map[string][]*regexp.Regexp, error) {
	return map[string][]*regexp.Regexp{
		"image/*":         {anthropicFetchableURL},
		"application/pdf": {anthropicFetchableURL},
	}, nil
}

// builtRequest is a request body plus what has to travel outside it.
type builtRequest struct {
	body *messagesRequest
	// betas are the anthropic-beta values the requested features need. They
	// belong in a header rather than the body, which is why buildRequest
	// cannot just return the body.
	betas    []string
	warnings []provider.Warning
}

// headers merges the beta flags the request needs into the caller's headers.
//
// A caller that set anthropic-beta itself wins: they may be enabling something
// this package does not know about, and overwriting it would break the call.
func (b *builtRequest) headers(callHeaders provider.Headers) provider.Headers {
	if len(b.betas) == 0 {
		return callHeaders
	}
	if _, ok := callHeaders["anthropic-beta"]; ok {
		return callHeaders
	}

	merged := make(provider.Headers, len(callHeaders)+1)
	for k, v := range callHeaders {
		merged[k] = v
	}
	merged["anthropic-beta"] = strings.Join(b.betas, ",")
	return merged
}

// buildRequest renders call options into an Anthropic request.
func (m *languageModel) buildRequest(opts provider.CallOptions, stream bool) (*builtRequest, error) {
	converted, err := convertPrompt(opts.Prompt)
	if err != nil {
		return nil, err
	}
	warnings := converted.Warnings

	maxTokens := m.caps.MaxOutputTokens
	if opts.MaxOutputTokens != nil {
		maxTokens = *opts.MaxOutputTokens
	}

	req := &messagesRequest{
		Model:         m.modelID,
		MaxTokens:     maxTokens,
		System:        converted.System,
		Messages:      converted.Messages,
		StopSequences: opts.StopSequences,
		Stream:        stream,
	}

	// Anthropic requires a non-empty message list even when the caller only
	// supplied a system prompt.
	if len(req.Messages) == 0 {
		return nil, &provider.InvalidPromptError{
			Message: "at least one user or assistant message is required",
		}
	}

	warnings = append(warnings, m.applySampling(req, opts)...)

	thinkingWarnings, err := m.applyThinking(req, opts)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, thinkingWarnings...)

	toolWarnings, betas, err := m.applyTools(req, opts)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, toolWarnings...)

	warnings = append(warnings, m.applyResponseFormat(req, opts)...)

	if opts.Seed != nil {
		warnings = append(warnings, provider.Unsupported("seed", "anthropic does not support seeded sampling"))
	}
	if opts.PresencePenalty != nil {
		warnings = append(warnings, provider.Unsupported("presencePenalty", ""))
	}
	if opts.FrequencyPenalty != nil {
		warnings = append(warnings, provider.Unsupported("frequencyPenalty", ""))
	}

	return &builtRequest{body: req, betas: betas, warnings: warnings}, nil
}

// applySampling copies temperature, top_p and top_k, warning when the model
// rejects them outright.
func (m *languageModel) applySampling(req *messagesRequest, opts provider.CallOptions) []provider.Warning {
	var warnings []provider.Warning

	if m.caps.RejectsSamplingParameters {
		// These models return 400 if the parameters are present at all, so
		// they are dropped rather than forwarded.
		for name, set := range map[string]bool{
			"temperature": opts.Temperature != nil,
			"topP":        opts.TopP != nil,
			"topK":        opts.TopK != nil,
		} {
			if set {
				warnings = append(warnings, provider.Unsupported(
					name,
					fmt.Sprintf("%s does not accept sampling parameters; the setting was dropped", m.modelID),
				))
			}
		}
		return warnings
	}

	req.Temperature = opts.Temperature
	req.TopP = opts.TopP
	req.TopK = opts.TopK
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

// applyThinking translates the unified reasoning effort into whichever
// thinking shape the model accepts, then lets explicit provider options
// override the result.
func (m *languageModel) applyThinking(req *messagesRequest, opts provider.CallOptions) ([]provider.Warning, error) {
	var warnings []provider.Warning

	if effort := opts.Reasoning; effort != "" && effort != provider.ReasoningDefault {
		switch {
		case effort == provider.ReasoningNone:
			// "disabled" must be sent explicitly: some models default
			// thinking on, so omitting it would silently keep it enabled and
			// spend the output budget on it.
			req.Thinking = &thinkingConfig{Type: "disabled"}

		case m.caps.SupportsAdaptiveThinking:
			req.Thinking = &thinkingConfig{Type: "adaptive", Display: "summarized"}
			req.OutputConfig = &outputConfig{Effort: m.adaptiveEffort(effort)}

		default:
			fraction, ok := reasoningBudgetFraction[effort]
			if !ok {
				warnings = append(warnings, provider.Unsupported(
					"reasoning",
					fmt.Sprintf("reasoning level %q is not supported by %s", effort, m.modelID),
				))
				break
			}
			budget := int64(math.Round(float64(req.MaxTokens) * fraction))
			budget = min(max(budget, minReasoningBudget), req.MaxTokens)
			req.Thinking = &thinkingConfig{Type: "enabled", BudgetTokens: &budget}
		}
	}

	if err := m.applyThinkingOverrides(req, opts); err != nil {
		return nil, err
	}

	// Newer models reject a disabled-thinking request whose effort is above
	// "high", so lower the effort rather than fail the call.
	if m.caps.RejectsThinkingDisabledAboveHighEffort &&
		req.Thinking != nil && req.Thinking.Type == "disabled" &&
		req.OutputConfig != nil &&
		(req.OutputConfig.Effort == "xhigh" || req.OutputConfig.Effort == "max") {
		warnings = append(warnings, provider.Unsupported(
			"providerOptions.anthropic.effort",
			fmt.Sprintf(
				"%s rejects effort %q while thinking is disabled; it was lowered to \"high\"",
				m.modelID, req.OutputConfig.Effort,
			),
		))
		req.OutputConfig.Effort = "high"
	}

	// Anthropic requires the thinking budget to leave room for an answer.
	if req.Thinking != nil && req.Thinking.BudgetTokens != nil && *req.Thinking.BudgetTokens >= req.MaxTokens {
		req.MaxTokens = *req.Thinking.BudgetTokens + minReasoningBudget
	}

	return warnings, nil
}

// adaptiveEffort maps a unified effort level onto the strings an
// adaptive-thinking model accepts.
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

// applyThinkingOverrides lets providerOptions.anthropic set the thinking
// config directly, which is the escape hatch for models whose behaviour this
// build predates.
func (m *languageModel) applyThinkingOverrides(req *messagesRequest, opts provider.CallOptions) error {
	block := opts.ProviderOptions.Get(providerID)
	if block == nil {
		return nil
	}

	if raw, ok := block["thinking"]; ok {
		cfg, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("anthropic: providerOptions.thinking must be an object, got %T", raw)
		}
		thinking := &thinkingConfig{Type: "enabled"}
		if t, ok := cfg["type"].(string); ok && t != "" {
			thinking.Type = t
		}
		if d, ok := cfg["display"].(string); ok {
			thinking.Display = d
		}
		if b, ok := toInt64(cfg["budgetTokens"]); ok {
			thinking.BudgetTokens = &b
		} else if b, ok := toInt64(cfg["budget_tokens"]); ok {
			thinking.BudgetTokens = &b
		}
		req.Thinking = thinking
	}

	if effort, ok := block["effort"].(string); ok && effort != "" {
		if req.OutputConfig == nil {
			req.OutputConfig = &outputConfig{}
		}
		req.OutputConfig.Effort = effort
	}

	return nil
}

// applyTools renders the tool list and tool choice, and reports the beta flags
// the chosen tools require.
func (m *languageModel) applyTools(req *messagesRequest, opts provider.CallOptions) ([]provider.Warning, []string, error) {
	var warnings []provider.Warning
	var betas []string

	for _, tool := range opts.Tools {
		switch t := tool.(type) {
		case provider.FunctionTool:
			schema, err := schemaForTool(t)
			if err != nil {
				return nil, nil, err
			}
			req.Tools = append(req.Tools, apiTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: schema,
			})

		case provider.ProviderTool:
			spec, ok := hostedTools[t.ID]
			if !ok {
				// An unknown id would be sent as a tool the API rejects, so
				// drop it and say which ids exist.
				warnings = append(warnings, provider.Unsupported(
					"provider-executed tools",
					fmt.Sprintf("tool %q was dropped: %q is not a known anthropic-hosted tool", t.Name, t.ID),
				))
				continue
			}

			hosted := apiTool{Type: spec.wireType, Name: spec.name}
			if spec.build != nil {
				spec.build(&hosted, t.Args)
			}
			req.Tools = append(req.Tools, hosted)

			if spec.beta != "" {
				betas = append(betas, spec.beta)
			}

		default:
			return nil, nil, fmt.Errorf("anthropic: unsupported tool type %T", tool)
		}
	}

	if opts.ToolChoice == nil {
		return warnings, betas, nil
	}

	switch opts.ToolChoice.Type {
	case provider.ToolChoiceAuto:
		req.ToolChoice = &toolChoice{Type: "auto"}
	case provider.ToolChoiceNone:
		// Anthropic has no "none": withholding the tools is the equivalent,
		// and keeps the model from calling something it was told not to.
		req.Tools = nil
	case provider.ToolChoiceRequired:
		req.ToolChoice = &toolChoice{Type: "any"}
	case provider.ToolChoiceTool:
		req.ToolChoice = &toolChoice{Type: "tool", Name: opts.ToolChoice.ToolName}
	}

	return warnings, betas, nil
}

// emptyObjectSchema is what a tool with no arguments must send: Anthropic
// requires input_schema to be an object schema even when nothing is expected.
var emptyObjectSchema = map[string]any{
	"type":       "object",
	"properties": map[string]any{},
}

// schemaForTool renders a tool's input schema for the wire.
func schemaForTool(t provider.FunctionTool) (any, error) {
	if t.InputSchema == nil {
		return emptyObjectSchema, nil
	}
	// Round-trip through JSON so the wire body carries a plain document
	// rather than the Go schema type's field layout.
	raw, err := json.Marshal(t.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encoding schema for tool %q: %w", t.Name, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("anthropic: decoding schema for tool %q: %w", t.Name, err)
	}
	// $schema is meaningful to validators but not to the API, and some
	// endpoints reject unknown top-level keys.
	delete(out, "$schema")
	return out, nil
}

// jsonToolName is the tool injected to emulate structured output. The
// Messages API has no response-format field, so the schema is presented as the
// only callable tool and the model's arguments are the object.
const jsonToolName = "json"

// usesJSONTool reports whether a call is emulating structured output with the
// forced tool, which is also what tells the response and stream paths to read
// the tool's arguments as the response text.
//
// The workaround needs the tool slot to itself: the model can only be forced
// to call one tool, so a caller's own tools would become unreachable.
func usesJSONTool(opts provider.CallOptions) bool {
	return opts.ResponseFormat != nil &&
		opts.ResponseFormat.Type == "json" &&
		opts.ResponseFormat.Schema != nil &&
		len(opts.Tools) == 0
}

// applyResponseFormat handles JSON output requests.
func (m *languageModel) applyResponseFormat(req *messagesRequest, opts provider.CallOptions) []provider.Warning {
	if opts.ResponseFormat == nil || opts.ResponseFormat.Type != "json" {
		return nil
	}

	if opts.ResponseFormat.Schema == nil {
		// Without a schema there is nothing to force the model into: unlike
		// the chat-completions API, Anthropic has no schema-less JSON mode.
		return []provider.Warning{provider.Unsupported(
			"responseFormat json",
			"anthropic has no schema-less JSON mode; ask for JSON in the prompt instead",
		)}
	}

	if len(opts.Tools) > 0 {
		return []provider.Warning{provider.Unsupported(
			"responseFormat json",
			"structured output cannot be combined with tools on anthropic; the response format was ignored",
		)}
	}

	schema, err := schemaForTool(provider.FunctionTool{
		Name:        jsonToolName,
		InputSchema: opts.ResponseFormat.Schema,
	})
	if err != nil {
		return []provider.Warning{provider.Unsupported("responseFormat json", err.Error())}
	}

	description := opts.ResponseFormat.Description
	if description == "" {
		description = "Respond with a JSON object matching the schema."
	}

	req.Tools = []apiTool{{
		Name:        jsonToolName,
		Description: description,
		InputSchema: schema,
	}}
	req.ToolChoice = &toolChoice{Type: "tool", Name: jsonToolName}

	return nil
}

// DoGenerate implements provider.LanguageModel.
func (m *languageModel) DoGenerate(ctx context.Context, opts provider.CallOptions) (*provider.GenerateResult, error) {
	built, err := m.buildRequest(opts, false)
	if err != nil {
		return nil, err
	}
	req, warnings := built.body, built.warnings
	headers := built.headers(opts.Headers)

	resp, err := providerutil.Retry(ctx, m.provider.retry, func(ctx context.Context) (*messagesResponse, error) {
		httpResp, err := m.client.PostJSON(ctx, "/v1/messages", req, headers)
		if err != nil {
			return nil, err
		}
		var decoded messagesResponse
		if err := providerutil.DecodeJSON(httpResp, &decoded); err != nil {
			return nil, err
		}
		return &decoded, nil
	})
	if err != nil {
		return nil, err
	}

	content, err := m.convertContent(resp.Content)
	if err != nil {
		return nil, err
	}
	if usesJSONTool(opts) {
		content = jsonToolAsText(content)
	}

	requestBody, _ := json.Marshal(req)

	return &provider.GenerateResult{
		Content:      content,
		FinishReason: mapStopReason(resp.StopReason),
		Usage:        convertUsage(resp.Usage),
		Warnings:     warnings,
		Request:      &provider.RequestInfo{Body: string(requestBody)},
		Response: &provider.ResponseInfo{
			ResponseMetadata: provider.ResponseMetadata{ID: resp.ID, ModelID: resp.Model},
		},
	}, nil
}

// jsonToolAsText rewrites the forced structured-output call into text, so that
// a caller asking for JSON reads it the same way as from any other provider
// and never learns a tool was involved.
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

// convertContent maps response blocks onto spec content.
func (m *languageModel) convertContent(blocks []apiBlock) ([]provider.Content, error) {
	out := make([]provider.Content, 0, len(blocks))

	for _, block := range blocks {
		switch block.Type {
		case "text":
			out = append(out, provider.Text{Text: block.Text})

		case "thinking":
			// The signature has to survive the round trip or the reasoning
			// cannot be replayed in a later turn.
			out = append(out, provider.Reasoning{
				Text: deref(block.Thinking),
				ProviderMetadata: provider.ProviderMetadata{
					providerID: {"signature": block.Signature},
				},
			})

		case "redacted_thinking":
			out = append(out, provider.Reasoning{
				Text: "",
				ProviderMetadata: provider.ProviderMetadata{
					providerID: {"redactedData": block.Data},
				},
			})

		case "tool_use":
			out = append(out, provider.ToolCall{
				ToolCallID: block.ID,
				ToolName:   block.Name,
				Input:      string(block.Input),
			})

		case "server_tool_use":
			call, ok := hostedToolCall(block)
			if !ok {
				out = append(out, unknownBlock(block))
				continue
			}
			out = append(out, call)

		default:
			if results := hostedResultBlocks(block, newSourceID); results != nil {
				out = append(out, results...)
				continue
			}
			// Unknown block types are surfaced rather than dropped, so a new
			// server-side feature is visible instead of silently missing.
			out = append(out, unknownBlock(block))
		}
	}
	return out, nil
}

// mapStopReason converts Anthropic's stop_reason to the unified finish reason.
func mapStopReason(raw *string) provider.FinishReason {
	if raw == nil {
		return provider.FinishReason{Unified: provider.FinishOther}
	}

	unified := provider.FinishOther
	switch *raw {
	case "end_turn", "stop_sequence", "pause_turn":
		unified = provider.FinishStop
	case "tool_use":
		unified = provider.FinishToolCalls
	case "max_tokens", "model_context_window_exceeded":
		unified = provider.FinishLength
	case "refusal":
		unified = provider.FinishContentFilter
	}

	return provider.FinishReason{Unified: unified, Raw: *raw}
}

// convertUsage maps Anthropic usage onto the spec's breakdown.
//
// Anthropic's input_tokens excludes cached tokens, so the spec's total is the
// sum of fresh, cache-read and cache-write tokens. Getting this wrong
// understates the prompt size on every cached request.
func convertUsage(u anthropicUsage) provider.Usage {
	cacheWrite := deref(u.CacheCreationInputTokens)
	cacheRead := deref(u.CacheReadInputTokens)

	inputTokens, outputTokens := executorTotals(u)
	total := inputTokens + cacheWrite + cacheRead

	usage := provider.Usage{
		InputTokens: provider.InputTokens{
			Total:      &total,
			NoCache:    &inputTokens,
			CacheRead:  &cacheRead,
			CacheWrite: &cacheWrite,
		},
		OutputTokens: provider.OutputTokens{Total: &outputTokens},
	}

	if u.OutputTokensDetails != nil && u.OutputTokensDetails.ThinkingTokens != nil {
		reasoning := *u.OutputTokensDetails.ThinkingTokens
		text := outputTokens - reasoning
		usage.OutputTokens.Reasoning = &reasoning
		usage.OutputTokens.Text = &text
	}

	if raw, err := json.Marshal(u); err == nil {
		var obj provider.JSONObject
		if json.Unmarshal(raw, &obj) == nil {
			usage.Raw = obj
		}
	}

	return usage
}

// executorTotals resolves the input and output token counts.
//
// When the server ran several sampling passes it reports them in iterations,
// and the top-level counts exclude the compaction passes. Advisor passes are
// excluded here too: they bill at a different model's rates, so folding them
// into the totals would misattribute cost. A turn served by a server-side
// fallback is the exception, since the top-level counts already describe the
// answer that was actually returned.
func executorTotals(u anthropicUsage) (input, output int64) {
	if len(u.Iterations) == 0 {
		return u.InputTokens, u.OutputTokens
	}

	for _, it := range u.Iterations {
		if it.Type == "fallback_message" {
			return u.InputTokens, u.OutputTokens
		}
	}

	var found bool
	for _, it := range u.Iterations {
		if it.Type == "message" || it.Type == "compaction" {
			input += it.InputTokens
			output += it.OutputTokens
			found = true
		}
	}
	if !found {
		return u.InputTokens, u.OutputTokens
	}
	return input, output
}

// deref returns the pointed-to value or zero.
func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// toInt64 coerces a JSON number, which decodes as float64, to an int64.
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	default:
		return 0, false
	}
}
