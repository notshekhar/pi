package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/ai/providerutil"
)

// event is one Responses stream event; only the fields used are decoded.
type event struct {
	Type        string          `json:"type"`
	ItemID      string          `json:"item_id"`
	OutputIndex int             `json:"output_index"`
	Delta       string          `json:"delta"`
	Item        *outputItem     `json:"item"`
	Response    *responseObject `json:"response"`
	// SummaryIndex is a reasoning summary part's position in its item.
	SummaryIndex int `json:"summary_index"`
	// error events: the details nested under error, or (older) inline.
	Error *struct {
		Type    string `json:"type"`
		Code    any    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Code    any    `json:"code"`
	Message string `json:"message"`
}

type outputItem struct {
	Type             string  `json:"type"`
	ID               string  `json:"id"`
	CallID           string  `json:"call_id"`
	Name             string  `json:"name"`
	Arguments        string  `json:"arguments"`
	EncryptedContent *string `json:"encrypted_content"`
}

type responseObject struct {
	ID                string `json:"id"`
	Model             string `json:"model"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *apiUsage `json:"usage"`
	Error *struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// apiUsage is the Responses usage block. input_tokens includes the cached
// tokens; output_tokens includes the reasoning ones.
type apiUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens     int64  `json:"cached_tokens"`
		CacheWriteTokens *int64 `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// convertUsage is the AI SDK's convertOpenAIResponsesUsage.
func convertUsage(u *apiUsage) provider.Usage {
	if u == nil {
		return provider.Usage{}
	}
	total, out := u.InputTokens, u.OutputTokens
	var cached, reasoning int64
	var write *int64
	if d := u.InputTokensDetails; d != nil {
		cached, write = d.CachedTokens, d.CacheWriteTokens
	}
	if d := u.OutputTokensDetails; d != nil {
		reasoning = d.ReasoningTokens
	}
	fresh := total - cached
	if write != nil {
		fresh -= *write
	}
	text := out - reasoning
	usage := provider.Usage{
		InputTokens:  provider.InputTokens{Total: &total, NoCache: &fresh, CacheRead: &cached, CacheWrite: write},
		OutputTokens: provider.OutputTokens{Total: &out, Text: &text, Reasoning: &reasoning},
	}
	if raw, err := json.Marshal(u); err == nil {
		var obj provider.JSONObject
		if json.Unmarshal(raw, &obj) == nil {
			usage.Raw = obj
		}
	}
	return usage
}

// mapFinish is the AI SDK's mapOpenAIResponseFinishReason.
func mapFinish(reason string, functionCall bool) provider.FinishReason {
	unified := provider.FinishOther
	switch reason {
	case "":
		unified = provider.FinishStop
		if functionCall {
			unified = provider.FinishToolCalls
		}
	case "max_output_tokens":
		unified = provider.FinishLength
	case "content_filter":
		unified = provider.FinishContentFilter
	default:
		if functionCall {
			unified = provider.FinishToolCalls
		}
	}
	return provider.FinishReason{Unified: unified, Raw: reason}
}

type reasoningState struct {
	encrypted *string
	open      map[int]bool // summary parts started and not yet ended
}

type streamState struct {
	ctx    context.Context
	out    chan<- provider.StreamPart
	raw    bool
	url    string
	end    providerutil.StreamEnd
	usage  provider.Usage
	finish provider.FinishReason
	// completed is response.completed/incomplete/failed seen: the provider's
	// completion signal.
	completed    bool
	errored      bool
	functionCall bool
	reasoning    map[string]*reasoningState
	calls        map[string]outputItem // function calls by item id
}

func newStreamState(ctx context.Context, out chan<- provider.StreamPart, raw bool, url string) *streamState {
	return &streamState{ctx: ctx, out: out, raw: raw, url: url,
		reasoning: map[string]*reasoningState{}, calls: map[string]outputItem{}}
}

func (s *streamState) emit(part provider.StreamPart) bool {
	s.end.Observe(part)
	select {
	case s.out <- part:
		return true
	case <-s.ctx.Done():
		return false
	}
}

func meta(fields map[string]any) provider.ProviderMetadata {
	return provider.ProviderMetadata{providerID: fields}
}

func reasoningID(itemID string, index int) string { return itemID + ":" + strconv.Itoa(index) }

func (s *streamState) run(body io.ReadCloser) {
	err := providerutil.ScanSSE(body, func(ev providerutil.SSEEvent) error {
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		var e event
		if err := json.Unmarshal([]byte(ev.Data), &e); err != nil {
			s.emit(provider.ErrorPart{Err: &provider.InvalidResponseError{
				Message: "could not decode stream event", Response: ev.Data, Cause: err}})
			return nil
		}
		if s.raw {
			var v any
			if json.Unmarshal([]byte(ev.Data), &v) == nil {
				s.emit(provider.Raw{RawValue: v})
			}
		}
		if e.Type == "" {
			e.Type = ev.Event
		}
		s.handle(e)
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) && s.ctx.Err() == nil {
		s.emit(provider.ErrorPart{Err: fmt.Errorf("openairesponses: reading stream: %w", err)})
	}
	if s.ctx.Err() == nil {
		reason, err := s.end.Reason(s.completed, s.finish)
		if err != nil {
			s.emit(provider.ErrorPart{Err: fmt.Errorf("openairesponses: %w", err)})
		}
		s.finish = reason
	}
	s.emit(provider.Finish{Usage: s.usage, FinishReason: s.finish})
}

func (s *streamState) handle(e event) {
	switch e.Type {
	case "response.created":
		if r := e.Response; r != nil {
			s.emit(provider.ResponseMetadataPart{ResponseMetadata: provider.ResponseMetadata{ID: r.ID, ModelID: r.Model}})
		}

	case "response.output_item.added":
		if e.Item == nil {
			return
		}
		switch e.Item.Type {
		case "message":
			s.emit(provider.TextStart{ID: e.Item.ID, ProviderMetadata: meta(map[string]any{"itemId": e.Item.ID})})
		case "reasoning":
			s.reasoning[e.Item.ID] = &reasoningState{encrypted: e.Item.EncryptedContent, open: map[int]bool{0: true}}
			s.emit(provider.ReasoningStart{ID: reasoningID(e.Item.ID, 0), ProviderMetadata: meta(map[string]any{"itemId": e.Item.ID})})
		case "function_call":
			s.calls[e.Item.ID] = *e.Item
			s.emit(provider.ToolInputStart{ID: e.Item.CallID, ToolName: e.Item.Name})
		}

	case "response.output_text.delta":
		s.emit(provider.TextDelta{ID: e.ItemID, Delta: e.Delta})

	case "response.reasoning_summary_part.added":
		if r := s.reasoning[e.ItemID]; r != nil && e.SummaryIndex > 0 {
			// A new summary part closes the ones before it.
			for i := range r.open {
				s.emit(provider.ReasoningEnd{ID: reasoningID(e.ItemID, i), ProviderMetadata: meta(map[string]any{"itemId": e.ItemID})})
				delete(r.open, i)
			}
			r.open[e.SummaryIndex] = true
			s.emit(provider.ReasoningStart{ID: reasoningID(e.ItemID, e.SummaryIndex), ProviderMetadata: meta(map[string]any{"itemId": e.ItemID})})
		}

	case "response.reasoning_summary_text.delta":
		s.emit(provider.ReasoningDelta{ID: reasoningID(e.ItemID, e.SummaryIndex), Delta: e.Delta})

	case "response.function_call_arguments.delta":
		if call, ok := s.calls[e.ItemID]; ok {
			s.emit(provider.ToolInputDelta{ID: call.CallID, Delta: e.Delta})
		}

	case "response.output_item.done":
		if e.Item == nil {
			return
		}
		switch e.Item.Type {
		case "message":
			s.emit(provider.TextEnd{ID: e.Item.ID, ProviderMetadata: meta(map[string]any{"itemId": e.Item.ID})})
		case "reasoning":
			r := s.reasoning[e.Item.ID]
			if r == nil {
				return
			}
			fields := map[string]any{"itemId": e.Item.ID}
			if enc := e.Item.EncryptedContent; enc != nil {
				fields["reasoningEncryptedContent"] = *enc
			} else if r.encrypted != nil {
				fields["reasoningEncryptedContent"] = *r.encrypted
			}
			for i := range r.open {
				s.emit(provider.ReasoningEnd{ID: reasoningID(e.Item.ID, i), ProviderMetadata: meta(fields)})
			}
			delete(s.reasoning, e.Item.ID)
		case "function_call":
			s.functionCall = true
			args := e.Item.Arguments
			if args == "" {
				args = "{}"
			}
			s.emit(provider.ToolInputEnd{ID: e.Item.CallID})
			s.emit(provider.ToolCall{ToolCallID: e.Item.CallID, ToolName: e.Item.Name, Input: args,
				ProviderMetadata: meta(map[string]any{"itemId": e.Item.ID})})
			delete(s.calls, e.Item.ID)
		}

	case "response.completed", "response.incomplete":
		s.completed = true
		if r := e.Response; r != nil {
			reason := ""
			if r.IncompleteDetails != nil {
				reason = r.IncompleteDetails.Reason
			}
			if !s.errored {
				s.finish = mapFinish(reason, s.functionCall)
			}
			s.usage = convertUsage(r.Usage)
		}

	case "response.failed":
		s.completed = true
		s.finish = provider.FinishReason{Unified: provider.FinishError, Raw: "error"}
		if r := e.Response; r != nil {
			if r.IncompleteDetails != nil && r.IncompleteDetails.Reason != "" {
				s.finish = mapFinish(r.IncompleteDetails.Reason, s.functionCall)
			}
			s.usage = convertUsage(r.Usage)
			if r.Error != nil && !s.errored {
				s.errored = true
				s.emit(provider.ErrorPart{Err: s.apiError(r.Error.Message, r.Error.Code)})
			}
		}

	case "error":
		s.finish = provider.FinishReason{Unified: provider.FinishError, Raw: "error"}
		message, code := e.Message, e.Code
		if e.Error != nil {
			message, code = e.Error.Message, e.Error.Code
			if code == nil {
				code = e.Error.Type
			}
		}
		if !s.errored {
			s.errored = true
			s.emit(provider.ErrorPart{Err: s.apiError(message, code)})
		}
	}
}

// apiError is a stream error frame as an APICallError, retryable when its
// code says the service was busy (the AI SDK's createOpenAIProviderStreamError).
func (s *streamState) apiError(message string, code any) error {
	c := fmt.Sprint(code)
	status, retryable := 0, false
	switch c {
	case "rate_limit_exceeded":
		status, retryable = 429, true
	case "server_error", "internal_error":
		status, retryable = 500, true
	case "server_is_overloaded", "overloaded":
		status, retryable = 503, true
	}
	if message == "" {
		message = "openai responses stream error"
	}
	return &provider.APICallError{Message: message, URL: s.url, StatusCode: status, IsRetryable: retryable, Data: c}
}
