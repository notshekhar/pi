package bedrock

import "encoding/json"

// Bedrock's Converse API, which is the vendor-neutral shape: the same request
// body drives Claude, Llama, Mistral and Nova, with per-model settings tucked
// into additionalModelRequestFields.

// converseRequest is the Converse and ConverseStream body.
type converseRequest struct {
	// Messages excludes system prompts, which have their own field.
	Messages []apiMessage  `json:"messages"`
	System   []systemBlock `json:"system,omitempty"`

	InferenceConfig *inferenceConfig `json:"inferenceConfig,omitempty"`
	ToolConfig      *toolConfig      `json:"toolConfig,omitempty"`

	// AdditionalModelRequestFields carries settings Converse has no field for,
	// which is where Anthropic's thinking configuration goes.
	AdditionalModelRequestFields map[string]any `json:"additionalModelRequestFields,omitempty"`

	// AdditionalModelResponseFieldPaths asks Converse to echo named fields
	// from the model-specific payload, used for Anthropic's stop_sequence.
	AdditionalModelResponseFieldPaths []string `json:"additionalModelResponseFieldPaths,omitempty"`

	// ServiceTier selects a reserved-capacity or default serving tier.
	ServiceTier *serviceTier `json:"serviceTier,omitempty"`
}

// serviceTier names a Bedrock serving tier.
type serviceTier struct {
	Type string `json:"type"`
}

// systemBlock is one system prompt entry.
type systemBlock struct {
	Text string `json:"text,omitempty"`
	// CachePoint marks everything before it as cacheable.
	CachePoint *cachePoint `json:"cachePoint,omitempty"`
}

// cachePoint enables prompt caching up to this point.
type cachePoint struct {
	Type string `json:"type"`
}

// apiMessage is a user or assistant turn.
type apiMessage struct {
	Role    string     `json:"role"`
	Content []apiBlock `json:"content"`
}

// apiBlock is one content block. Converse discriminates by which key is
// present rather than by a type tag, so exactly one field is set.
type apiBlock struct {
	Text string `json:"text,omitempty"`

	Image    *imageBlock    `json:"image,omitempty"`
	Document *documentBlock `json:"document,omitempty"`
	Video    *videoBlock    `json:"video,omitempty"`

	ToolUse    *toolUseBlock    `json:"toolUse,omitempty"`
	ToolResult *toolResultBlock `json:"toolResult,omitempty"`

	// ReasoningContent carries extended thinking, including the signature that
	// has to survive a round trip.
	ReasoningContent *reasoningBlock `json:"reasoningContent,omitempty"`

	CachePoint *cachePoint `json:"cachePoint,omitempty"`
}

// imageBlock is an inline image.
type imageBlock struct {
	// Format is the bare subtype: "png", not "image/png".
	Format string      `json:"format"`
	Source imageSource `json:"source"`
}

// imageSource carries inline bytes or an S3 URI. Exactly one field is set.
type imageSource struct {
	Bytes      []byte      `json:"bytes,omitempty"`
	S3Location *s3Location `json:"s3Location,omitempty"`
}

// s3Location points at an object Bedrock fetches itself.
type s3Location struct {
	URI string `json:"uri"`
}

// videoBlock is an inline or S3-hosted video.
type videoBlock struct {
	Format string      `json:"format"`
	Source imageSource `json:"source"`
}

// documentBlock is an inline document.
type documentBlock struct {
	Format string      `json:"format"`
	Name   string      `json:"name"`
	Source imageSource `json:"source"`
}

// toolUseBlock is a tool call.
type toolUseBlock struct {
	ToolUseID string `json:"toolUseId"`
	Name      string `json:"name"`
	// Input is a decoded object, not a JSON string.
	Input any `json:"input"`
}

// toolResultBlock answers a tool call.
type toolResultBlock struct {
	ToolUseID string           `json:"toolUseId"`
	Content   []toolResultPart `json:"content"`
	// Status is "success" or "error".
	Status string `json:"status,omitempty"`
}

// toolResultPart is one part of a tool result.
type toolResultPart struct {
	Text string `json:"text,omitempty"`
	JSON any    `json:"json,omitempty"`
	// Image lets a tool return a screenshot.
	Image *imageBlock `json:"image,omitempty"`
}

// reasoningBlock carries extended thinking.
type reasoningBlock struct {
	ReasoningText *reasoningText `json:"reasoningText,omitempty"`
	// RedactedContent is thinking the provider encrypted.
	RedactedContent []byte `json:"redactedContent,omitempty"`
}

// reasoningText is thinking plus the signature that authenticates it.
type reasoningText struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

// inferenceConfig holds the sampling settings Converse understands.
type inferenceConfig struct {
	MaxTokens     *int64   `json:"maxTokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
	TopK          *int64   `json:"topK,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

// toolConfig declares the tools and how the model may choose among them.
type toolConfig struct {
	Tools      []toolEntry     `json:"tools"`
	ToolChoice *toolChoiceSpec `json:"toolChoice,omitempty"`
}

// toolEntry is one tool declaration.
type toolEntry struct {
	ToolSpec   *toolSpec   `json:"toolSpec,omitempty"`
	CachePoint *cachePoint `json:"cachePoint,omitempty"`
}

// toolSpec describes a client-executed tool.
type toolSpec struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	InputSchema toolSchema `json:"inputSchema"`
}

// toolSchema wraps the JSON Schema, which Converse nests under "json".
type toolSchema struct {
	JSON any `json:"json"`
}

// toolChoiceSpec constrains tool selection. Exactly one field is set.
type toolChoiceSpec struct {
	Auto *struct{}     `json:"auto,omitempty"`
	Any  *struct{}     `json:"any,omitempty"`
	Tool *namedToolRef `json:"tool,omitempty"`
}

// namedToolRef forces one tool.
type namedToolRef struct {
	Name string `json:"name"`
}

// converseResponse is the non-streaming reply.
type converseResponse struct {
	Output struct {
		Message *apiMessage `json:"message"`
	} `json:"output"`

	StopReason string      `json:"stopReason"`
	Usage      *apiUsage   `json:"usage"`
	Metrics    *apiMetrics `json:"metrics"`

	// AdditionalModelResponseFields carries per-model extras.
	AdditionalModelResponseFields json.RawMessage `json:"additionalModelResponseFields"`
}

// apiUsage reports token counts.
type apiUsage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	TotalTokens  int64 `json:"totalTokens"`

	// CacheReadInputTokens and CacheWriteInputTokens are reported separately
	// from InputTokens, matching Anthropic's own API: InputTokens is the
	// uncached count and the spec total is the sum of the three. The TypeScript
	// SDK treats them this way; folding them into InputTokens would
	// under-count a cached prompt.
	CacheReadInputTokens  *int64 `json:"cacheReadInputTokens"`
	CacheWriteInputTokens *int64 `json:"cacheWriteInputTokens"`
}

// apiMetrics reports latency.
type apiMetrics struct {
	LatencyMs int64 `json:"latencyMs"`
}

// streamEvent is one decoded ConverseStream frame's payload. Which fields are
// set depends on the frame's :event-type header.
type streamEvent struct {
	// messageStart
	Role string `json:"role"`

	// contentBlockStart
	Start *struct {
		ToolUse *struct {
			ToolUseID string `json:"toolUseId"`
			Name      string `json:"name"`
		} `json:"toolUse"`
	} `json:"start"`

	// contentBlockDelta
	Delta *struct {
		Text    string `json:"text"`
		ToolUse *struct {
			Input string `json:"input"`
		} `json:"toolUse"`
		ReasoningContent *struct {
			Text            string `json:"text"`
			Signature       string `json:"signature"`
			RedactedContent []byte `json:"redactedContent"`
		} `json:"reasoningContent"`
	} `json:"delta"`

	// contentBlockStart, contentBlockDelta and contentBlockStop
	ContentBlockIndex *int `json:"contentBlockIndex"`

	// messageStop
	StopReason string `json:"stopReason"`

	// metadata
	Usage   *apiUsage   `json:"usage"`
	Metrics *apiMetrics `json:"metrics"`

	// exception frames
	Message string `json:"message"`
}
