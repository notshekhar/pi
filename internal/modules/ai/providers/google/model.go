package google

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// languageModel implements provider.LanguageModel over the generateContent API.
type languageModel struct {
	modelID  string
	caps     capabilities
	provider *Provider
}

// SpecificationVersion implements provider.LanguageModel.
func (m *languageModel) SpecificationVersion() string { return provider.SpecificationVersion }

// Provider implements provider.LanguageModel.
func (m *languageModel) Provider() string { return m.provider.name }

// ModelID implements provider.LanguageModel.
func (m *languageModel) ModelID() string { return m.modelID }

// googleFetchableURL matches the URLs Google fetches server-side: its own
// file store and Cloud Storage.
var googleFetchableURL = []*regexp.Regexp{
	regexp.MustCompile(`^https://generativelanguage\.googleapis\.com/v1beta/files/.*`),
	regexp.MustCompile(`^gs://.*`),
}

// SupportedURLs implements provider.LanguageModel.
func (m *languageModel) SupportedURLs(context.Context) (map[string][]*regexp.Regexp, error) {
	return map[string][]*regexp.Regexp{"*/*": googleFetchableURL}, nil
}

// capabilities records the model traits that change the request shape.
type capabilities struct {
	// UsesGemini3Features selects thinkingLevel over an explicit thinking
	// budget. Google's ids are open-ended, so anything not recognised as an
	// older generation inherits the newest behaviour.
	UsesGemini3Features bool

	// SupportsGemini2Tools marks the hosted-tool shapes introduced with
	// Gemini 2: googleSearch, urlContext and codeExecution. Gemini 1 and the
	// original gemini-pro only have the older googleSearchRetrieval.
	SupportsGemini2Tools bool
}

var (
	geminiPattern     = regexp.MustCompile(`(?i)(^|/)gemini-`)
	gemini1Pattern    = regexp.MustCompile(`(?i)(^|/)gemini-1([.\-]|$)`)
	gemini2Pattern    = regexp.MustCompile(`(?i)(^|/)gemini-2([.\-]|$)`)
	geminiProPattern  = regexp.MustCompile(`(?i)(^|/)gemini-pro(-vision)?$`)
	roboticsERPattern = regexp.MustCompile(`(?i)(^|/)gemini-robotics-er-1\.5([.\-]|$)`)
)

// modelCapabilities classifies a model id.
func modelCapabilities(modelID string) capabilities {
	isGemini := geminiPattern.MatchString(modelID)
	isOlder := gemini1Pattern.MatchString(modelID) ||
		gemini2Pattern.MatchString(modelID) ||
		geminiProPattern.MatchString(modelID) ||
		roboticsERPattern.MatchString(modelID)

	// Only Gemini 1 and the original gemini-pro predate the modern tool
	// shapes; an unrecognised id is assumed current rather than ancient.
	preGemini2 := gemini1Pattern.MatchString(modelID) || geminiProPattern.MatchString(modelID)

	return capabilities{
		UsesGemini3Features:  isGemini && !isOlder,
		SupportsGemini2Tools: !preGemini2,
	}
}

// modelPath renders the model id as a resource path.
func modelPath(modelID string) string {
	if strings.Contains(modelID, "/") {
		return modelID
	}
	return "models/" + modelID
}

// buildRequest renders call options into a generateContent body.
func (m *languageModel) buildRequest(opts provider.CallOptions) (*generateRequest, []provider.Warning, error) {
	converted, err := convertPrompt(opts.Prompt)
	if err != nil {
		return nil, nil, err
	}
	warnings := converted.Warnings

	req := &generateRequest{
		Contents:          converted.Contents,
		SystemInstruction: converted.SystemInstruction,
		GenerationConfig: &generationConfig{
			MaxOutputTokens:  opts.MaxOutputTokens,
			Temperature:      opts.Temperature,
			TopP:             opts.TopP,
			TopK:             opts.TopK,
			Seed:             opts.Seed,
			StopSequences:    opts.StopSequences,
			PresencePenalty:  opts.PresencePenalty,
			FrequencyPenalty: opts.FrequencyPenalty,
		},
	}

	if len(req.Contents) == 0 {
		return nil, nil, &provider.InvalidPromptError{
			Message: "at least one user or assistant message is required",
		}
	}

	m.applyThinking(req, opts)

	toolWarnings, err := m.applyTools(req, opts)
	if err != nil {
		return nil, nil, err
	}
	warnings = append(warnings, toolWarnings...)

	warnings = append(warnings, m.applyResponseFormat(req, opts)...)
	warnings = append(warnings, m.applyProviderOptions(req, opts)...)

	return req, warnings, nil
}

// thinkingBudgetFraction is the share of the output budget given to thinking
// at each effort level, for models that take an explicit budget.
var thinkingBudgetFraction = map[provider.ReasoningEffort]float64{
	provider.ReasoningMinimal: 0.02,
	provider.ReasoningLow:     0.10,
	provider.ReasoningMedium:  0.30,
	provider.ReasoningHigh:    0.60,
	provider.ReasoningXHigh:   0.90,
}

// defaultMaxOutputTokens is the ceiling assumed when the caller sets none,
// used only to size a thinking budget.
const defaultMaxOutputTokens = 65536

// applyThinking translates the unified reasoning effort into whichever
// thinking shape the model accepts.
func (m *languageModel) applyThinking(req *generateRequest, opts provider.CallOptions) {
	effort := opts.Reasoning
	if effort == "" || effort == provider.ReasoningDefault {
		return
	}

	cfg := &thinkingConfig{IncludeThoughts: true}

	switch {
	case m.caps.UsesGemini3Features:
		// Gemini 3 takes a level and rejects a token budget. Thinking cannot
		// be switched off entirely, so "none" becomes the lowest level.
		cfg.ThinkingLevel = gemini3ThinkingLevel(effort)
		if effort == provider.ReasoningNone {
			cfg.IncludeThoughts = false
		}

	case effort == provider.ReasoningNone:
		budget := int64(0)
		cfg.ThinkingBudget = &budget
		cfg.IncludeThoughts = false

	default:
		maxTokens := int64(defaultMaxOutputTokens)
		if opts.MaxOutputTokens != nil {
			maxTokens = *opts.MaxOutputTokens
		}
		fraction, ok := thinkingBudgetFraction[effort]
		if !ok {
			return
		}
		budget := int64(math.Round(float64(maxTokens) * fraction))
		cfg.ThinkingBudget = &budget
	}

	req.GenerationConfig.ThinkingConfig = cfg
}

// gemini3ThinkingLevel maps a unified effort onto Gemini 3's levels.
func gemini3ThinkingLevel(effort provider.ReasoningEffort) string {
	switch effort {
	case provider.ReasoningNone, provider.ReasoningMinimal:
		return "minimal"
	case provider.ReasoningLow:
		return "low"
	case provider.ReasoningMedium:
		return "medium"
	case provider.ReasoningHigh, provider.ReasoningXHigh:
		// Gemini 3 has no level above high.
		return "high"
	default:
		return "medium"
	}
}

// applyTools renders the tool list and tool choice.
//
// Google takes a single tool object holding every declaration, rather than one
// object per tool.
func (m *languageModel) applyTools(req *generateRequest, opts provider.CallOptions) ([]provider.Warning, error) {
	var (
		warnings     []provider.Warning
		declarations []functionDeclaration
	)

	// hosted collects the provider-run tools, which Google takes as sibling
	// keys of the same tool object rather than as declarations.
	hosted := apiTool{}
	var hasHosted bool

	for _, tool := range opts.Tools {
		switch t := tool.(type) {
		case provider.FunctionTool:
			declarations = append(declarations, functionDeclaration{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  toOpenAPISchema(t.InputSchema, true),
			})

		case provider.ProviderTool:
			if !applyHostedTool(&hosted, t, m.caps.SupportsGemini2Tools) {
				warnings = append(warnings, provider.Unsupported(
					"provider-executed tools",
					fmt.Sprintf("tool %q was dropped: %q is not a hosted tool %s supports",
						t.Name, t.ID, m.modelID),
				))
				continue
			}
			hasHosted = true
		}
	}

	// Google rejects a request that mixes function declarations with hosted
	// tools, so the combination is refused here with a message that says why
	// rather than surfacing as an opaque 400.
	if len(declarations) > 0 && hasHosted {
		return nil, fmt.Errorf(
			"google: hosted tools cannot be combined with function tools in one request")
	}

	switch {
	case len(declarations) > 0:
		req.Tools = []apiTool{{FunctionDeclarations: declarations}}
	case hasHosted:
		req.Tools = []apiTool{hosted}
	}

	if opts.ToolChoice == nil {
		return warnings, nil
	}

	cfg := &functionCallingConfig{}
	switch opts.ToolChoice.Type {
	case provider.ToolChoiceAuto:
		cfg.Mode = "AUTO"
	case provider.ToolChoiceNone:
		cfg.Mode = "NONE"
	case provider.ToolChoiceRequired:
		cfg.Mode = "ANY"
	case provider.ToolChoiceTool:
		cfg.Mode = "ANY"
		cfg.AllowedFunctionNames = []string{opts.ToolChoice.ToolName}
	}
	req.ToolConfig = &toolConfig{FunctionCallingConfig: cfg}

	return warnings, nil
}

// applyResponseFormat handles JSON output requests.
func (m *languageModel) applyResponseFormat(req *generateRequest, opts provider.CallOptions) []provider.Warning {
	if opts.ResponseFormat == nil || opts.ResponseFormat.Type != "json" {
		return nil
	}

	req.GenerationConfig.ResponseMimeType = "application/json"
	if opts.ResponseFormat.Schema != nil {
		req.GenerationConfig.ResponseSchema = toOpenAPISchema(opts.ResponseFormat.Schema, false)
	}
	return nil
}

// applyProviderOptions folds in google-specific settings.
func (m *languageModel) applyProviderOptions(req *generateRequest, opts provider.CallOptions) []provider.Warning {
	block := opts.ProviderOptions.Get(providerID)
	if block == nil {
		return nil
	}

	if v, ok := block["cachedContent"].(string); ok && v != "" {
		req.CachedContent = v
	}

	if raw, ok := block["safetySettings"].([]any); ok {
		for _, item := range raw {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			category, _ := entry["category"].(string)
			threshold, _ := entry["threshold"].(string)
			if category != "" && threshold != "" {
				req.SafetySettings = append(req.SafetySettings, safetySetting{
					Category: category, Threshold: threshold,
				})
			}
		}
	}

	// An explicit thinkingConfig overrides whatever the effort mapping chose.
	if raw, ok := block["thinkingConfig"].(map[string]any); ok {
		cfg := req.GenerationConfig.ThinkingConfig
		if cfg == nil {
			cfg = &thinkingConfig{}
			req.GenerationConfig.ThinkingConfig = cfg
		}
		if v, ok := raw["includeThoughts"].(bool); ok {
			cfg.IncludeThoughts = v
		}
		if v, ok := raw["thinkingLevel"].(string); ok {
			cfg.ThinkingLevel = v
			cfg.ThinkingBudget = nil
		}
		if v, ok := raw["thinkingBudget"].(float64); ok {
			budget := int64(v)
			cfg.ThinkingBudget = &budget
			cfg.ThinkingLevel = ""
		}
	}

	return nil
}

// DoGenerate implements provider.LanguageModel.
func (m *languageModel) DoGenerate(ctx context.Context, opts provider.CallOptions) (*provider.GenerateResult, error) {
	req, warnings, err := m.buildRequest(opts)
	if err != nil {
		return nil, err
	}
	path := m.provider.path(m.modelID, "generateContent", false)

	resp, err := providerutil.Retry(ctx, m.provider.retry, func(ctx context.Context) (*generateResponse, error) {
		httpResp, err := m.provider.client.PostJSON(ctx, path, req, opts.Headers)
		if err != nil {
			return nil, err
		}
		var decoded generateResponse
		if err := providerutil.DecodeJSON(httpResp, &decoded); err != nil {
			return nil, err
		}
		return &decoded, nil
	})
	if err != nil {
		return nil, err
	}

	if resp.PromptFeedback != nil && resp.PromptFeedback.BlockReason != "" {
		return nil, &provider.APICallError{
			Message: "the prompt was blocked: " + resp.PromptFeedback.BlockReason,
			URL:     m.provider.client.BaseURL + path,
		}
	}
	if len(resp.Candidates) == 0 {
		return nil, &provider.InvalidResponseError{Message: "the provider returned no candidates"}
	}

	c := resp.Candidates[0]
	content, hasToolCalls := m.convertParts(c)

	requestBody, _ := json.Marshal(req)

	return &provider.GenerateResult{
		Content:      content,
		FinishReason: mapFinishReason(c.FinishReason, hasToolCalls),
		Usage:        convertUsage(resp.UsageMetadata),
		Warnings:     warnings,
		Request:      &provider.RequestInfo{Body: string(requestBody)},
		Response: &provider.ResponseInfo{
			ResponseMetadata: provider.ResponseMetadata{
				ID: resp.ResponseID, ModelID: resp.ModelVersion,
			},
		},
	}, nil
}

// convertParts maps a candidate's parts onto spec content, reporting whether
// any tool calls were present.
func (m *languageModel) convertParts(c candidate) ([]provider.Content, bool) {
	var (
		out          []provider.Content
		hasToolCalls bool
	)
	if c.Content == nil {
		return nil, false
	}

	for _, p := range c.Content.Parts {
		switch {
		case p.FunctionCall != nil:
			hasToolCalls = true
			args, err := json.Marshal(p.FunctionCall.Args)
			if err != nil {
				args = []byte("{}")
			}
			id := p.FunctionCall.ID
			if id == "" {
				// Google omits call ids on the public API, but a result has to
				// be matched to its call, so one is synthesised.
				id = providerutil.GenerateID("call", 12)
			}
			out = append(out, provider.ToolCall{
				ToolCallID:       id,
				ToolName:         p.FunctionCall.Name,
				Input:            string(args),
				ProviderMetadata: signatureMetadata(p.ThoughtSignature),
			})

		case p.Thought:
			out = append(out, provider.Reasoning{
				Text:             p.Text,
				ProviderMetadata: signatureMetadata(p.ThoughtSignature),
			})

		case p.ExecutableCode != nil:
			// The hosted code execution tool. Google reports it as a plain
			// part, but it is a tool call the provider already ran, so it is
			// presented as one and never executed locally.
			out = append(out, hostedCodeCall(*p.ExecutableCode))

		case p.CodeExecutionResult != nil:
			out = append(out, hostedCodeResult(*p.CodeExecutionResult))

		case p.InlineData != nil:
			out = append(out, provider.File{
				MediaType: p.InlineData.MimeType,
				Data:      provider.FileDataBytes{Base64: p.InlineData.Data},
			})

		case p.Text != "":
			out = append(out, provider.Text{
				Text:             p.Text,
				ProviderMetadata: signatureMetadata(p.ThoughtSignature),
			})
		}
	}

	for _, src := range groundingSources(c.GroundingMetadata) {
		out = append(out, src)
	}

	return out, hasToolCalls
}

// signatureMetadata wraps a thought signature for the round trip back.
func signatureMetadata(signature string) provider.ProviderMetadata {
	if signature == "" {
		return nil
	}
	return provider.ProviderMetadata{providerID: {"thoughtSignature": signature}}
}

// groundingSources converts grounding chunks into source parts.
func groundingSources(md *groundingMetadata) []provider.Source {
	if md == nil {
		return nil
	}
	var out []provider.Source
	for _, chunk := range md.GroundingChunks {
		if chunk.Web == nil {
			continue
		}
		out = append(out, provider.Source{
			SourceType: provider.SourceURL,
			ID:         providerutil.GenerateID("src", 12),
			URL:        chunk.Web.URI,
			Title:      chunk.Web.Title,
		})
	}
	return out
}
