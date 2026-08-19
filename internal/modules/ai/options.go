package ai

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// ErrAbortRun, when wrapped by an error a tool returns, stops the whole run
// instead of reporting the failure to the model. Use it for conditions the
// model cannot recover from, such as the user cancelling.
var ErrAbortRun = errors.New("pi: run aborted")

// defaultMaxSteps bounds an agent loop that never stops calling tools. It is
// a safety net, not a target: a run that hits it has usually gone wrong.
const defaultMaxSteps = 16

// Options configures GenerateText and StreamText.
type Options struct {
	// Model is required.
	Model provider.LanguageModel

	// System is a convenience for a leading system message. It is prepended
	// to Messages, so both can be used together.
	System string

	// Messages is the conversation so far. Build entries with UserText,
	// AssistantText and friends, or carry forward the Messages from a
	// previous Result.
	Messages []provider.Message

	// Tools the model may call. Tools returned in the model's output are
	// executed automatically and the run continues.
	Tools []Tool

	// ProviderTools are tools the provider hosts and runs itself, built by
	// the provider packages: anthropic.WebSearch, google.Search and so on.
	//
	// They are separate from Tools because nothing local ever runs for them —
	// the call and its result arrive together inside the model's turn — so a
	// run does not step for one, and their results are not fed back as a tool
	// message.
	ProviderTools []provider.ProviderTool

	// ApproveTool is asked before a tool call that needs approval runs. It is
	// required as soon as anything needs approval: a run fails with
	// ErrNoApprover rather than executing an unapproved call because the
	// program forgot to wire one up.
	//
	// Requests are made one at a time in the order the model produced them, so
	// a person answering them sees a coherent sequence. It is called on the
	// goroutine driving the run and may block for as long as it needs; cancel
	// the context to abandon the wait.
	ApproveTool func(ctx context.Context, call ToolCall) (ApprovalDecision, error)

	// NeedsApproval is the caller's policy for which calls need approval, on
	// top of what the tools themselves demand through ApprovalTool. Either can
	// require approval and neither can waive the other's requirement.
	NeedsApproval func(ctx context.Context, call ToolCall) (bool, error)

	// RepairToolCall gets a second chance at a tool call the model got wrong —
	// unparseable arguments, or a name that matches no tool. Nil reports the
	// failure to the model instead, which costs a whole extra step.
	RepairToolCall RepairToolCallFunc

	// ToolChoice constrains tool selection. Nil lets the model decide.
	//
	// Forcing a tool applies to every step, so a forced choice combined with
	// automatic tool execution will loop until MaxSteps. Force a tool only
	// with MaxSteps of 1, or clear it after the first step.
	ToolChoice *provider.ToolChoice

	// ActiveTools restricts which of Tools the model is offered, by name. Nil
	// offers all of them. PrepareStep can narrow or widen it per step.
	ActiveTools []string

	// PrepareStep is called before each step and can change the model, the
	// active tools, the tool choice, the prompt and the sampling settings for
	// that step alone. Nil leaves every step configured the same.
	PrepareStep PrepareStepFunc

	// MaxSteps caps how many times the model runs. One step is one model call
	// plus any tools it asked for. Zero means defaultMaxSteps.
	MaxSteps int

	// StopWhen ends the run early. It is consulted after each completed step,
	// before deciding whether to call the model again. Returning true stops
	// the run even if the model asked for more tools.
	StopWhen func(step Step) bool

	// OnStepFinish is called after each step completes, for logging and
	// progress reporting. It runs on the goroutine driving the run, so a slow
	// callback slows the run.
	OnStepFinish func(step Step)

	MaxOutputTokens *int64
	Temperature     *float64
	TopP            *float64
	TopK            *int64
	Seed            *int64
	StopSequences   []string

	// Reasoning sets the thinking effort. Empty leaves it to the provider.
	Reasoning provider.ReasoningEffort

	// Headers are extra HTTP headers for every call in the run.
	Headers provider.Headers

	// ProviderOptions carries provider-specific settings, keyed by provider id.
	ProviderOptions provider.ProviderOptions

	// IncludeRawChunks makes StreamText forward the provider's raw events.
	IncludeRawChunks bool

	// Downloader fetches file URLs the model cannot fetch itself. Nil uses an
	// http.DefaultClient-backed one. Supply your own to add authentication, a
	// proxy, or a longer-lived cache.
	Downloader Downloader

	// DisableURLDownload leaves file URLs alone, passing them to the provider
	// as they are. Set it when every URL is one the provider fetches, or when
	// the process must not make outbound requests of its own.
	DisableURLDownload bool

	// ObjectName and ObjectDescription label the schema for GenerateObject and
	// StreamObject. Providers show them to the model, so a description is
	// worth setting when the field names alone are ambiguous. Both are ignored
	// by GenerateText and StreamText.
	ObjectName        string
	ObjectDescription string

	// responseFormat is set by the object entry points. It is unexported
	// because asking for JSON without decoding it is GenerateObject's job, not
	// a mode of GenerateText.
	responseFormat *provider.ResponseFormat
}

// maxSteps resolves the configured step cap.
func (o *Options) maxSteps() int {
	if o.MaxSteps > 0 {
		return o.MaxSteps
	}
	return defaultMaxSteps
}

// validate reports configuration that cannot produce a run.
func (o *Options) validate() error {
	if o.Model == nil {
		return errors.New("pi: Options.Model is required")
	}
	return nil
}

// callOptions renders the per-step model call options.
func (o *Options) callOptions(prompt provider.Prompt, stream bool) provider.CallOptions {
	return provider.CallOptions{
		Prompt:           prompt,
		MaxOutputTokens:  o.MaxOutputTokens,
		Temperature:      o.Temperature,
		TopP:             o.TopP,
		TopK:             o.TopK,
		Seed:             o.Seed,
		StopSequences:    o.StopSequences,
		Tools:            specTools(o.Tools, o.ProviderTools),
		ToolChoice:       o.ToolChoice,
		ResponseFormat:   o.responseFormat,
		Reasoning:        o.Reasoning,
		Headers:          o.Headers,
		ProviderOptions:  o.ProviderOptions,
		IncludeRawChunks: stream && o.IncludeRawChunks,
	}
}

// ToolCall is a tool invocation the model requested.
type ToolCall struct {
	ToolCallID string
	ToolName   string
	// Input is the raw JSON arguments, exactly as the model produced them.
	Input json.RawMessage
}

// ToolExecution is the outcome of running one tool.
type ToolExecution struct {
	ToolCall

	// Result is what was reported to the model.
	Result ToolResult

	// Err is set when the tool returned an error. The error is also reported
	// to the model as a failure, so a run continues unless it wraps
	// ErrAbortRun.
	Err error

	// Denied reports that the call was refused at approval and never ran.
	// Result carries the refusal that was reported to the model.
	Denied bool
}

// Step is one model call plus any tools it triggered.
type Step struct {
	// Number is the zero-based index of the step within the run.
	Number int

	// Text is the text the model produced in this step.
	Text string

	// Reasoning is the thinking the model produced, if any.
	Reasoning string

	// Content is the model's full ordered output, including the parts that
	// Text and Reasoning flatten.
	Content []provider.Content

	// ToolCalls is what the model asked for, and ToolExecutions what came
	// back. They correspond by index.
	ToolCalls      []ToolCall
	ToolExecutions []ToolExecution

	FinishReason provider.FinishReason
	Usage        provider.Usage
	Warnings     []provider.Warning

	// Messages are the messages this step appended to the conversation: an
	// assistant message, plus a tool message when tools ran.
	Messages []provider.Message
}

// Result is the outcome of a completed run.
type Result struct {
	// Text is the text from the final step, which is the model's answer.
	// Text from intermediate steps is in Steps.
	Text string

	// Reasoning is the thinking from the final step.
	Reasoning string

	// Steps is every step, in order.
	Steps []Step

	// FinishReason is the reason the final step ended.
	FinishReason provider.FinishReason

	// Usage is the total across every step.
	Usage provider.Usage

	// Warnings is the union of the warnings from every step.
	Warnings []provider.Warning

	// Messages is the full conversation including everything the run added,
	// ready to pass back as Options.Messages for the next turn.
	Messages []provider.Message
}

// Core-level stream parts. These join the provider's parts on the stream that
// StreamText returns, which is why they carry the same discriminator method.

// StepStart marks the beginning of a step.
type StepStart struct {
	Step int
}

// StepFinish marks the end of a step, after any tools have run.
type StepFinish struct {
	Step         int
	FinishReason provider.FinishReason
	Usage        provider.Usage
}

// ToolExecuted reports that a tool the model called has finished running.
// It arrives after the provider's ToolCall part for the same call id.
type ToolExecuted struct {
	Execution ToolExecution
}

// RunFinish is the last part of a run, carrying the totals.
type RunFinish struct {
	Steps        int
	FinishReason provider.FinishReason
	Usage        provider.Usage
}

// StreamPartType implements provider.StreamPart.
func (StepStart) StreamPartType() string { return "step-start" }

// StreamPartType implements provider.StreamPart.
func (StepFinish) StreamPartType() string { return "step-finish" }

// StreamPartType implements provider.StreamPart.
func (ToolExecuted) StreamPartType() string { return "tool-executed" }

// StreamPartType implements provider.StreamPart.
func (RunFinish) StreamPartType() string { return "run-finish" }

// StreamPart is the element type of the stream StreamText returns. It is the
// provider's part type, widened with the core parts above.
type StreamPart = provider.StreamPart

// buildPrompt assembles the prompt for the next model call.
func buildPrompt(system string, messages []provider.Message) provider.Prompt {
	prompt := make(provider.Prompt, 0, len(messages)+1)
	if system != "" {
		prompt = append(prompt, provider.SystemMessage{Content: system})
	}
	return append(prompt, messages...)
}

// addUsage accumulates token counts across steps.
//
// Nil means "not reported", which is not the same as zero, so a nil field
// stays nil until some step reports a figure for it.
func addUsage(total, step provider.Usage) provider.Usage {
	total.InputTokens.Total = addTokens(total.InputTokens.Total, step.InputTokens.Total)
	total.InputTokens.NoCache = addTokens(total.InputTokens.NoCache, step.InputTokens.NoCache)
	total.InputTokens.CacheRead = addTokens(total.InputTokens.CacheRead, step.InputTokens.CacheRead)
	total.InputTokens.CacheWrite = addTokens(total.InputTokens.CacheWrite, step.InputTokens.CacheWrite)

	total.OutputTokens.Total = addTokens(total.OutputTokens.Total, step.OutputTokens.Total)
	total.OutputTokens.Text = addTokens(total.OutputTokens.Text, step.OutputTokens.Text)
	total.OutputTokens.Reasoning = addTokens(total.OutputTokens.Reasoning, step.OutputTokens.Reasoning)

	// Raw is per-call, so the most recent step's payload is kept rather than
	// merged into something that describes no single call.
	if step.Raw != nil {
		total.Raw = step.Raw
	}
	return total
}

// addTokens sums two optional counts, preserving nil when neither reported.
func addTokens(a, b *int64) *int64 {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	sum := *a + *b
	return &sum
}

// newToolCallID synthesises an id for a provider that did not supply one.
func newToolCallID() string {
	return providerutil.GenerateID("call", 16)
}
