package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// runner carries the state of one GenerateText or StreamText call.
type runner struct {
	opts  *Options
	tools toolSet

	// messages grows as the run proceeds, starting from the caller's history.
	messages []provider.Message
	steps    []Step
	usage    provider.Usage
	warnings []provider.Warning

	// downloader fetches file URLs the model cannot fetch itself. It is
	// per-run and caching, so the same attachment replayed on every step is
	// downloaded once.
	downloader Downloader
}

// newRunner validates options and prepares the run.
func newRunner(opts *Options) (*runner, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	tools, err := newToolSet(opts.Tools)
	if err != nil {
		return nil, err
	}

	// Copy the caller's history: a run appends to it, and mutating the
	// caller's slice would corrupt a conversation they are still holding.
	messages := make([]provider.Message, len(opts.Messages))
	copy(messages, opts.Messages)

	return &runner{
		opts:       opts,
		tools:      tools,
		messages:   messages,
		downloader: CachingDownloader(opts.Downloader),
	}, nil
}

// prompt returns the prompt for a step, with any file URL the step's model
// cannot fetch itself replaced by the downloaded bytes.
func (r *runner) prompt(ctx context.Context, plan stepPlan) (provider.Prompt, error) {
	prompt := plan.prompt(r)
	if r.opts.DisableURLDownload {
		return prompt, nil
	}
	return resolveFileURLs(ctx, plan.model, prompt, r.downloader)
}

// stepOutput is what one model call produced, before tools have run.
type stepOutput struct {
	content      []provider.Content
	text         string
	reasoning    string
	finishReason provider.FinishReason
	usage        provider.Usage
	warnings     []provider.Warning
}

// toolCalls extracts the calls the model requested.
func (o *stepOutput) toolCalls() []ToolCall {
	var calls []ToolCall
	for _, c := range o.content {
		call, ok := c.(provider.ToolCall)
		if !ok || call.ProviderExecuted {
			// A provider-executed call has already run server-side; running
			// it again locally would duplicate its effects.
			continue
		}
		id := call.ToolCallID
		if id == "" {
			id = newToolCallID()
		}
		input := call.Input
		if input == "" {
			input = "{}"
		}
		calls = append(calls, ToolCall{
			ToolCallID: id,
			ToolName:   call.ToolName,
			Input:      json.RawMessage(input),
		})
	}
	return calls
}

// assistantMessage renders the model's output as a message to replay next turn.
//
// Reasoning keeps its provider metadata because that is where the signature
// lives, and Anthropic rejects replayed reasoning that has lost it.
func (o *stepOutput) assistantMessage() provider.AssistantMessage {
	parts := make([]provider.AssistantPart, 0, len(o.content))

	for _, c := range o.content {
		switch v := c.(type) {
		case provider.Text:
			if v.Text == "" {
				continue
			}
			parts = append(parts, provider.TextPart{
				Text:            v.Text,
				ProviderOptions: metadataAsOptions(v.ProviderMetadata),
			})

		case provider.Reasoning:
			parts = append(parts, provider.ReasoningPart{
				Text:            v.Text,
				ProviderOptions: metadataAsOptions(v.ProviderMetadata),
			})

		case provider.ToolCall:
			// Input is the model's raw JSON; decode it so the replayed part
			// carries a value rather than a string containing JSON.
			var decoded any
			if err := json.Unmarshal([]byte(v.Input), &decoded); err != nil {
				decoded = v.Input
			}
			parts = append(parts, provider.ToolCallPart{
				ToolCallID:       v.ToolCallID,
				ToolName:         v.ToolName,
				Input:            decoded,
				ProviderExecuted: v.ProviderExecuted,
				ProviderOptions:  metadataAsOptions(v.ProviderMetadata),
			})

		case provider.ToolResult:
			// The result of a tool the provider ran itself. It belongs in the
			// assistant turn, and has to be replayed verbatim: providers
			// validate these payloads against the call they answered.
			parts = append(parts, provider.ToolResultPart{
				ToolCallID:      v.ToolCallID,
				ToolName:        v.ToolName,
				Output:          provider.ToolOutputJSON{Value: v.Result},
				ProviderOptions: metadataAsOptions(v.ProviderMetadata),
			})

		case provider.File:
			parts = append(parts, provider.FilePart{
				Data:            v.Data,
				MediaType:       v.MediaType,
				ProviderOptions: metadataAsOptions(v.ProviderMetadata),
			})
		}
	}

	return provider.AssistantMessage{Content: parts}
}

// metadataAsOptions converts metadata coming out of a provider into the
// options going back in, which is how a signature survives a round trip.
func metadataAsOptions(md provider.ProviderMetadata) provider.ProviderOptions {
	if len(md) == 0 {
		return nil
	}
	return provider.ProviderOptions(md)
}

// executeTools runs the model's tool calls and returns their outcomes in the
// order the model asked for them.
//
// Calls run concurrently: an agent step routinely reads several files at once,
// and running those in series would make every step as slow as the sum of its
// tools. Results are reassembled in order so that a run is reproducible.
func (r *runner) executeTools(ctx context.Context, plan stepPlan, calls []ToolCall, denials map[string]ToolExecution, onDone func(ToolExecution)) ([]ToolExecution, error) {
	if len(calls) == 0 {
		return nil, nil
	}

	executions := make([]ToolExecution, len(calls))

	// A tool that aborts the run cancels its siblings, since their results
	// would be discarded anyway.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		abortErr error
	)

	for i, call := range calls {
		// A refused call never runs, but still reports back so the model
		// learns it was refused rather than that it silently vanished.
		if denied, ok := denials[call.ToolCallID]; ok {
			executions[i] = denied
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			exec := r.executeTool(ctx, plan, call)

			mu.Lock()
			executions[i] = exec
			if exec.Err != nil && errors.Is(exec.Err, ErrAbortRun) && abortErr == nil {
				abortErr = exec.Err
				cancel()
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	if abortErr != nil {
		return executions, abortErr
	}

	// Notify in call order rather than completion order, so a transcript of
	// the run reads the same every time.
	if onDone != nil {
		for _, exec := range executions {
			onDone(exec)
		}
	}
	return executions, nil
}

// executeTool runs one tool call, converting a failure into a result the model
// can read.
// The result is named so that the deferred recover below can replace it; with
// an unnamed result the recovered panic would be discarded.
func (r *runner) executeTool(ctx context.Context, plan stepPlan, call ToolCall) (exec ToolExecution) {
	exec = ToolExecution{ToolCall: call}
	ctx = withToolCallID(ctx, call.ToolCallID)

	// Dispatch goes through the step's tools, not the run's: a tool left out
	// of the active set must be unreachable, not merely unadvertised.
	tool, ok := plan.dispatch()[call.ToolName]
	if !ok {
		// The model called something it was not offered — a hallucination, or
		// a name carried over from replayed history. Telling it so is more
		// useful than failing the run: it can pick a real one on the next step.
		exec.Result = ToolErrorf("unknown tool %q; available tools: %s", call.ToolName, plan.toolNames())
		exec.Err = fmt.Errorf("pi: model called unknown tool %q", call.ToolName)
		return exec
	}

	// A panicking tool must not take the whole agent down: a CLI agent runs
	// arbitrary user-supplied tools, and one bad index should be recoverable.
	defer func() {
		if p := recover(); p != nil {
			exec.Result = ToolErrorf("tool %s panicked: %v", call.ToolName, p)
			exec.Err = fmt.Errorf("pi: tool %s panicked: %v", call.ToolName, p)
		}
	}()

	result, err := tool.Execute(ctx, call.Input)
	if err != nil {
		exec.Err = err
		if errors.Is(err, ErrAbortRun) {
			return exec
		}
		// Report the failure to the model unless the tool already supplied a
		// result to report instead.
		if result == (ToolResult{}) {
			result = ToolError(err.Error())
		}
	}

	exec.Result = result
	return exec
}

// joinComma joins names for display.
func joinComma(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

// toolMessage renders tool outcomes as a message for the next turn.
func toolMessage(executions []ToolExecution) provider.ToolMessage {
	parts := make([]provider.ToolPart, 0, len(executions))
	for _, exec := range executions {
		parts = append(parts, provider.ToolResultPart{
			ToolCallID: exec.ToolCallID,
			ToolName:   exec.ToolName,
			Output:     exec.Result.Output(),
		})
	}
	return provider.ToolMessage{Content: parts}
}

// recordStep folds a completed step into the run state.
func (r *runner) recordStep(step Step) {
	r.steps = append(r.steps, step)
	r.usage = addUsage(r.usage, step.Usage)
	r.warnings = append(r.warnings, step.Warnings...)
	r.messages = append(r.messages, step.Messages...)

	if r.opts.OnStepFinish != nil {
		r.opts.OnStepFinish(step)
	}
}

// shouldContinue reports whether the run should call the model again.
func (r *runner) shouldContinue(step Step) bool {
	// Nothing to feed back means there is nothing for another call to react to.
	if len(step.ToolExecutions) == 0 {
		return false
	}
	if len(r.steps) >= r.opts.maxSteps() {
		return false
	}
	if r.opts.StopWhen != nil && r.opts.StopWhen(step) {
		return false
	}
	return true
}

// result assembles the final Result from the recorded steps.
func (r *runner) result() *Result {
	res := &Result{
		Steps:    r.steps,
		Usage:    r.usage,
		Warnings: r.warnings,
		Messages: r.messages,
	}
	if n := len(r.steps); n > 0 {
		last := r.steps[n-1]
		res.Text = last.Text
		res.Reasoning = last.Reasoning
		res.FinishReason = last.FinishReason
	}
	return res
}
