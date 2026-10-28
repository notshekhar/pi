package openaicompat

import "encoding/json"

// chatRequest is the body of POST /chat/completions.
type chatRequest struct {
	Model    string       `json:"model"`
	Messages []apiMessage `json:"messages"`

	// MaxTokens is the legacy field. Newer OpenAI models require
	// max_completion_tokens instead, which UseMaxCompletionTokens selects.
	MaxTokens           *int64 `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int64 `json:"max_completion_tokens,omitempty"`

	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"top_p,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`
	Seed             *int64   `json:"seed,omitempty"`
	Stop             []string `json:"stop,omitempty"`

	Tools      []apiTool `json:"tools,omitempty"`
	ToolChoice any       `json:"tool_choice,omitempty"`

	ResponseFormat  any    `json:"response_format,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`

	// Extra carries provider-specific fields that are merged into the body.
	Extra map[string]any `json:"-"`
}

// streamOptions asks for a final usage chunk, which is otherwise omitted from
// streaming responses.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// apiMessage is one chat message.
type apiMessage struct {
	Role string `json:"role"`

	// Content is a string for simple messages and a []contentPart for
	// multi-modal ones. It is omitted entirely for an assistant message that
	// only carries tool calls, because some gateways reject a null content.
	Content any `json:"content,omitempty"`

	// ToolCalls is set on assistant messages.
	ToolCalls []apiToolCall `json:"tool_calls,omitempty"`

	// ToolCallID is set on tool messages.
	ToolCallID string `json:"tool_call_id,omitempty"`

	// Name optionally labels a participant.
	Name string `json:"name,omitempty"`
}

// contentPart is one part of a multi-modal message.
type contentPart struct {
	Type string `json:"type"`

	Text string `json:"text,omitempty"`

	ImageURL *imageURL `json:"image_url,omitempty"`
	File     *fileData `json:"file,omitempty"`
}

// imageURL holds an image as a URL or a data: URI.
type imageURL struct {
	URL string `json:"url"`
	// Detail is "auto", "low" or "high".
	Detail string `json:"detail,omitempty"`
}

// fileData holds a non-image attachment.
type fileData struct {
	Filename string `json:"filename,omitempty"`
	FileData string `json:"file_data,omitempty"`
	FileID   string `json:"file_id,omitempty"`
}

// apiToolCall is a tool invocation on an assistant message.
type apiToolCall struct {
	// Index orders the call within a streaming delta. It is absent in
	// non-streaming responses.
	Index    *int            `json:"index,omitempty"`
	ID       string          `json:"id,omitempty"`
	Type     string          `json:"type,omitempty"`
	Function apiFunctionCall `json:"function"`
}

// apiFunctionCall is the name and arguments of a tool call. Arguments is a
// JSON string, not an object.
type apiFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// apiTool is a tool definition.
type apiTool struct {
	Type     string      `json:"type"`
	Function apiFunction `json:"function"`
}

// apiFunction describes a callable function.
type apiFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters"`
	Strict      *bool  `json:"strict,omitempty"`
}

// chatResponse is a non-streaming completion.
type chatResponse struct {
	ID      string    `json:"id"`
	Model   string    `json:"model"`
	Created int64     `json:"created"`
	Choices []choice  `json:"choices"`
	Usage   *apiUsage `json:"usage"`
}

// choice is one completion alternative.
type choice struct {
	Index        int            `json:"index"`
	Message      *apiMessageOut `json:"message"`
	Delta        *apiMessageOut `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

// apiMessageOut is a message the model produced.
type apiMessageOut struct {
	Role    string `json:"role"`
	Content string `json:"content"`

	// Reasoning is spelled differently across gateways: DeepSeek and several
	// others use reasoning_content, while some use reasoning. Both are read.
	ReasoningContent string `json:"reasoning_content"`
	Reasoning        string `json:"reasoning"`

	ToolCalls []apiToolCall `json:"tool_calls"`
}

// reasoningText returns whichever reasoning field the gateway populated.
func (m *apiMessageOut) reasoningText() string {
	if m.ReasoningContent != "" {
		return m.ReasoningContent
	}
	return m.Reasoning
}

// apiUsage is the OpenAI token accounting.
//
// PromptTokens already includes cached tokens, which is the opposite of
// Anthropic's convention.
type apiUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`

	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`

	CompletionTokensDetails *struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`

	// PromptCacheHitTokens is DeepSeek's spelling of the cache-read count.
	// It is read here rather than in a fork of this package because it is
	// simply an extra key that other gateways omit.
	PromptCacheHitTokens *int64 `json:"prompt_cache_hit_tokens"`

	// CachedTokens is Moonshot/Kimi's top-level spelling of the same
	// figure. OpenAI nests it under prompt_tokens_details instead.
	CachedTokens *int64 `json:"cached_tokens"`
}

// cachedInputTokens returns the cache-read count from whichever field the
// gateway populated.
func (u *apiUsage) cachedInputTokens() int64 {
	if u.PromptCacheHitTokens != nil {
		return *u.PromptCacheHitTokens
	}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
		return u.PromptTokensDetails.CachedTokens
	}
	if u.CachedTokens != nil {
		return *u.CachedTokens
	}
	return 0
}

// streamChunk is one decoded server-sent event.
type streamChunk struct {
	ID      string    `json:"id"`
	Model   string    `json:"model"`
	Choices []choice  `json:"choices"`
	Usage   *apiUsage `json:"usage"`
}

// marshalRequest encodes the body, merging any provider-specific extras.
//
// Extras are merged at the top level rather than nested, because gateways
// expose their own knobs as sibling fields of the standard ones.
func marshalRequest(req *chatRequest) ([]byte, error) {
	base, err := json.Marshal(*req)
	if err != nil {
		return nil, err
	}
	if len(req.Extra) == 0 {
		return base, nil
	}

	var merged map[string]any
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	for k, v := range req.Extra {
		merged[k] = v
	}
	return json.Marshal(merged)
}
