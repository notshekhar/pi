package provider

import (
	"context"
	"regexp"

	"github.com/notshekhar/pi/internal/modules/ai/jsonschema"
)

// ReasoningEffort controls how much the model thinks before answering.
type ReasoningEffort string

// Reasoning effort levels. ReasoningDefault leaves the choice to the provider.
const (
	ReasoningDefault ReasoningEffort = "provider-default"
	ReasoningNone    ReasoningEffort = "none"
	ReasoningMinimal ReasoningEffort = "minimal"
	ReasoningLow     ReasoningEffort = "low"
	ReasoningMedium  ReasoningEffort = "medium"
	ReasoningHigh    ReasoningEffort = "high"
	ReasoningXHigh   ReasoningEffort = "xhigh"
)

// ResponseFormat requests text or JSON output.
type ResponseFormat struct {
	// Type is "text" or "json".
	Type string
	// Schema constrains JSON output. Optional.
	Schema *jsonschema.Schema
	// Name and Description give the model extra guidance about the output.
	// Some providers require Name when a Schema is set.
	Name        string
	Description string
}

// Tool is a tool offered to the model: either a FunctionTool the client
// executes, or a ProviderTool the provider runs itself.
type Tool interface{ isTool() }

// FunctionTool is a tool the client executes. This is not the user-facing tool
// definition; core maps package pi's tools onto this.
type FunctionTool struct {
	// Name must be unique within a call.
	Name string
	// Description tells the model what the tool is for. Worth writing well:
	// it is the model's only guide to when the tool applies.
	Description string
	// InputSchema describes the arguments the model must produce.
	InputSchema *jsonschema.Schema

	// InputExamples show the model well-formed calls.
	InputExamples []JSONObject
	// Strict asks the provider to constrain decoding so the output always
	// validates against InputSchema. Providers that support it may reject
	// schemas using features they cannot constrain.
	Strict bool

	ProviderOptions ProviderOptions
}

// ProviderTool is a tool the provider hosts and executes, such as Anthropic's
// web search. ID is formatted "{provider}.{tool}".
type ProviderTool struct {
	ID   string
	Name string
	Args map[string]any
}

func (FunctionTool) isTool() {}
func (ProviderTool) isTool() {}

// ToolChoiceType selects a tool-choice strategy.
type ToolChoiceType string

// Tool choice strategies.
const (
	// ToolChoiceAuto lets the model decide whether to call a tool.
	ToolChoiceAuto ToolChoiceType = "auto"
	// ToolChoiceNone forbids tool calls.
	ToolChoiceNone ToolChoiceType = "none"
	// ToolChoiceRequired forces some tool call.
	ToolChoiceRequired ToolChoiceType = "required"
	// ToolChoiceTool forces one named tool.
	ToolChoiceTool ToolChoiceType = "tool"
)

// ToolChoice constrains which tool the model may call. ToolName is set only
// when Type is ToolChoiceTool.
type ToolChoice struct {
	Type     ToolChoiceType
	ToolName string
}

// CallOptions is the full input to a model call. Optional numeric settings are
// pointers so that "unset" is distinguishable from a deliberate zero;
// Temperature 0 is meaningful.
type CallOptions struct {
	// Prompt is the message list. Required.
	Prompt Prompt

	MaxOutputTokens *int64
	Temperature     *float64
	TopP            *float64
	TopK            *int64
	PresencePenalty *float64
	// FrequencyPenalty discourages repeating tokens already produced.
	FrequencyPenalty *float64
	// Seed requests deterministic sampling where the provider supports it.
	Seed *int64

	// StopSequences halt generation when produced. Providers cap how many are
	// accepted, and warn when the list is truncated.
	StopSequences []string

	ResponseFormat *ResponseFormat

	Tools      []Tool
	ToolChoice *ToolChoice

	// Reasoning sets the thinking budget. Empty means ReasoningDefault.
	Reasoning ReasoningEffort

	// IncludeRawChunks makes streaming calls also emit Raw parts.
	IncludeRawChunks bool

	// Headers are extra HTTP headers for this call only.
	Headers Headers

	// ProviderOptions carries provider-specific settings, keyed by provider id.
	ProviderOptions ProviderOptions
}

// RequestInfo records what was sent, for logging and debugging.
type RequestInfo struct {
	// Body is the request payload, usually as raw JSON bytes.
	Body JSONValue
}

// ResponseInfo records what came back, for logging and debugging.
type ResponseInfo struct {
	ResponseMetadata
	Headers Headers
	Body    JSONValue
}

// GenerateResult is the outcome of a non-streaming call.
type GenerateResult struct {
	// Content is the model's output in the order it was produced.
	Content []Content

	FinishReason FinishReason
	Usage        Usage

	ProviderMetadata ProviderMetadata
	Request          *RequestInfo
	Response         *ResponseInfo

	// Warnings reports settings the model could not honour.
	Warnings []Warning
}

// StreamResult is the outcome of a streaming call.
//
// Stream is closed when generation ends. The caller must drain it or cancel
// the context; abandoning a stream without either leaks the provider's
// reader goroutine and its HTTP connection.
//
// Errors arrive as ErrorPart values in the stream rather than as a Go error,
// because a call can produce several and can continue past them.
type StreamResult struct {
	Stream <-chan StreamPart

	Request  *RequestInfo
	Response *ResponseInfo
}

// LanguageModel is the contract every provider implements.
//
// The Do prefix marks these as the low-level entry points. Application code
// should call ai.GenerateText or ai.StreamText, which add tool execution,
// multi-step agent loops, retries and prompt conversion on top.
type LanguageModel interface {
	// SpecificationVersion reports the spec revision, always "v4".
	SpecificationVersion() string

	// Provider is the provider id, e.g. "anthropic".
	Provider() string

	// ModelID is the provider-specific model id.
	ModelID() string

	// SupportedURLs maps a media type pattern ("image/*", "*/*") to URL
	// patterns the provider fetches natively. Matching URLs are passed
	// through untouched; the rest are downloaded and inlined by core.
	// Matching is done against lower-cased URLs.
	SupportedURLs(ctx context.Context) (map[string][]*regexp.Regexp, error)

	// DoGenerate runs a non-streaming call.
	DoGenerate(ctx context.Context, opts CallOptions) (*GenerateResult, error)

	// DoStream runs a streaming call.
	DoStream(ctx context.Context, opts CallOptions) (*StreamResult, error)
}
