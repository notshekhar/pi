package ai

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
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

// streamAccumulator rebuilds a step's completed content from its stream parts.
//
// Text and reasoning arrive as deltas keyed by block id, and blocks can
// interleave, so deltas are collected per id and assembled in the order the
// blocks opened.
type streamAccumulator struct {
	blocks []*accumulatedBlock
	byID   map[string]*accumulatedBlock

	content      []provider.Content
	finishReason provider.FinishReason
	usage        provider.Usage
	warnings     []provider.Warning
	err          error
}

// accumulatedBlock is one open text or reasoning block.
type accumulatedBlock struct {
	id   string
	kind string
	text strings.Builder
	// order is the position the block opened at, used to keep interleaved
	// blocks in the order the model produced them.
	order    int
	metadata provider.ProviderMetadata
	closed   bool
}

// add folds one part into the accumulator.
func (a *streamAccumulator) add(part StreamPart) {
	switch v := part.(type) {
	case provider.StreamStart:
		a.warnings = append(a.warnings, v.Warnings...)

	case provider.TextStart:
		a.open(v.ID, "text", v.ProviderMetadata)
	case provider.TextDelta:
		a.appendDelta(v.ID, "text", v.Delta, v.ProviderMetadata)
	case provider.TextEnd:
		a.close(v.ID, v.ProviderMetadata)

	case provider.ReasoningStart:
		a.open(v.ID, "reasoning", v.ProviderMetadata)
	case provider.ReasoningDelta:
		a.appendDelta(v.ID, "reasoning", v.Delta, v.ProviderMetadata)
	case provider.ReasoningEnd:
		a.close(v.ID, v.ProviderMetadata)

	case provider.ToolCall:
		a.content = append(a.content, v)
	case provider.ToolResult:
		a.content = append(a.content, v)
	case provider.File:
		a.content = append(a.content, v)
	case provider.Source:
		a.content = append(a.content, v)

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

// open starts a block, tolerating a provider that emits deltas without a
// matching start.
func (a *streamAccumulator) open(id, kind string, md provider.ProviderMetadata) *accumulatedBlock {
	if a.byID == nil {
		a.byID = map[string]*accumulatedBlock{}
	}
	if b, ok := a.byID[id]; ok {
		mergeMetadata(&b.metadata, md)
		return b
	}
	b := &accumulatedBlock{id: id, kind: kind, order: len(a.blocks), metadata: md}
	a.byID[id] = b
	a.blocks = append(a.blocks, b)
	return b
}

// appendDelta adds text to a block.
func (a *streamAccumulator) appendDelta(id, kind, delta string, md provider.ProviderMetadata) {
	b := a.open(id, kind, nil)
	b.text.WriteString(delta)
	// A delta can carry metadata of its own: Anthropic's reasoning signature
	// arrives on an otherwise empty delta.
	mergeMetadata(&b.metadata, md)
}

// close marks a block finished.
func (a *streamAccumulator) close(id string, md provider.ProviderMetadata) {
	b, ok := a.byID[id]
	if !ok {
		return
	}
	b.closed = true
	mergeMetadata(&b.metadata, md)
}

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
	blocks := make([]*accumulatedBlock, len(a.blocks))
	copy(blocks, a.blocks)
	sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].order < blocks[j].order })

	var text, reasoning strings.Builder

	// Text and reasoning blocks come first in the content list, followed by
	// the tool calls and other parts collected as they arrived. Providers
	// finish their prose before requesting tools, so this preserves the real
	// order without needing a global sequence number.
	content := make([]provider.Content, 0, len(blocks)+len(a.content))
	for _, b := range blocks {
		switch b.kind {
		case "text":
			text.WriteString(b.text.String())
			content = append(content, provider.Text{
				Text:             b.text.String(),
				ProviderMetadata: b.metadata,
			})
		case "reasoning":
			reasoning.WriteString(b.text.String())
			content = append(content, provider.Reasoning{
				Text:             b.text.String(),
				ProviderMetadata: b.metadata,
			})
		}
	}
	content = append(content, a.content...)

	return &stepOutput{
		content:      content,
		text:         text.String(),
		reasoning:    reasoning.String(),
		finishReason: a.finishReason,
		usage:        a.usage,
		warnings:     a.warnings,
	}
}
