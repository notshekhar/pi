package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/ai/providerutil"
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
			ctx:         ctx,
			out:         out,
			raw:         opts.IncludeRawChunks,
			toolCalls:   map[int]*pendingToolCall{},
			finish:      provider.FinishReason{Unified: provider.FinishOther},
			keepDetails: m.provider.reasoningDetails,
			key:         m.provider.optionsKey,
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
	// finished is a finish_reason seen: the provider's completion signal.
	finished bool
	// details accumulates OpenRouter's reasoning_details for the step, when
	// the provider keeps them; key names the metadata they ride under.
	keepDetails bool
	details     []any
	key         string
	end         providerutil.StreamEnd
}

// emit sends a part, giving up if the consumer has gone away.
func (s *streamState) emit(part provider.StreamPart) bool {
	s.end.Observe(part)
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
	// Without a finish_reason what streamed may be cut short (the AI SDK's
	// openai chat model: interrupted).
	if s.ctx.Err() == nil {
		reason, err := s.end.Reason(s.finished, s.finish)
		if err != nil {
			s.emit(provider.ErrorPart{Err: fmt.Errorf("openaicompat: %w", err)})
		}
		s.finish = reason
	}
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
			s.finished = true
		}
	}
}

// handleDelta processes the incremental content of one choice.
func (s *streamState) handleDelta(delta *apiMessageOut) {
	if s.keepDetails && len(delta.ReasoningDetails) > 0 {
		for _, d := range delta.ReasoningDetails {
			s.addDetail(d)
		}
		// Encrypted reasoning has no text, but its block must exist to carry
		// the details back.
		if !s.reasoningOpen && !s.textOpen {
			s.reasoningOpen = true
			s.emit(provider.ReasoningStart{ID: reasoningBlockID})
		}
	}
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
			s.emit(provider.ReasoningEnd{ID: reasoningBlockID, ProviderMetadata: s.detailsMeta()})
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
		s.emit(provider.ReasoningEnd{ID: reasoningBlockID, ProviderMetadata: s.detailsMeta()})
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

// addDetail folds one reasoning_details chunk in, as the OpenRouter SDK
// does: consecutive text chunks concatenate, keeping the signature and
// format whichever chunk carried them.
func (s *streamState) addDetail(d map[string]any) {
	if d["type"] == "reasoning.text" && len(s.details) > 0 {
		if last, _ := s.details[len(s.details)-1].(map[string]any); last != nil && last["type"] == "reasoning.text" {
			text, _ := last["text"].(string)
			more, _ := d["text"].(string)
			last["text"] = text + more
			for _, k := range []string{"signature", "format"} {
				if v, ok := last[k].(string); !ok || v == "" {
					if dv, ok := d[k]; ok && dv != nil {
						last[k] = dv
					}
				}
			}
			return
		}
	}
	copied := make(map[string]any, len(d))
	for k, v := range d {
		copied[k] = v
	}
	s.details = append(s.details, copied)
}

// detailsMeta carries the step's reasoning_details on its reasoning part.
func (s *streamState) detailsMeta() provider.ProviderMetadata {
	if !s.keepDetails || len(s.details) == 0 {
		return nil
	}
	return provider.ProviderMetadata{s.key: {"reasoning_details": s.details}}
}
