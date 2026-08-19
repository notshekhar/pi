package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// streamBufferSize decouples the reader goroutine from the consumer.
const streamBufferSize = 64

// textBlockID is the id used for the single text block a chat completion
// produces. The API has no block ids of its own, so one is synthesised.
const (
	textBlockID      = "0"
	reasoningBlockID = "reasoning-0"
)

// DoStream implements provider.LanguageModel.
func (m *languageModel) DoStream(ctx context.Context, opts provider.CallOptions) (*provider.StreamResult, error) {
	req, warnings, err := m.buildRequest(opts, true)
	if err != nil {
		return nil, err
	}
	body, err := marshalRequest(req)
	if err != nil {
		return nil, err
	}

	resp, err := providerutil.Retry(ctx, m.provider.retry, func(ctx context.Context) (io.ReadCloser, error) {
		httpResp, err := m.provider.client.PostJSON(ctx, m.provider.chatPath, json.RawMessage(body), opts.Headers)
		if err != nil {
			return nil, err
		}
		return httpResp.Body, nil
	})
	if err != nil {
		return nil, err
	}

	out := make(chan provider.StreamPart, streamBufferSize)

	go func() {
		defer close(out)
		s := &streamState{
			ctx:       ctx,
			out:       out,
			raw:       opts.IncludeRawChunks,
			toolCalls: map[int]*pendingToolCall{},
			finish:    provider.FinishReason{Unified: provider.FinishOther},
		}
		s.emit(provider.StreamStart{Warnings: warnings})
		s.run(resp)
	}()

	return &provider.StreamResult{
		Stream:  out,
		Request: &provider.RequestInfo{Body: string(body)},
	}, nil
}

// pendingToolCall accumulates a tool call across streaming deltas.
//
// The API streams a call as a series of fragments keyed by index: the id and
// name arrive on the first fragment and the arguments build up over the rest.
type pendingToolCall struct {
	id        string
	name      string
	arguments strings.Builder
	// started records that ToolInputStart has been emitted, which cannot
	// happen until the name is known.
	started bool
}

// streamState carries the bookkeeping for one streaming response.
type streamState struct {
	ctx context.Context
	out chan<- provider.StreamPart
	raw bool

	toolCalls map[int]*pendingToolCall
	// toolOrder preserves the order indices first appeared, so calls are
	// finalised in the order the model produced them.
	toolOrder []int

	textOpen      bool
	reasoningOpen bool

	usage      provider.Usage
	finish     provider.FinishReason
	sentFinish bool
}

// emit sends a part, giving up if the consumer has gone away.
func (s *streamState) emit(part provider.StreamPart) bool {
	select {
	case s.out <- part:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// run consumes the SSE body.
func (s *streamState) run(body io.ReadCloser) {
	err := providerutil.ScanSSE(body, func(ev providerutil.SSEEvent) error {
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		if strings.TrimSpace(ev.Data) == "" {
			return nil
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
			s.emit(provider.ErrorPart{Err: &provider.InvalidResponseError{
				Message:  "could not decode stream chunk",
				Response: ev.Data,
				Cause:    err,
			}})
			return nil
		}

		if s.raw {
			var rawValue any
			if json.Unmarshal([]byte(ev.Data), &rawValue) == nil {
				s.emit(provider.Raw{RawValue: rawValue})
			}
		}

		s.handle(chunk)
		return nil
	})

	if err != nil && !errors.Is(err, context.Canceled) && s.ctx.Err() == nil {
		s.emit(provider.ErrorPart{Err: fmt.Errorf("openaicompat: reading stream: %w", err)})
	}

	s.closeBlocks()
	s.flushToolCalls()
	s.emitFinish()
}

// handle folds one chunk into the stream.
func (s *streamState) handle(chunk streamChunk) {
	if chunk.ID != "" {
		s.emit(provider.ResponseMetadataPart{ResponseMetadata: provider.ResponseMetadata{
			ID: chunk.ID, ModelID: chunk.Model,
		}})
	}

	// A usage-only chunk carries no choices, which is how the final
	// include_usage frame arrives.
	if chunk.Usage != nil {
		s.usage = convertUsage(chunk.Usage)
	}

	for _, c := range chunk.Choices {
		if c.Delta != nil {
			s.handleDelta(c.Delta)
		}
		if c.FinishReason != nil {
			s.finish = mapFinishReason(c.FinishReason)
		}
	}
}

// handleDelta processes the incremental content of one choice.
func (s *streamState) handleDelta(delta *apiMessageOut) {
	if reasoning := delta.reasoningText(); reasoning != "" {
		if !s.reasoningOpen {
			s.reasoningOpen = true
			s.emit(provider.ReasoningStart{ID: reasoningBlockID})
		}
		s.emit(provider.ReasoningDelta{ID: reasoningBlockID, Delta: reasoning})
	}

	if delta.Content != "" {
		// Reasoning always precedes the answer, so the arrival of content
		// closes the reasoning block.
		if s.reasoningOpen {
			s.reasoningOpen = false
			s.emit(provider.ReasoningEnd{ID: reasoningBlockID})
		}
		if !s.textOpen {
			s.textOpen = true
			s.emit(provider.TextStart{ID: textBlockID})
		}
		s.emit(provider.TextDelta{ID: textBlockID, Delta: delta.Content})
	}

	for _, call := range delta.ToolCalls {
		s.handleToolCallDelta(call)
	}
}

// handleToolCallDelta accumulates one tool call fragment.
func (s *streamState) handleToolCallDelta(call apiToolCall) {
	// The index is what ties fragments together. Some gateways omit it when
	// there is only one call in flight, in which case index 0 is implied.
	index := 0
	if call.Index != nil {
		index = *call.Index
	}

	pending, ok := s.toolCalls[index]
	if !ok {
		pending = &pendingToolCall{}
		s.toolCalls[index] = pending
		s.toolOrder = append(s.toolOrder, index)
	}

	if call.ID != "" {
		pending.id = call.ID
	}
	if call.Function.Name != "" {
		pending.name = call.Function.Name
	}

	// The start cannot be announced until the name is known, and the name may
	// arrive on a later fragment than the id.
	if !pending.started && pending.name != "" {
		pending.started = true
		s.emit(provider.ToolInputStart{ID: s.toolCallID(index, pending), ToolName: pending.name})
	}

	if call.Function.Arguments != "" {
		pending.arguments.WriteString(call.Function.Arguments)
		if pending.started {
			s.emit(provider.ToolInputDelta{
				ID:    s.toolCallID(index, pending),
				Delta: call.Function.Arguments,
			})
		}
	}
}

// toolCallID returns the id to report for a call, synthesising one when the
// gateway did not supply it. An id is required to match a result to its call.
func (s *streamState) toolCallID(index int, pending *pendingToolCall) string {
	if pending.id != "" {
		return pending.id
	}
	pending.id = providerutil.GenerateID("call", 12) + "_" + strconv.Itoa(index)
	return pending.id
}

// closeBlocks closes any text or reasoning block still open.
func (s *streamState) closeBlocks() {
	if s.reasoningOpen {
		s.reasoningOpen = false
		s.emit(provider.ReasoningEnd{ID: reasoningBlockID})
	}
	if s.textOpen {
		s.textOpen = false
		s.emit(provider.TextEnd{ID: textBlockID})
	}
}

// flushToolCalls emits the completed tool calls.
//
// The chat API has no per-call terminator, so calls are only complete once the
// stream ends. They are emitted in the order their indices first appeared.
func (s *streamState) flushToolCalls() {
	for _, index := range s.toolOrder {
		pending := s.toolCalls[index]
		if pending.name == "" {
			// A fragment stream that never named a tool cannot be executed.
			s.emit(provider.ErrorPart{Err: &provider.InvalidResponseError{
				Message: fmt.Sprintf("tool call at index %d has no name", index),
			}})
			continue
		}

		id := s.toolCallID(index, pending)
		s.emit(provider.ToolInputEnd{ID: id})

		input := pending.arguments.String()
		if strings.TrimSpace(input) == "" {
			input = "{}"
		}
		s.emit(provider.ToolCall{ToolCallID: id, ToolName: pending.name, Input: input})
	}
}

// emitFinish sends the terminal Finish part, at most once.
func (s *streamState) emitFinish() {
	if s.sentFinish {
		return
	}
	s.sentFinish = true
	s.emit(provider.Finish{Usage: s.usage, FinishReason: s.finish})
}
