package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// streamBufferSize decouples the reader goroutine from the consumer.
const streamBufferSize = 64

// DoStream implements provider.LanguageModel.
func (m *languageModel) DoStream(ctx context.Context, opts provider.CallOptions) (*provider.StreamResult, error) {
	req, warnings, err := m.buildRequest(opts)
	if err != nil {
		return nil, err
	}
	path := m.provider.path(m.modelID, "streamGenerateContent", true)

	body, err := providerutil.Retry(ctx, m.provider.retry, func(ctx context.Context) (io.ReadCloser, error) {
		httpResp, err := m.provider.client.PostJSON(ctx, path, req, opts.Headers)
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
			ctx:    ctx,
			out:    out,
			model:  m,
			raw:    opts.IncludeRawChunks,
			finish: provider.FinishReason{Unified: provider.FinishOther},
		}
		s.emit(provider.StreamStart{Warnings: warnings})
		s.run(body)
	}()

	requestBody, _ := json.Marshal(req)

	return &provider.StreamResult{
		Stream:  out,
		Request: &provider.RequestInfo{Body: string(requestBody)},
	}, nil
}

// streamState carries the bookkeeping for one streaming response.
//
// Google streams whole GenerateContentResponse values rather than deltas, so
// each chunk carries complete parts. Text still needs to be presented as a
// single logical block, which means opening one on the first text part and
// closing it when the stream ends or reasoning takes over.
type streamState struct {
	ctx   context.Context
	out   chan<- provider.StreamPart
	model *languageModel
	raw   bool

	textOpen      bool
	reasoningOpen bool
	// blockSeq numbers the synthesised block ids, since Google supplies none.
	blockSeq int
	textID   string

	toolCallSeen bool
	// lastCodeCallID pairs a hosted code execution result with the code that
	// produced it, which Google links only by the order the parts arrive in.
	lastCodeCallID string
	usage          provider.Usage
	finish         provider.FinishReason
	rawFinish      string
	sentFinish     bool
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

		var chunk generateResponse
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
		s.emit(provider.ErrorPart{Err: fmt.Errorf("google: reading stream: %w", err)})
	}

	s.closeBlocks()
	s.emitFinish()
}

// handle folds one chunk into the stream.
func (s *streamState) handle(chunk generateResponse) {
	if chunk.ResponseID != "" || chunk.ModelVersion != "" {
		s.emit(provider.ResponseMetadataPart{ResponseMetadata: provider.ResponseMetadata{
			ID: chunk.ResponseID, ModelID: chunk.ModelVersion,
		}})
	}

	if chunk.PromptFeedback != nil && chunk.PromptFeedback.BlockReason != "" {
		s.emit(provider.ErrorPart{Err: &provider.APICallError{
			Message: "the prompt was blocked: " + chunk.PromptFeedback.BlockReason,
		}})
	}

	if chunk.UsageMetadata != nil {
		// Each chunk restates the running totals, so the last one wins.
		s.usage = convertUsage(chunk.UsageMetadata)
	}

	for _, c := range chunk.Candidates {
		if c.Content != nil {
			for _, p := range c.Content.Parts {
				s.handlePart(p)
			}
		}
		for _, src := range groundingSources(c.GroundingMetadata) {
			s.emit(src)
		}
		if c.FinishReason != "" {
			s.rawFinish = c.FinishReason
		}
	}
}

// handlePart emits one complete part from a chunk.
func (s *streamState) handlePart(p part) {
	switch {
	case p.FunctionCall != nil:
		// Tool arguments arrive complete rather than as fragments, so the
		// start, delta and end are emitted together to keep the part sequence
		// the same shape consumers see from other providers.
		s.closeBlocks()
		s.toolCallSeen = true

		args, err := json.Marshal(p.FunctionCall.Args)
		if err != nil {
			args = []byte("{}")
		}
		id := p.FunctionCall.ID
		if id == "" {
			id = providerutil.GenerateID("call", 12)
		}

		s.emit(provider.ToolInputStart{ID: id, ToolName: p.FunctionCall.Name})
		s.emit(provider.ToolInputDelta{ID: id, Delta: string(args)})
		s.emit(provider.ToolInputEnd{ID: id})
		s.emit(provider.ToolCall{
			ToolCallID:       id,
			ToolName:         p.FunctionCall.Name,
			Input:            string(args),
			ProviderMetadata: signatureMetadata(p.ThoughtSignature),
		})

	case p.Thought:
		if s.textOpen {
			s.closeText()
		}
		if !s.reasoningOpen {
			s.reasoningOpen = true
			s.blockSeq++
			s.textID = "reasoning-" + strconv.Itoa(s.blockSeq)
			s.emit(provider.ReasoningStart{ID: s.textID})
		}
		s.emit(provider.ReasoningDelta{
			ID:               s.textID,
			Delta:            p.Text,
			ProviderMetadata: signatureMetadata(p.ThoughtSignature),
		})

	case p.ExecutableCode != nil:
		// The hosted code execution tool, which the provider has already run.
		// Its arguments arrive whole, so the input parts are emitted together.
		s.closeBlocks()
		call := hostedCodeCall(*p.ExecutableCode)
		s.emit(provider.ToolInputStart{
			ID:               call.ToolCallID,
			ToolName:         call.ToolName,
			ProviderExecuted: true,
		})
		s.emit(provider.ToolInputDelta{ID: call.ToolCallID, Delta: call.Input})
		s.emit(provider.ToolInputEnd{ID: call.ToolCallID})
		s.emit(call)
		s.lastCodeCallID = call.ToolCallID

	case p.CodeExecutionResult != nil:
		s.closeBlocks()
		result := hostedCodeResult(*p.CodeExecutionResult)
		// Google links the result to its code only by position, so it is
		// matched to the call that most recently went past.
		result.ToolCallID = s.lastCodeCallID
		s.emit(result)

	case p.InlineData != nil:
		s.emit(provider.File{
			MediaType: p.InlineData.MimeType,
			Data:      provider.FileDataBytes{Base64: p.InlineData.Data},
		})

	case p.Text != "":
		if s.reasoningOpen {
			s.closeReasoning()
		}
		if !s.textOpen {
			s.textOpen = true
			s.blockSeq++
			s.textID = "text-" + strconv.Itoa(s.blockSeq)
			s.emit(provider.TextStart{ID: s.textID})
		}
		s.emit(provider.TextDelta{
			ID:               s.textID,
			Delta:            p.Text,
			ProviderMetadata: signatureMetadata(p.ThoughtSignature),
		})
	}
}

// closeText closes the open text block.
func (s *streamState) closeText() {
	if !s.textOpen {
		return
	}
	s.textOpen = false
	s.emit(provider.TextEnd{ID: s.textID})
}

// closeReasoning closes the open reasoning block.
func (s *streamState) closeReasoning() {
	if !s.reasoningOpen {
		return
	}
	s.reasoningOpen = false
	s.emit(provider.ReasoningEnd{ID: s.textID})
}

// closeBlocks closes whichever block is open.
func (s *streamState) closeBlocks() {
	s.closeText()
	s.closeReasoning()
}

// emitFinish sends the terminal Finish part, at most once.
//
// The finish reason is resolved here because Google reports STOP even when the
// model asked for tools, and whether tools were asked for is only known once
// the whole stream has been seen.
func (s *streamState) emitFinish() {
	if s.sentFinish {
		return
	}
	s.sentFinish = true
	s.finish = mapFinishReason(s.rawFinish, s.toolCallSeen)
	s.emit(provider.Finish{Usage: s.usage, FinishReason: s.finish})
}
