package ai

import (
	"context"
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/ai/provider"
)

// streamBufferSize decouples the run from the consumer so that a slow renderer
// does not stall the HTTP read.
const streamBufferSize = 64

// StreamResult is a running StreamText call.
type StreamResult struct {
	// Stream carries the run's events. It is closed when the run ends.
	//
	// The caller must drain it or cancel the context. Abandoning it without
	// either blocks the run's goroutine and leaks the provider connection.
	Stream <-chan StreamPart

	done   chan struct{}
	mu     sync.Mutex
	result *Result
	err    error
}

// Final blocks until the run has finished and returns its aggregate result.
//
// It must be called after the Stream has been drained: the run only completes
// once its events have been consumed, so calling Final while events are
// pending deadlocks.
func (r *StreamResult) Final() (*Result, error) {
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result, r.err
}

// StreamText runs the model and streams its output, executing any tools it
// calls and stepping until it stops asking for them.
//
// The returned Stream carries the provider's parts (TextDelta, ToolCall,
// ReasoningDelta and so on) interleaved with the core's own StepStart,
// StepFinish, ToolExecuted and RunFinish parts. Every part carries a
// StreamPartType, so a consumer can either type-switch or dispatch on the
// string.
//
// Errors that end the run arrive as provider.ErrorPart on the stream and are
// also returned by Final; setup errors are returned directly.
func StreamText(ctx context.Context, opts Options) (*StreamResult, error) {
	r, err := newRunner(&opts)
	if err != nil {
		return nil, err
	}

	out := make(chan StreamPart, streamBufferSize)
	result := &StreamResult{Stream: out, done: make(chan struct{})}

	go func() {
		defer close(out)
		defer close(result.done)

		runErr := r.streamRun(ctx, out)

		result.mu.Lock()
		result.result = r.result()
		result.err = runErr
		result.mu.Unlock()
	}()

	return result, nil
}

// streamRun drives the agent loop, forwarding parts as they arrive.
func (r *runner) streamRun(ctx context.Context, out chan<- StreamPart) error {
	emit := func(part StreamPart) bool {
		select {
		case out <- part:
			return true
		case <-ctx.Done():
			return false
		}
	}

	for {
		stepNumber := len(r.steps)
		emit(StepStart{Step: stepNumber})

		plan, err := r.planStep(ctx)
		if err != nil {
			emit(provider.ErrorPart{Err: err})
			return err
		}

		stepOut, err := r.streamStep(ctx, plan, emit)
		if err != nil {
			emit(provider.ErrorPart{Err: err})
			return err
		}

		stepOut.withholdUnfinishedCalls()
		calls, err := r.repairCalls(ctx, plan, stepOut.toolCalls())
		if err != nil {
			emit(provider.ErrorPart{Err: err})
			return err
		}

		step := Step{
			Number:       stepNumber,
			Text:         stepOut.text,
			Reasoning:    stepOut.reasoning,
			Content:      stepOut.content,
			ToolCalls:    calls,
			FinishReason: stepOut.finishReason,
			Usage:        stepOut.usage,
			Warnings:     stepOut.warnings,
			Messages:     []provider.Message{stepOut.assistantMessage()},
		}

		// `resolved` may differ from `calls`: an approver can rewrite a
		// call's input. The step keeps the ORIGINAL — see UpdatedInput.
		resolved, denials, err := r.resolveApprovals(ctx, calls, emit)
		if err != nil {
			emit(provider.ErrorPart{Err: err})
			return err
		}

		executions, abortErr := r.executeTools(ctx, plan, resolved, denials, func(exec ToolExecution) {
			emit(ToolExecuted{Execution: exec})
		})
		step.ToolExecutions = executions
		if len(executions) > 0 {
			step.Messages = append(step.Messages, toolMessage(executions))
		}

		r.recordStep(step)
		emit(StepFinish{
			Step:         stepNumber,
			FinishReason: step.FinishReason,
			Usage:        step.Usage,
		})

		if abortErr != nil {
			emit(provider.ErrorPart{Err: abortErr})
			return abortErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !r.shouldContinue(step) {
			emit(RunFinish{
				Steps:        len(r.steps),
				FinishReason: step.FinishReason,
				Usage:        r.usage,
			})
			return nil
		}
	}
}

// streamStep performs one streaming model call, forwarding its parts and
// assembling the completed content.
func (r *runner) streamStep(ctx context.Context, plan stepPlan, emit func(StreamPart) bool) (*stepOutput, error) {
	prompt, err := r.prompt(ctx, plan)
	if err != nil {
		return nil, err
	}

	res, err := plan.model.DoStream(ctx, plan.callOptions(r, prompt, true))
	if err != nil {
		return nil, err
	}

	acc := &streamAccumulator{}

	for part := range res.Stream {
		acc.add(part)

		// Finish and StreamStart are folded into the step's own bookkeeping
		// rather than forwarded, so the consumer sees exactly one StepFinish
		// per step instead of two overlapping terminal events.
		switch part.(type) {
		case provider.Finish, provider.StreamStart:
			continue
		}

		if !emit(part) {
			// The consumer is gone. Drain the provider so its goroutine
			// finishes and the connection is released.
			go func() {
				for range res.Stream { //nolint:revive // draining
				}
			}()
			return nil, ctx.Err()
		}
	}

	if acc.err != nil {
		return nil, acc.err
	}
	return acc.output(), nil
}

// streamAccumulator rebuilds a step's completed content from its stream
// parts, the AI SDK's way (stream-language-model-call's upsertTextContentPart):
// content keeps the order the parts arrived in, and a text or reasoning block
// is found by its kind and id only while it is open.
//
// Order is what the next request replays. A step that reasoned, called a
// tool, reasoned again and called another must go back exactly so: the
// Responses API refuses a function call separated from the reasoning item
// before it, and Anthropic's interleaved thinking loses its sequence.
type streamAccumulator struct {
	parts []accumulatedPart
	open  map[blockKey]*accumulatedBlock

	finishReason provider.FinishReason
	usage        provider.Usage
	warnings     []provider.Warning
	err          error
}

// accumulatedPart is one piece of the step's content: a text or reasoning
// block still being filled, or a part that arrived whole.
type accumulatedPart struct {
	block   *accumulatedBlock
	content provider.Content
}

// blockKey names an open block. Text and reasoning ids are separate spaces,
// as they are in the AI SDK: a provider may number both from zero.
type blockKey struct{ kind, id string }

// accumulatedBlock is one text or reasoning block.
type accumulatedBlock struct {
	kind     string
	text     strings.Builder
	metadata provider.ProviderMetadata
}

// add folds one part into the accumulator.
func (a *streamAccumulator) add(part StreamPart) {
	switch v := part.(type) {
	case provider.StreamStart:
		a.warnings = append(a.warnings, v.Warnings...)

	case provider.TextStart:
		a.upsert("text", v.ID, "", v.ProviderMetadata)
	case provider.TextDelta:
		a.upsert("text", v.ID, v.Delta, v.ProviderMetadata)
	case provider.TextEnd:
		a.upsert("text", v.ID, "", v.ProviderMetadata)
		a.end("text", v.ID)

	case provider.ReasoningStart:
		a.upsert("reasoning", v.ID, "", v.ProviderMetadata)
	case provider.ReasoningDelta:
		a.upsert("reasoning", v.ID, v.Delta, v.ProviderMetadata)
	case provider.ReasoningEnd:
		a.upsert("reasoning", v.ID, "", v.ProviderMetadata)
		a.end("reasoning", v.ID)

	case provider.ToolCall, provider.ToolResult, provider.File, provider.Source:
		a.parts = append(a.parts, accumulatedPart{content: v.(provider.Content)})

	case provider.Finish:
		a.finishReason = v.FinishReason
		a.usage = v.Usage

	case provider.ErrorPart:
		// Keep the first error: later ones are usually consequences of it.
		if a.err == nil {
			a.err = v.Err
		}
	}
}

// upsert adds delta and metadata to the open block of that kind and id,
// opening one where the content stands now if there is none: a provider
// that sends a delta, or only an end, without a start still keeps it.
// A delta can carry metadata of its own — Anthropic's reasoning signature
// arrives on an otherwise empty one.
func (a *streamAccumulator) upsert(kind, id, delta string, md provider.ProviderMetadata) {
	key := blockKey{kind, id}
	b, ok := a.open[key]
	if !ok {
		if a.open == nil {
			a.open = map[blockKey]*accumulatedBlock{}
		}
		b = &accumulatedBlock{kind: kind}
		a.open[key] = b
		a.parts = append(a.parts, accumulatedPart{block: b})
	}
	b.text.WriteString(delta)
	mergeMetadata(&b.metadata, md)
}

// end closes a block: its id may now name a new one.
func (a *streamAccumulator) end(kind, id string) { delete(a.open, blockKey{kind, id}) }

// mergeMetadata folds provider metadata into an accumulating map.
func mergeMetadata(dst *provider.ProviderMetadata, src provider.ProviderMetadata) {
	if len(src) == 0 {
		return
	}
	if *dst == nil {
		*dst = provider.ProviderMetadata{}
	}
	for providerKey, fields := range src {
		existing, ok := (*dst)[providerKey]
		if !ok {
			existing = provider.JSONObject{}
			(*dst)[providerKey] = existing
		}
		for k, v := range fields {
			existing[k] = v
		}
	}
}

// output assembles the finished step content.
func (a *streamAccumulator) output() *stepOutput {
	var text, reasoning strings.Builder
	content := make([]provider.Content, 0, len(a.parts))
	for _, p := range a.parts {
		b := p.block
		switch {
		case b == nil:
			content = append(content, p.content)
		case b.kind == "text":
			text.WriteString(b.text.String())
			content = append(content, provider.Text{Text: b.text.String(), ProviderMetadata: b.metadata})
		default:
			reasoning.WriteString(b.text.String())
			content = append(content, provider.Reasoning{Text: b.text.String(), ProviderMetadata: b.metadata})
		}
	}
	return &stepOutput{
		content:      content,
		text:         text.String(),
		reasoning:    reasoning.String(),
		finishReason: a.finishReason,
		usage:        a.usage,
		warnings:     a.warnings,
	}
}
