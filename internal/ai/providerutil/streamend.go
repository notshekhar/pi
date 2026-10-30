package providerutil

import (
	"errors"

	"github.com/notshekhar/pi/internal/ai/provider"
)

// ErrStreamEndedEarly is a stream that closed before the provider's
// completion signal and before any output: nothing to keep, so it fails
// rather than finishing as an empty answer.
var ErrStreamEndedEarly = errors.New("the provider's stream ended before it sent anything")

// StreamEnd watches a provider stream's parts so its end can be judged the
// AI SDK's way (ai@7 "interrupted"), and so a stream that stops mid-block
// can still close what it opened.
type StreamEnd struct {
	output, errored bool
	open            []provider.StreamPart // the end owed for each open block, in opening order
}

// Observe notes one part on its way to the consumer.
func (e *StreamEnd) Observe(part provider.StreamPart) {
	e.track(part)
	switch p := part.(type) {
	case provider.TextDelta:
		e.output = e.output || p.Delta != ""
	case provider.ReasoningDelta:
		e.output = e.output || p.Delta != ""
	case provider.ToolInputDelta:
		e.output = e.output || p.Delta != ""
	case provider.ToolCall:
		e.output = true
	case provider.ErrorPart:
		e.errored = true
	}
}

// Reason is the finish reason for a stream that has ended. completed is
// whether the provider sent its completion signal (Anthropic's message_stop,
// a chat chunk's finish_reason, Bedrock's messageStop, a Gemini
// finishReason); reason is what it said.
//
// Without that signal the output may be cut short: a stream that produced
// some finishes "interrupted", and one that produced none returns
// ErrStreamEndedEarly to report. A stream that already reported an error
// finishes "error". A completed stream keeps its reason.
func (e *StreamEnd) Reason(completed bool, reason provider.FinishReason) (provider.FinishReason, error) {
	switch {
	case completed:
		return reason, nil
	case e.errored:
		if reason.Unified == "" {
			reason.Unified = provider.FinishError
		}
		return reason, nil
	case e.output:
		return provider.FinishReason{Unified: provider.FinishInterrupted, Raw: reason.Raw}, nil
	}
	return provider.FinishReason{Unified: provider.FinishError}, ErrStreamEndedEarly
}

// Unclosed is the end of every block still open, in the order they opened:
// what a stream that stopped mid-text, mid-thought or mid-arguments owes
// before its Finish, so every start a consumer saw has its end. A tool input
// cut short gets its end and no call — arguments that never finished are
// not a call to run.
func (e *StreamEnd) Unclosed() []provider.StreamPart {
	return append([]provider.StreamPart(nil), e.open...)
}

// track keeps the open blocks: a start owes an end, an end pays it.
func (e *StreamEnd) track(part provider.StreamPart) {
	switch p := part.(type) {
	case provider.TextStart:
		e.open = append(e.open, provider.TextEnd{ID: p.ID})
	case provider.ReasoningStart:
		e.open = append(e.open, provider.ReasoningEnd{ID: p.ID})
	case provider.ToolInputStart:
		e.open = append(e.open, provider.ToolInputEnd{ID: p.ID})
	case provider.TextEnd, provider.ReasoningEnd, provider.ToolInputEnd:
		for i, owed := range e.open {
			if sameEnd(owed, part) {
				e.open = append(e.open[:i], e.open[i+1:]...)
				break
			}
		}
	}
}

// sameEnd reports whether two ends close the same block: same kind, same id.
func sameEnd(a, b provider.StreamPart) bool {
	switch x := a.(type) {
	case provider.TextEnd:
		y, ok := b.(provider.TextEnd)
		return ok && x.ID == y.ID
	case provider.ReasoningEnd:
		y, ok := b.(provider.ReasoningEnd)
		return ok && x.ID == y.ID
	case provider.ToolInputEnd:
		y, ok := b.(provider.ToolInputEnd)
		return ok && x.ID == y.ID
	}
	return false
}
