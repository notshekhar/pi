package provider

// Prompt is the standardized message list handed to a model. It is not the
// user-facing prompt type: core maps friendlier shapes onto this so the
// user-facing API can evolve without breaking providers.
type Prompt []Message

// Message is one message in a Prompt: SystemMessage, UserMessage,
// AssistantMessage, or ToolMessage.
type Message interface {
	isMessage()
	// Role returns the wire role of the message.
	Role() string
}

// SystemMessage carries system instructions.
type SystemMessage struct {
	Content         string
	ProviderOptions ProviderOptions
}

// UserMessage is input from the user. Parts may be TextPart or FilePart.
type UserMessage struct {
	Content         []UserPart
	ProviderOptions ProviderOptions
}

// AssistantMessage is output previously produced by the model, replayed back
// as history. Parts may be TextPart, FilePart, ReasoningPart,
// ReasoningFilePart, CustomPart, ToolCallPart, or ToolResultPart.
type AssistantMessage struct {
	Content         []AssistantPart
	ProviderOptions ProviderOptions
}

// ToolMessage carries results for tool calls the model requested. Parts may be
// ToolResultPart or ToolApprovalResponsePart.
type ToolMessage struct {
	Content         []ToolPart
	ProviderOptions ProviderOptions
}

func (SystemMessage) isMessage()    {}
func (UserMessage) isMessage()      {}
func (AssistantMessage) isMessage() {}
func (ToolMessage) isMessage()      {}

// Role returns "system".
func (SystemMessage) Role() string { return "system" }

// Role returns "user".
func (UserMessage) Role() string { return "user" }

// Role returns "assistant".
func (AssistantMessage) Role() string { return "assistant" }

// Role returns "tool".
func (ToolMessage) Role() string { return "tool" }

// Part markers. A single concrete part type may be valid in several roles, so
// the role-scoped interfaces overlap deliberately.
type (
	// UserPart is a part valid inside a UserMessage.
	UserPart interface{ isUserPart() }
	// AssistantPart is a part valid inside an AssistantMessage.
	AssistantPart interface{ isAssistantPart() }
	// ToolPart is a part valid inside a ToolMessage.
	ToolPart interface{ isToolPart() }
)

// TextPart is literal text. Valid in user and assistant messages.
type TextPart struct {
	Text            string
	ProviderOptions ProviderOptions
}

// FilePart is a file attachment. Valid in user and assistant messages.
//
// MediaType is either a full IANA media type ("image/png") or just the
// top-level segment ("image"); a "*" subtype is equivalent to the segment
// alone.
type FilePart struct {
	Data            FileData
	MediaType       string
	Filename        string
	ProviderOptions ProviderOptions
}

// ReasoningPart is model reasoning replayed as history. Providers that
// require signed reasoning (Anthropic, OpenAI) round-trip the signature
// through ProviderOptions.
type ReasoningPart struct {
	Text            string
	ProviderOptions ProviderOptions
}

// ReasoningFilePart is a file produced as part of reasoning. Data is
// FileDataBytes or FileDataURL.
type ReasoningFilePart struct {
	Data            FileData
	MediaType       string
	ProviderOptions ProviderOptions
}

// CustomPart is an opaque provider-specific part with no standardized payload.
// Kind is formatted "{provider}.{type}".
type CustomPart struct {
	Kind            string
	ProviderOptions ProviderOptions
}

// ToolCallPart is a tool call replayed as history. Input is the decoded
// arguments object, not a JSON string.
type ToolCallPart struct {
	ToolCallID       string
	ToolName         string
	Input            JSONValue
	ProviderExecuted bool
	ProviderOptions  ProviderOptions
}

// ToolResultPart is the outcome of a tool call. Valid in tool messages and,
// for provider-executed tools, in assistant messages.
type ToolResultPart struct {
	ToolCallID      string
	ToolName        string
	Output          ToolResultOutput
	ProviderOptions ProviderOptions
}

// ToolApprovalResponsePart answers a ToolApprovalRequest.
type ToolApprovalResponsePart struct {
	ApprovalID      string
	Approved        bool
	Reason          string
	ProviderOptions ProviderOptions
}

func (TextPart) isUserPart() {}
func (FilePart) isUserPart() {}

func (TextPart) isAssistantPart()          {}
func (FilePart) isAssistantPart()          {}
func (ReasoningPart) isAssistantPart()     {}
func (ReasoningFilePart) isAssistantPart() {}
func (CustomPart) isAssistantPart()        {}
func (ToolCallPart) isAssistantPart()      {}
func (ToolResultPart) isAssistantPart()    {}

func (ToolResultPart) isToolPart()           {}
func (ToolApprovalResponsePart) isToolPart() {}

// ToolResultOutput is the payload of a ToolResultPart: ToolOutputText,
// ToolOutputJSON, ToolOutputErrorText, ToolOutputErrorJSON,
// ToolOutputExecutionDenied, or ToolOutputContent.
type ToolResultOutput interface{ isToolResultOutput() }

// ToolOutputText is plain text sent straight to the API.
type ToolOutputText struct {
	Value           string
	ProviderOptions ProviderOptions
}

// ToolOutputJSON is a JSON-serializable result.
type ToolOutputJSON struct {
	Value           JSONValue
	ProviderOptions ProviderOptions
}

// ToolOutputErrorText is a failure reported as text.
type ToolOutputErrorText struct {
	Value           string
	ProviderOptions ProviderOptions
}

// ToolOutputErrorJSON is a failure reported as JSON.
type ToolOutputErrorJSON struct {
	Value           JSONValue
	ProviderOptions ProviderOptions
}

// ToolOutputExecutionDenied reports that the user refused the call.
type ToolOutputExecutionDenied struct {
	Reason          string
	ProviderOptions ProviderOptions
}

// ToolOutputContent is multi-modal tool output, e.g. text plus a screenshot.
// Parts are ToolContentText, ToolContentFile, or ToolContentCustom.
type ToolOutputContent struct {
	Value []ToolContentPart
}

func (ToolOutputText) isToolResultOutput()            {}
func (ToolOutputJSON) isToolResultOutput()            {}
func (ToolOutputErrorText) isToolResultOutput()       {}
func (ToolOutputErrorJSON) isToolResultOutput()       {}
func (ToolOutputExecutionDenied) isToolResultOutput() {}
func (ToolOutputContent) isToolResultOutput()         {}

// ToolContentPart is one part of a ToolOutputContent.
type ToolContentPart interface{ isToolContentPart() }

// ToolContentText is a text part of multi-modal tool output.
type ToolContentText struct {
	Text            string
	ProviderOptions ProviderOptions
}

// ToolContentFile is a file part of multi-modal tool output.
type ToolContentFile struct {
	Data            FileData
	MediaType       string
	Filename        string
	ProviderOptions ProviderOptions
}

// ToolContentCustom is an opaque provider-specific part.
type ToolContentCustom struct {
	ProviderOptions ProviderOptions
}

func (ToolContentText) isToolContentPart()   {}
func (ToolContentFile) isToolContentPart()   {}
func (ToolContentCustom) isToolContentPart() {}
