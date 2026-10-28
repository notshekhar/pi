package anthropic

import "encoding/json"

// messagesRequest is the body of POST /v1/messages.
type messagesRequest struct {
	Model     string `json:"model"`
	MaxTokens int64  `json:"max_tokens"`

	System   []systemBlock `json:"system,omitempty"`
	Messages []apiMessage  `json:"messages"`

	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"top_p,omitempty"`
	TopK          *int64   `json:"top_k,omitempty"`
	StopSequences []string `json:"stop_sequences,omitempty"`

	Tools      []apiTool   `json:"tools,omitempty"`
	ToolChoice *toolChoice `json:"tool_choice,omitempty"`

	Thinking     *thinkingConfig `json:"thinking,omitempty"`
	OutputConfig *outputConfig   `json:"output_config,omitempty"`

	Stream bool `json:"stream,omitempty"`
}

// systemBlock is one block of the system prompt. Anthropic accepts a bare
// string too, but the block form is required to attach cache breakpoints.
type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// cacheControl marks a prompt prefix as cacheable. Anthropic allows at most
// four breakpoints per request.
type cacheControl struct {
	Type string `json:"type"`
	// TTL is "5m" or "1h". Empty uses the API default of five minutes.
	TTL string `json:"ttl,omitempty"`
}

// thinkingConfig enables extended thinking.
//
// Older models take {"type":"enabled","budget_tokens":N}. Models with
// adaptive thinking take {"type":"adaptive","display":"summarized"} and set
// the level through output_config.effort instead; sending budget_tokens to
// those is rejected.
type thinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens *int64 `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
}

// outputConfig carries the effort level for adaptive-thinking models.
type outputConfig struct {
	Effort string `json:"effort,omitempty"`
}

// apiMessage is a user or assistant message.
type apiMessage struct {
	Role    string     `json:"role"`
	Content []apiBlock `json:"content"`
}

// apiBlock is one content block. It is a flat struct rather than a union
// because Anthropic's blocks overlap heavily and omitempty keeps the wire
// output correct; Type selects which fields are meaningful.
type apiBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// image, document
	Source *blockSource `json:"source,omitempty"`
	Title  string       `json:"title,omitempty"`
	// Context is document metadata passed to the model but excluded from
	// citations.
	Context   string     `json:"context,omitempty"`
	Citations *citations `json:"citations,omitempty"`

	// thinking. Thinking is a pointer because the API requires the field to be
	// present on every thinking block, and a model can legitimately return a
	// signed block whose text is empty — omitempty on a string would drop it
	// and the replay 400s with "missing field `thinking`".
	Thinking  *string `json:"thinking,omitempty"`
	Signature string  `json:"signature,omitempty"`

	// redacted_thinking
	Data string `json:"data,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	// Content is a string or a []apiBlock, which is why it is typed as any.
	Content any   `json:"content,omitempty"`
	IsError *bool `json:"is_error,omitempty"`

	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// blockSource carries file bytes, text, or a URL for image and document blocks.
type blockSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
	// Text is used by {"type":"text"} document sources.
	Text string `json:"text,omitempty"`
	// FileID is used by {"type":"file"} sources referring to an upload.
	FileID string `json:"file_id,omitempty"`
}

// citations toggles document citation generation.
type citations struct {
	Enabled bool `json:"enabled"`
}

// apiTool is a client-executed tool definition.
type apiTool struct {
	Type        string `json:"type,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// InputSchema is omitted for provider-hosted tools: their shape is fixed
	// by the dated Type, and sending a schema for one is rejected.
	InputSchema any `json:"input_schema,omitempty"`
	// CacheControl on the last tool caches the whole tool block.
	CacheControl *cacheControl `json:"cache_control,omitempty"`

	// Fields below belong to provider-hosted tools only.

	// MaxUses caps how many times the model may call a hosted tool per turn.
	MaxUses          *int     `json:"max_uses,omitempty"`
	AllowedDomains   []string `json:"allowed_domains,omitempty"`
	BlockedDomains   []string `json:"blocked_domains,omitempty"`
	MaxContentTokens *int     `json:"max_content_tokens,omitempty"`
	// UserLocation biases web search results geographically.
	UserLocation map[string]any `json:"user_location,omitempty"`
	Citations    *citations     `json:"citations,omitempty"`

	// Computer-use display geometry.
	DisplayWidthPx  *int `json:"display_width_px,omitempty"`
	DisplayHeightPx *int `json:"display_height_px,omitempty"`
	DisplayNumber   *int `json:"display_number,omitempty"`
}

// toolChoice constrains tool selection. Anthropic spells "required" as "any".
type toolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
	// DisableParallelToolUse forces at most one tool call per turn.
	DisableParallelToolUse *bool `json:"disable_parallel_tool_use,omitempty"`
}

// messagesResponse is a non-streaming completion.
type messagesResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Model      string         `json:"model"`
	Content    []apiBlock     `json:"content"`
	StopReason *string        `json:"stop_reason"`
	Usage      anthropicUsage `json:"usage"`
}

// anthropicUsage is the token accounting Anthropic reports.
//
// InputTokens counts only uncached tokens: the cache figures are additional,
// so a true prompt total is the sum of all three.
type anthropicUsage struct {
	InputTokens              int64  `json:"input_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`

	OutputTokensDetails *struct {
		ThinkingTokens *int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`

	// Iterations breaks usage down when the server ran several sampling
	// passes, e.g. context compaction.
	Iterations []usageIteration `json:"iterations"`
}

// usageIteration is one server-side sampling pass.
type usageIteration struct {
	// Type is "message", "compaction", "advisor_message", or
	// "fallback_message".
	Type                     string `json:"type"`
	Model                    string `json:"model"`
	InputTokens              int64  `json:"input_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
}

// streamEvent is one decoded server-sent event from a streaming call.
type streamEvent struct {
	Type string `json:"type"`

	// message_start
	Message *messagesResponse `json:"message"`

	// content_block_start
	Index        int       `json:"index"`
	ContentBlock *apiBlock `json:"content_block"`

	// content_block_delta
	Delta *streamDelta `json:"delta"`

	// message_delta
	Usage *anthropicUsage `json:"usage"`

	// error
	Error *apiError `json:"error"`
}

// streamDelta is the payload of content_block_delta and message_delta.
type streamDelta struct {
	Type string `json:"type"`

	// text_delta
	Text string `json:"text"`
	// thinking_delta
	Thinking string `json:"thinking"`
	// signature_delta
	Signature string `json:"signature"`
	// input_json_delta
	PartialJSON string `json:"partial_json"`

	// message_delta carries the stop reason here.
	StopReason   *string `json:"stop_reason"`
	StopSequence *string `json:"stop_sequence"`
}

// apiError is Anthropic's error payload.
type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
