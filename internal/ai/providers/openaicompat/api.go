package openaicompat

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
)

// chatRequest is the body of POST /chat/completions.
type chatRequest struct {
	Model    string       `json:"model"`
	Messages []apiMessage `json:"messages"`

	// MaxTokens is the legacy field. Newer OpenAI models require
	// max_completion_tokens instead, which UseMaxCompletionTokens selects.
	MaxTokens           *int64 `json:"max_tokens,omitzero"`
	MaxCompletionTokens *int64 `json:"max_completion_tokens,omitzero"`

	Temperature      *float64 `json:"temperature,omitzero"`
	TopP             *float64 `json:"top_p,omitzero"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitzero"`
	PresencePenalty  *float64 `json:"presence_penalty,omitzero"`
	Seed             *int64   `json:"seed,omitzero"`
	Stop             []string `json:"stop,omitempty"`

	Tools      []apiTool `json:"tools,omitempty"`
	ToolChoice any       `json:"tool_choice,omitzero"`

	ResponseFormat  any    `json:"response_format,omitzero"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	Stream        bool           `json:"stream,omitzero"`
	StreamOptions *streamOptions `json:"stream_options,omitzero"`

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
	Content any `json:"content,omitzero"`

	// ReasoningContent replays an assistant turn's reasoning. A pointer, so
	// a model that requires the field gets it even when empty.
	ReasoningContent *string `json:"reasoning_content,omitzero"`

	// Reasoning and ReasoningDetails are OpenRouter's replay of a turn's
	// reasoning and its signed details.
	Reasoning        string `json:"reasoning,omitempty"`
	ReasoningDetails []any  `json:"reasoning_details,omitempty"`

	// ToolCalls is set on assistant messages.
	ToolCalls []apiToolCall `json:"tool_calls,omitempty"`

	// ToolCallID is set on tool messages.
	ToolCallID string `json:"tool_call_id,omitempty"`

	// Name optionally labels a participant.
	Name string `json:"name,omitempty"`

	// Prefix marks a trailing assistant turn the model continues (Mistral).
	Prefix bool `json:"prefix,omitzero"`
}

// contentPart is one part of a multi-modal message.
type contentPart struct {
	Type string `json:"type"`

	Text string `json:"text,omitempty"`

	ImageURL *imageURL `json:"image_url,omitzero"`
	File     *fileData `json:"file,omitzero"`

	// CacheControl marks a prompt-cache breakpoint after this part.
	CacheControl *cacheControl `json:"cache_control,omitzero"`
}

// cacheControl is Anthropic's breakpoint, as gateways relay it.
type cacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
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
	Index    *int            `json:"index,omitzero"`
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
	Strict      *bool  `json:"strict,omitzero"`
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

	// ReasoningDetails is OpenRouter's structured reasoning: text with its
	// signature, summaries, and encrypted blocks.
	ReasoningDetails []map[string]any `json:"reasoning_details"`
	Reasoning        string           `json:"reasoning"`

	ToolCalls []apiToolCall `json:"tool_calls"`

	// thinking is reasoning that arrived as content parts (Mistral's
	// [{"type":"thinking","thinking":[{"type":"text",...}]}]).
	thinking string
}

// UnmarshalJSON accepts content as a string or as a list of parts, which is
// how Mistral's reasoning models stream it. Decoding the list as a string
// failed the whole chunk.
func (m *apiMessageOut) UnmarshalJSON(data []byte) error {
	type plain apiMessageOut
	var wire struct {
		plain
		Content jsontext.Value `json:"content"`
	}
	if err := json.Unmarshal(data, &wire, jsonopt.Read); err != nil {
		return err
	}
	*m = apiMessageOut(wire.plain)
	raw := bytes.TrimSpace(wire.Content)
	switch {
	case len(raw) == 0 || string(raw) == "null":
	case raw[0] == '"':
		return json.Unmarshal(raw, &m.Content, jsonopt.Read)
	case raw[0] == '[':
		var parts []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"thinking"`
		}
		if err := json.Unmarshal(raw, &parts, jsonopt.Read); err != nil {
			return err
		}
		var text, thinking strings.Builder
		for _, p := range parts {
			switch p.Type {
			case "text":
				text.WriteString(p.Text)
			case "thinking":
				for _, t := range p.Thinking {
					if t.Type == "text" {
						thinking.WriteString(t.Text)
					}
				}
			}
		}
		m.Content, m.thinking = text.String(), thinking.String()
	}
	return nil
}

// reasoningText returns whichever reasoning field the gateway populated.
func (m *apiMessageOut) reasoningText() string {
	if m.ReasoningContent != "" {
		return m.ReasoningContent
	}
	if m.Reasoning != "" {
		return m.Reasoning
	}
	return m.thinking
}

// apiUsage is the OpenAI token accounting.
//
// PromptTokens already includes cached tokens, which is the opposite of
// Anthropic's convention.
type apiUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`

	PromptTokensDetails *promptTokensDetails `json:"prompt_tokens_details"`

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

	// NumCachedTokens is Mistral's spelling.
	NumCachedTokens *int64 `json:"num_cached_tokens"`
}

// promptTokensDetails breaks prompt_tokens down.
type promptTokensDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
	// CacheWriteTokens is OpenRouter's count of tokens written to an
	// explicit (Anthropic) cache, billed at the write price.
	CacheWriteTokens int64 `json:"cache_write_tokens"`
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
	if u.NumCachedTokens != nil {
		return *u.NumCachedTokens
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
	base, err := json.Marshal(*req, jsonopt.Write)
	if err != nil {
		return nil, err
	}
	if len(req.Extra) == 0 {
		return base, nil
	}

	var merged map[string]any
	if err := json.Unmarshal(base, &merged, jsonopt.Read); err != nil {
		return nil, err
	}
	for k, v := range req.Extra {
		merged[k] = v
	}
	return json.Marshal(merged, jsonopt.Write)
}
