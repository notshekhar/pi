package bedrock

import (
	"context"
	"encoding/json"
	"errors"
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

// eventStreamAccept is the media type ConverseStream returns.
const eventStreamAccept = "application/vnd.amazon.eventstream"

// DoStream implements provider.LanguageModel.
func (m *languageModel) DoStream(ctx context.Context, opts provider.CallOptions) (*provider.StreamResult, error) {
	built, err := m.buildRequest(opts)
	if err != nil {
		return nil, err
	}

	path := modelPath(m.modelID, "converse-stream")
	headers := cloneHeaders(opts.Headers)
	if headers == nil {
		headers = provider.Headers{}
	}
	headers["Accept"] = eventStreamAccept

	// Only the connection setup is retried. Once bytes are flowing a failure
	// cannot be retried transparently: part of the response has already been
	// handed to the caller, and Bedrock offers no way to resume.
	resp, err := providerutil.Retry(ctx, m.provider.retry, func(ctx context.Context) (*httpStream, error) {
		httpResp, err := m.postJSON(ctx, path, built.body, headers)
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
			jsonTool: built.jsonTool,
			finish:   provider.FinishReason{Unified: provider.FinishOther},
		}
		s.emit(provider.StreamStart{Warnings: built.warnings})
		s.run(resp.body)
	}()

	requestBody, _ := json.Marshal(built.body)

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

// cloneHeaders copies a header map so Accept can be set without mutating
// the caller's.
func cloneHeaders(h provider.Headers) provider.Headers {
	if h == nil {
		return nil
	}
	out := make(provider.Headers, len(h)+1)
	for k, v := range h {
		out[k] = v
	}
	return out
}

// blockKind is what a streaming content block turned out to be.
type blockKind int

const (
	blockText blockKind = iota
	blockReasoning
	blockToolCall
)

// blockState tracks one open content block across its start, deltas and stop.
type blockState struct {
	kind blockKind

	toolCallID string
	toolName   string
	input      strings.Builder

	// jsonTool is set when this block is the forced structured-output tool,
	// whose arguments are streamed as text rather than as a tool call.
	jsonTool bool
}

// streamState carries the bookkeeping for one streaming response.
type streamState struct {
	model *languageModel
	out   chan<- provider.StreamPart
	ctx   context.Context

	blocks map[int]*blockState

	raw      bool
	jsonTool bool
	usage    provider.Usage
	finish   provider.FinishReason
	// jsonFromTool is set when the forced json tool produced the answer, so
	// a tool_use stop reason is reported as stop.
	jsonFromTool bool
	sentFinish   bool
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

// run consumes the event-stream body and translates it into spec stream parts.
func (s *streamState) run(body io.ReadCloser) {
	defer body.Close()

	reader := newEventStreamReader(body)
	for {
		if s.ctx.Err() != nil {
			break
		}

		msg, err := reader.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if s.ctx.Err() == nil {
				s.emit(provider.ErrorPart{Err: err})
			}
			break
		}

		if s.raw {
			var payload any
			if json.Unmarshal(msg.Payload, &payload) == nil {
				s.emit(provider.Raw{RawValue: map[string]any{
					"eventType": msg.eventType(),
					"payload":   payload,
				}})
			}
		}

		if msg.messageType() == "exception" {
			s.handleException(msg)
			continue
		}

		var event streamEvent
		if err := json.Unmarshal(msg.Payload, &event); err != nil {
			s.emit(provider.ErrorPart{Err: &provider.InvalidResponseError{
				Message:  "could not decode stream event",
				Response: string(msg.Payload),
				Cause:    err,
			}})
			continue
		}

		s.handle(msg.eventType(), event)
	}

	s.emitFinish()
}

// handleException turns a mid-stream error frame into an ErrorPart.
func (s *streamState) handleException(msg eventMessage) {
	s.finish = provider.FinishReason{Unified: provider.FinishError}

	var event streamEvent
	_ = json.Unmarshal(msg.Payload, &event)
	message := event.Message
	if message == "" {
		message = msg.exceptionType()
	}
	if message == "" {
		message = "bedrock stream exception"
	}
	s.emit(provider.ErrorPart{Err: &provider.APICallError{
		Message: message,
		Data:    msg.exceptionType(),
	}})
}

// handle dispatches one decoded event.
func (s *streamState) handle(kind string, event streamEvent) {
	switch kind {
	case "messageStart":
		// Role only; nothing to emit.

	case "contentBlockStart":
		s.startBlock(event)

	case "contentBlockDelta":
		s.handleDelta(event)

	case "contentBlockStop":
		s.stopBlock(event)

	case "messageStop":
		s.finish = mapStopReason(event.StopReason, s.jsonFromTool)

	case "metadata":
		if event.Usage != nil {
			s.usage = convertUsage(event.Usage)
		}

	case "internalServerException", "modelStreamErrorException",
		"throttlingException", "validationException":
		s.finish = provider.FinishReason{Unified: provider.FinishError}
		message := event.Message
		if message == "" {
			message = kind
		}
		s.emit(provider.ErrorPart{Err: &provider.APICallError{Message: message, Data: kind}})
	}
}

// startBlock opens a content block.
func (s *streamState) startBlock(event streamEvent) {
	index := derefInt(event.ContentBlockIndex)
	id := strconv.Itoa(index)

	if event.Start != nil && event.Start.ToolUse != nil {
		tool := event.Start.ToolUse
		block := &blockState{
			kind:       blockToolCall,
			toolCallID: normalizeToolCallID(tool.ToolUseID, s.model.caps.IsMistral),
			toolName:   tool.Name,
			jsonTool:   s.jsonTool && tool.Name == jsonToolName,
		}
		s.blocks[index] = block
		if block.jsonTool {
			return
		}
		s.emit(provider.ToolInputStart{ID: block.toolCallID, ToolName: tool.Name})
		return
	}

	// A start without toolUse is text. Reasoning usually arrives as a delta
	// with no start, and is opened there instead.
	s.blocks[index] = &blockState{kind: blockText}
	s.emit(provider.TextStart{ID: id})
}

// handleDelta appends to an open block, opening one if the start was skipped.
func (s *streamState) handleDelta(event streamEvent) {
	if event.Delta == nil {
		return
	}
	index := derefInt(event.ContentBlockIndex)
	id := strconv.Itoa(index)

	if event.Delta.Text != "" {
		s.ensureBlock(index, blockText, func() {
			s.emit(provider.TextStart{ID: id})
		})
		s.emit(provider.TextDelta{ID: id, Delta: event.Delta.Text})
		return
	}

	if event.Delta.ToolUse != nil {
		block, ok := s.blocks[index]
		if !ok || block.kind != blockToolCall {
			return
		}
		block.input.WriteString(event.Delta.ToolUse.Input)
		if !block.jsonTool {
			s.emit(provider.ToolInputDelta{ID: block.toolCallID, Delta: event.Delta.ToolUse.Input})
		}
		return
	}

	if event.Delta.ReasoningContent != nil {
		rc := event.Delta.ReasoningContent
		s.ensureBlock(index, blockReasoning, func() {
			s.emit(provider.ReasoningStart{ID: id})
		})

		switch {
		case rc.Text != "":
			s.emit(provider.ReasoningDelta{ID: id, Delta: rc.Text})

		case rc.Signature != "":
			s.emit(provider.ReasoningDelta{
				ID:               id,
				Delta:            "",
				ProviderMetadata: reasoningMeta(provider.JSONObject{"signature": rc.Signature}),
			})

		case len(rc.RedactedContent) > 0:
			s.emit(provider.ReasoningDelta{
				ID:    id,
				Delta: "",
				ProviderMetadata: reasoningMeta(provider.JSONObject{
					"redactedData": string(rc.RedactedContent),
				}),
			})
		}
	}
}

// ensureBlock opens a block of the given kind if one is not already open.
func (s *streamState) ensureBlock(index int, kind blockKind, start func()) {
	if _, ok := s.blocks[index]; ok {
		return
	}
	s.blocks[index] = &blockState{kind: kind}
	start()
}

// stopBlock closes a content block and emits its completed form.
func (s *streamState) stopBlock(event streamEvent) {
	index := derefInt(event.ContentBlockIndex)
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
		s.emit(provider.ReasoningEnd{ID: id})

	case blockToolCall:
		input := block.input.String()
		if strings.TrimSpace(input) == "" {
			input = "{}"
		}

		if block.jsonTool {
			s.jsonFromTool = true
			s.emit(provider.TextStart{ID: id})
			s.emit(provider.TextDelta{ID: id, Delta: input})
			s.emit(provider.TextEnd{ID: id})
			return
		}

		s.emit(provider.ToolInputEnd{ID: block.toolCallID})
		s.emit(provider.ToolCall{
			ToolCallID: block.toolCallID,
			ToolName:   block.toolName,
			Input:      input,
		})
	}
}

// emitFinish sends the terminal Finish part, at most once.
func (s *streamState) emitFinish() {
	if s.sentFinish {
		return
	}
	s.sentFinish = true
	if s.jsonFromTool && s.finish.Raw == "tool_use" {
		s.finish = mapStopReason("tool_use", true)
	}
	s.emit(provider.Finish{Usage: s.usage, FinishReason: s.finish})
}

// derefInt returns the pointed-to value or zero.
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
