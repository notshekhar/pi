package provider

// StreamPart is one event in a model output stream.
//
// Blocks are delimited rather than implied: a text block arrives as
// TextStart, one or more TextDelta, then TextEnd, all sharing an ID. Several
// blocks may interleave, so consumers must key on ID rather than assume the
// stream is sequential. Tool calls stream the same way through
// ToolInputStart/ToolInputDelta/ToolInputEnd and are then followed by a
// complete ToolCall.
//
// All Content types are also StreamParts.
//
// The marker method is exported and returns the part's wire discriminator, so
// that higher layers can define their own parts on the same stream and so a
// part can be serialized without a type switch. Values are the same strings
// the AI SDK uses ("text-delta", "tool-input-start", and so on).
type StreamPart interface{ StreamPartType() string }

// StreamStart is the first part of every stream and carries warnings about
// the call, such as settings the model ignored.
type StreamStart struct {
	Warnings []Warning
}

// TextStart opens a text block.
type TextStart struct {
	ID               string
	ProviderMetadata ProviderMetadata
}

// TextDelta appends to the open text block with the matching ID.
type TextDelta struct {
	ID               string
	Delta            string
	ProviderMetadata ProviderMetadata
}

// TextEnd closes the text block with the matching ID.
type TextEnd struct {
	ID               string
	ProviderMetadata ProviderMetadata
}

// ReasoningStart opens a reasoning block.
type ReasoningStart struct {
	ID               string
	ProviderMetadata ProviderMetadata
}

// ReasoningDelta appends to the open reasoning block with the matching ID.
type ReasoningDelta struct {
	ID               string
	Delta            string
	ProviderMetadata ProviderMetadata
}

// ReasoningEnd closes the reasoning block with the matching ID.
type ReasoningEnd struct {
	ID               string
	ProviderMetadata ProviderMetadata
}

// ToolInputStart announces that the model has begun emitting arguments for a
// tool call. ID matches the ToolCallID of the ToolCall that will follow.
type ToolInputStart struct {
	ID       string
	ToolName string

	// ProviderExecuted reports that the provider will run the tool itself.
	ProviderExecuted bool
	// Dynamic marks a tool defined at runtime.
	Dynamic bool
	// Title is an optional human-readable label for the call.
	Title string

	ProviderMetadata ProviderMetadata
}

// ToolInputDelta appends a fragment of the JSON arguments for the tool call
// with the matching ID. Concatenated deltas equal the ToolCall's Input.
//
// Not every provider streams tool input: some emit a single delta holding the
// whole argument object, so consumers must not depend on incremental arrival.
type ToolInputDelta struct {
	ID               string
	Delta            string
	ProviderMetadata ProviderMetadata
}

// ToolInputEnd closes the argument stream for the tool call with the matching ID.
type ToolInputEnd struct {
	ID               string
	ProviderMetadata ProviderMetadata
}

// ResponseMetadataPart carries response identifiers as soon as they are known,
// rather than making callers wait for Finish.
type ResponseMetadataPart struct {
	ResponseMetadata
}

// Finish is the last meaningful part of a successful stream and carries the
// totals that are only known at the end.
type Finish struct {
	Usage            Usage
	FinishReason     FinishReason
	ProviderMetadata ProviderMetadata
}

// Raw is an unprocessed provider chunk, emitted only when CallOptions
// requested IncludeRawChunks.
type Raw struct {
	RawValue JSONValue
}

// ErrorPart reports an error mid-stream. A stream may carry several, and an
// error does not necessarily end the stream.
type ErrorPart struct {
	Err error
}

// StreamPartType returns the wire discriminator for each part.
func (StreamStart) StreamPartType() string          { return "stream-start" }
func (TextStart) StreamPartType() string            { return "text-start" }
func (TextDelta) StreamPartType() string            { return "text-delta" }
func (TextEnd) StreamPartType() string              { return "text-end" }
func (ReasoningStart) StreamPartType() string       { return "reasoning-start" }
func (ReasoningDelta) StreamPartType() string       { return "reasoning-delta" }
func (ReasoningEnd) StreamPartType() string         { return "reasoning-end" }
func (ToolInputStart) StreamPartType() string       { return "tool-input-start" }
func (ToolInputDelta) StreamPartType() string       { return "tool-input-delta" }
func (ToolInputEnd) StreamPartType() string         { return "tool-input-end" }
func (ResponseMetadataPart) StreamPartType() string { return "response-metadata" }
func (Finish) StreamPartType() string               { return "finish" }
func (Raw) StreamPartType() string                  { return "raw" }
func (ErrorPart) StreamPartType() string            { return "error" }

// Content types double as stream parts.
func (Text) StreamPartType() string                { return "text" }
func (Reasoning) StreamPartType() string           { return "reasoning" }
func (ReasoningFile) StreamPartType() string       { return "reasoning-file" }
func (File) StreamPartType() string                { return "file" }
func (Source) StreamPartType() string              { return "source" }
func (ToolCall) StreamPartType() string            { return "tool-call" }
func (ToolResult) StreamPartType() string          { return "tool-result" }
func (ToolApprovalRequest) StreamPartType() string { return "tool-approval-request" }
func (CustomContent) StreamPartType() string       { return "custom" }
