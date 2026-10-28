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
// AI SDK's way (ai@7 "interrupted").
type StreamEnd struct {
	output, errored bool
}

// Observe notes one part on its way to the consumer.
func (e *StreamEnd) Observe(part provider.StreamPart) {
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
