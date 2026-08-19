package anthropic

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

// streamBufferSize decouples the provider's reader goroutine from the
// consumer, so a slow renderer does not stall the HTTP read and risk a
// server-side idle timeout.
const streamBufferSize = 64

// DoStream implements provider.LanguageModel.
func (m *languageModel) DoStream(ctx context.Context, opts provider.CallOptions) (*provider.StreamResult, error) {
	built, err := m.buildRequest(opts, true)
	if err != nil {
		return nil, err
	}
	req, warnings := built.body, built.warnings
	headers := built.headers(opts.Headers)

	// Only the connection setup is retried. Once bytes are flowing a failure
	// cannot be retried transparently: part of the response has already been
	// handed to the caller, and Anthropic offers no way to resume.
	resp, err := providerutil.Retry(ctx, m.provider.retry, func(ctx context.Context) (*httpStream, error) {
		httpResp, err := m.client.PostJSON(ctx, "/v1/messages", req, headers)
		if err != nil {
			return nil, err
		}
		return &httpStream{body: httpResp.Body, headers: providerutil.FlattenHeaders(httpResp.Header)}, nil
	})
	if err != nil {
		return nil, err
	}

	out := make(chan provider.StreamPart, streamBufferSize)

	go func() {
		defer close(out)
		s := &streamState{
			model:    m,
			out:      out,
			ctx:      ctx,
			blocks:   map[int]*blockState{},
			raw:      opts.IncludeRawChunks,
			jsonTool: usesJSONTool(opts),
			finish:   provider.FinishReason{Unified: provider.FinishOther},
		}
		s.emit(provider.StreamStart{Warnings: warnings})
		s.run(resp.body)
	}()

	requestBody, _ := json.Marshal(req)

	return &provider.StreamResult{
		Stream:   out,
		Request:  &provider.RequestInfo{Body: string(requestBody)},
		Response: &provider.ResponseInfo{Headers: resp.headers},
	}, nil
}

// httpStream is the response body plus the headers worth reporting.
type httpStream struct {
	body    io.ReadCloser
	headers provider.Headers
}

// blockKind is what a streaming content block turned out to be.
type blockKind int

const (
	blockText blockKind = iota
	blockReasoning
	blockToolCall
	// blockHostedCall is a tool the provider executes itself, which streams
	// like a tool call but must never be run locally.
	blockHostedCall
	blockIgnored
)

// blockState tracks one open content block across its start, deltas and stop.
type blockState struct {
	kind blockKind

	// toolCallID, toolName and input accumulate a tool call, whose arguments
	// arrive as JSON fragments that only parse once concatenated.
	toolCallID string
	toolName   string
	input      strings.Builder

	// serverToolName is the provider's own name for a hosted tool, which
	// differs from the caller's name for the code-execution variants.
	serverToolName string

	// signature accumulates the reasoning signature, which arrives in its own
	// delta after the thinking text.
	signature strings.Builder
	// redactedData is set for redacted_thinking blocks.
	redactedData string
}

// streamState carries the bookkeeping for one streaming response.
type streamState struct {
	model *languageModel
	out   chan<- provider.StreamPart
	ctx   context.Context

	// blocks is keyed by Anthropic's content block index, which is also used
	// as the spec block id.
	blocks map[int]*blockState

	raw bool
	// jsonTool is set when the call emulates structured output, in which case
	// the forced tool's argument fragments are streamed as text deltas.
	jsonTool bool
	usage    provider.Usage
	finish   provider.FinishReason
	// sentFinish guards against emitting Finish twice when message_delta and
	// message_stop both arrive.
	sentFinish bool
}

// emit sends a part, dropping it if the consumer has gone away.
func (s *streamState) emit(part provider.StreamPart) bool {
	select {
	case s.out <- part:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// run consumes the SSE body and translates it into spec stream parts.
func (s *streamState) run(body io.ReadCloser) {
	err := providerutil.ScanSSE(body, func(ev providerutil.SSEEvent) error {
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}

		var event streamEvent
		if err := json.Unmarshal([]byte(ev.Data), &event); err != nil {
			// A single malformed event should not abandon a generation that
			// is otherwise progressing.
			s.emit(provider.ErrorPart{Err: &provider.InvalidResponseError{
				Message:  "could not decode stream event",
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

		// The event type is on the SSE frame and repeated in the payload;
		// prefer the payload since replayed fixtures often carry only that.
		kind := event.Type
		if kind == "" {
			kind = ev.Event
		}
		s.handle(kind, event)
		return nil
	})

	if err != nil && !errors.Is(err, context.Canceled) && s.ctx.Err() == nil {
		s.emit(provider.ErrorPart{Err: fmt.Errorf("anthropic: reading stream: %w", err)})
	}

	// A stream that ends without message_delta still owes the consumer a
	// Finish, or a caller waiting on usage would block forever.
	s.emitFinish()
}

// handle dispatches one decoded event.
func (s *streamState) handle(kind string, event streamEvent) {
	switch kind {
	case "message_start":
		if event.Message != nil {
			s.emit(provider.ResponseMetadataPart{ResponseMetadata: provider.ResponseMetadata{
				ID:      event.Message.ID,
				ModelID: event.Message.Model,
			}})
			// message_start carries the input token counts; message_delta
			// later carries only the output count, so both must be merged.
			s.usage = convertUsage(event.Message.Usage)
		}

	case "content_block_start":
		s.startBlock(event)

	case "content_block_delta":
		s.handleDelta(event)

	case "content_block_stop":
		s.stopBlock(event.Index)

	case "message_delta":
		if event.Delta != nil {
			s.finish = mapStopReason(event.Delta.StopReason)
		}
		if event.Usage != nil {
			s.mergeUsage(*event.Usage)
		}

	case "message_stop":
		s.emitFinish()

	case "error":
		if event.Error != nil {
			s.emit(provider.ErrorPart{Err: &provider.APICallError{
				Message: event.Error.Message,
				URL:     s.model.client.BaseURL + "/v1/messages",
				Data:    event.Error.Type,
			}})
		}

	case "ping":
		// Keep-alive.
	}
}

// startBlock opens a content block.
func (s *streamState) startBlock(event streamEvent) {
	if event.ContentBlock == nil {
		return
	}
	id := strconv.Itoa(event.Index)
	block := &blockState{}
	s.blocks[event.Index] = block

	switch event.ContentBlock.Type {
	case "text":
		block.kind = blockText
		s.emit(provider.TextStart{ID: id})

	case "thinking":
		block.kind = blockReasoning
		s.emit(provider.ReasoningStart{ID: id})

	case "redacted_thinking":
		block.kind = blockReasoning
		block.redactedData = event.ContentBlock.Data
		s.emit(provider.ReasoningStart{
			ID: id,
			ProviderMetadata: provider.ProviderMetadata{
				providerID: {"redactedData": event.ContentBlock.Data},
			},
		})

	case "tool_use":
		if s.jsonTool && event.ContentBlock.Name == jsonToolName {
			// Structured output rides on a forced tool. Presenting it as text
			// keeps the stream identical to a provider with a native JSON mode.
			block.kind = blockText
			s.emit(provider.TextStart{ID: id})
			return
		}
		block.kind = blockToolCall
		block.toolCallID = event.ContentBlock.ID
		block.toolName = event.ContentBlock.Name
		s.emit(provider.ToolInputStart{ID: id, ToolName: event.ContentBlock.Name})

	case "server_tool_use":
		// A hosted tool the provider runs itself. Its arguments still stream
		// in as input_json_delta, so it is tracked like any other tool call
		// and only marked provider-executed when it closes.
		call, ok := hostedToolCall(*event.ContentBlock)
		if !ok {
			block.kind = blockIgnored
			s.emit(unknownBlock(*event.ContentBlock))
			return
		}
		block.kind = blockHostedCall
		block.toolCallID = call.ToolCallID
		block.toolName = call.ToolName
		block.serverToolName = event.ContentBlock.Name
		s.emit(provider.ToolInputStart{
			ID:               id,
			ToolName:         call.ToolName,
			ProviderExecuted: true,
		})

	default:
		// A hosted tool's result arrives whole, so it is emitted here rather
		// than accumulated: there are no deltas to wait for.
		if results := hostedResultBlocks(*event.ContentBlock, newSourceID); results != nil {
			block.kind = blockIgnored
			for _, c := range results {
				if part, ok := c.(provider.StreamPart); ok {
					s.emit(part)
				}
			}
			return
		}

		block.kind = blockIgnored
		s.emit(unknownBlock(*event.ContentBlock))
	}
}

// handleDelta appends to an open block.
func (s *streamState) handleDelta(event streamEvent) {
	if event.Delta == nil {
		return
	}
	block, ok := s.blocks[event.Index]
	if !ok {
		return
	}
	id := strconv.Itoa(event.Index)

	switch event.Delta.Type {
	case "text_delta":
		s.emit(provider.TextDelta{ID: id, Delta: event.Delta.Text})

	case "thinking_delta":
		s.emit(provider.ReasoningDelta{ID: id, Delta: event.Delta.Thinking})

	case "signature_delta":
		// The signature is metadata rather than reasoning text, so it rides
		// on an empty delta. It has to reach the caller: without it the
		// reasoning cannot be replayed on the next turn.
		if block.kind != blockReasoning {
			return
		}
		block.signature.WriteString(event.Delta.Signature)
		s.emit(provider.ReasoningDelta{
			ID:    id,
			Delta: "",
			ProviderMetadata: provider.ProviderMetadata{
				providerID: {"signature": event.Delta.Signature},
			},
		})

	case "input_json_delta":
		block.input.WriteString(event.Delta.PartialJSON)
		if block.kind == blockText {
			s.emit(provider.TextDelta{ID: id, Delta: event.Delta.PartialJSON})
			return
		}
		s.emit(provider.ToolInputDelta{ID: id, Delta: event.Delta.PartialJSON})
	}
}

// stopBlock closes a content block and emits its completed form.
func (s *streamState) stopBlock(index int) {
	block, ok := s.blocks[index]
	if !ok {
		return
	}
	delete(s.blocks, index)
	id := strconv.Itoa(index)

	switch block.kind {
	case blockText:
		s.emit(provider.TextEnd{ID: id})

	case blockReasoning:
		metadata := provider.JSONObject{}
		if sig := block.signature.String(); sig != "" {
			metadata["signature"] = sig
		}
		if block.redactedData != "" {
			metadata["redactedData"] = block.redactedData
		}
		s.emit(provider.ReasoningEnd{
			ID:               id,
			ProviderMetadata: provider.ProviderMetadata{providerID: metadata},
		})

	case blockToolCall, blockHostedCall:
		s.emit(provider.ToolInputEnd{ID: id})

		// Anthropic sends no input_json_delta for a tool with no arguments,
		// so an empty accumulator means "{}", not a malformed call.
		input := block.input.String()
		if strings.TrimSpace(input) == "" {
			input = "{}"
		}

		call := provider.ToolCall{
			ToolCallID: block.toolCallID,
			ToolName:   block.toolName,
			Input:      input,
		}
		if block.kind == blockHostedCall {
			call.ProviderExecuted = true
			call.ProviderMetadata = provider.ProviderMetadata{
				providerID: {"serverToolName": block.serverToolName},
			}
		}
		s.emit(call)

	case blockIgnored:
		// Nothing was opened, so nothing to close.
	}
}

// mergeUsage folds the output counts from message_delta into the input counts
// captured at message_start.
func (s *streamState) mergeUsage(u anthropicUsage) {
	merged := convertUsage(u)

	// message_delta reports output tokens but repeats input_tokens as zero,
	// so keep whatever message_start reported unless this event improves on it.
	if merged.InputTokens.NoCache != nil && *merged.InputTokens.NoCache == 0 &&
		s.usage.InputTokens.NoCache != nil && *s.usage.InputTokens.NoCache > 0 {
		merged.InputTokens = s.usage.InputTokens
	}
	s.usage = merged
}

// emitFinish sends the terminal Finish part, at most once.
func (s *streamState) emitFinish() {
	if s.sentFinish {
		return
	}
	s.sentFinish = true
	s.emit(provider.Finish{Usage: s.usage, FinishReason: s.finish})
}
