package google

// generateRequest is the body of :generateContent and :streamGenerateContent.
type generateRequest struct {
	Contents          []content         `json:"contents"`
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	Tools             []apiTool         `json:"tools,omitempty"`
	ToolConfig        *toolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
	SafetySettings    []safetySetting   `json:"safetySettings,omitempty"`
	CachedContent     string            `json:"cachedContent,omitempty"`
}

// content is a turn in the conversation. Google names the assistant role
// "model", and has no role for tool results: those go back as user turns
// carrying functionResponse parts.
type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

// part is one piece of a turn. Exactly one payload field is set; Google
// discriminates by which key is present rather than by a type tag.
type part struct {
	Text string `json:"text,omitempty"`

	// Thought marks a text part as reasoning rather than answer.
	Thought bool `json:"thought,omitempty"`
	// ThoughtSignature authenticates replayed reasoning. It must survive a
	// round trip or the model rejects the history.
	ThoughtSignature string `json:"thoughtSignature,omitempty"`

	InlineData *inlineData `json:"inlineData,omitempty"`
	FileData   *fileData   `json:"fileData,omitempty"`

	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`

	// ExecutableCode and CodeExecutionResult are the hosted code execution
	// tool: Google reports what it ran and what came back as ordinary parts
	// rather than as a function call.
	ExecutableCode      *executableCode      `json:"executableCode,omitempty"`
	CodeExecutionResult *codeExecutionResult `json:"codeExecutionResult,omitempty"`
}

// executableCode is code the hosted tool ran.
type executableCode struct {
	Language string `json:"language"`
	Code     string `json:"code"`
}

// codeExecutionResult is what running that code produced.
type codeExecutionResult struct {
	Outcome string `json:"outcome"`
	Output  string `json:"output"`
}

// inlineData is a base64-encoded attachment.
type inlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// fileData references an uploaded file or a URL the API can fetch.
type fileData struct {
	MimeType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri"`
}

// functionCall is a tool invocation. Args is a decoded object, not a JSON
// string, which is the opposite of the OpenAI and Anthropic conventions.
type functionCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// functionResponse is a tool result. Response is an object, so scalar results
// have to be wrapped.
type functionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

// apiTool groups function declarations. Google takes one tool object holding
// many declarations, not one object per tool.
type apiTool struct {
	FunctionDeclarations []functionDeclaration `json:"functionDeclarations,omitempty"`

	// The fields below enable provider-hosted tools. Google discriminates by
	// which key is present, and an empty object means "on with defaults".
	//
	// They are pointers because omitempty treats an empty map as absent, which
	// would silently drop every tool that takes no configuration.
	GoogleSearch  *hostedToolArgs `json:"googleSearch,omitempty"`
	CodeExecution *hostedToolArgs `json:"codeExecution,omitempty"`
	URLContext    *hostedToolArgs `json:"urlContext,omitempty"`
	// GoogleSearchRetrieval is the pre-Gemini-2 spelling of search grounding.
	GoogleSearchRetrieval *hostedToolArgs `json:"googleSearchRetrieval,omitempty"`
}

// hostedToolArgs is a hosted tool's configuration. Nil means the tool was not
// requested; an empty value marshals as {}, which turns it on with defaults.
type hostedToolArgs map[string]any

// functionDeclaration describes a callable function. Parameters is an OpenAPI
// 3.0 schema, not JSON Schema.
type functionDeclaration struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// toolConfig constrains tool selection.
type toolConfig struct {
	FunctionCallingConfig *functionCallingConfig `json:"functionCallingConfig,omitempty"`
}

// functionCallingConfig selects the tool-choice mode. Google spells the modes
// AUTO, ANY and NONE.
type functionCallingConfig struct {
	Mode                 string   `json:"mode"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

// generationConfig carries the sampling and output settings.
type generationConfig struct {
	MaxOutputTokens *int64   `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	TopK            *int64   `json:"topK,omitempty"`
	Seed            *int64   `json:"seed,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`

	// PresencePenalty and FrequencyPenalty are supported on newer models only.
	PresencePenalty  *float64 `json:"presencePenalty,omitempty"`
	FrequencyPenalty *float64 `json:"frequencyPenalty,omitempty"`

	ResponseMimeType string `json:"responseMimeType,omitempty"`
	ResponseSchema   any    `json:"responseSchema,omitempty"`

	ThinkingConfig *thinkingConfig `json:"thinkingConfig,omitempty"`
}

// thinkingConfig controls reasoning.
//
// Gemini 2.5 takes an explicit ThinkingBudget in tokens; Gemini 3 takes a
// ThinkingLevel instead and rejects a budget. IncludeThoughts asks for the
// reasoning to be returned rather than only billed.
type thinkingConfig struct {
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
	ThinkingBudget  *int64 `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
}

// safetySetting overrides a content filter threshold.
type safetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

// generateResponse is a completion, and also one streaming chunk: the
// streaming endpoint emits partial values of this same shape.
type generateResponse struct {
	Candidates     []candidate     `json:"candidates"`
	UsageMetadata  *usageMetadata  `json:"usageMetadata"`
	ModelVersion   string          `json:"modelVersion"`
	ResponseID     string          `json:"responseId"`
	PromptFeedback *promptFeedback `json:"promptFeedback"`
}

// candidate is one generated alternative.
type candidate struct {
	Content           *content           `json:"content"`
	FinishReason      string             `json:"finishReason"`
	Index             int                `json:"index"`
	GroundingMetadata *groundingMetadata `json:"groundingMetadata"`
}

// promptFeedback reports that the prompt itself was blocked.
type promptFeedback struct {
	BlockReason string `json:"blockReason"`
}

// groundingMetadata carries the sources behind a grounded answer.
type groundingMetadata struct {
	GroundingChunks []groundingChunk `json:"groundingChunks"`
}

// groundingChunk is one cited source.
type groundingChunk struct {
	Web *struct {
		URI   string `json:"uri"`
		Title string `json:"title"`
	} `json:"web"`
}

// usageMetadata is Google's token accounting.
//
// CandidatesTokenCount excludes thinking tokens, so the output total is the
// sum of the two. PromptTokenCount includes cached tokens.
type usageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	TotalTokenCount         int64 `json:"totalTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
}
