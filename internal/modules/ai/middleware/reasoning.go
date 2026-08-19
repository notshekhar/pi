package middleware

import (
	"context"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// ExtractReasoning moves thinking that a model wrote inline into its text out
// into proper reasoning parts.
//
// Several open-weight models (DeepSeek R1 and its distills, QwQ) emit
// <think>…</think> in the ordinary content stream when served through a
// gateway that does not parse it. Without this the tags show up in the answer.
//
// tag is the element name without brackets, e.g. "think".
func ExtractReasoning(tag string) Middleware {
	openTag, closeTag := "<"+tag+">", "</"+tag+">"

	return Middleware{
		Name: "extract-reasoning",

		WrapGenerate: func(ctx context.Context, _ Info, opts provider.CallOptions, next Next) (*provider.GenerateResult, error) {
			res, err := next.Generate(ctx, opts)
			if err != nil {
				return nil, err
			}

			content := make([]provider.Content, 0, len(res.Content))
			for _, c := range res.Content {
				text, ok := c.(provider.Text)
				if !ok {
					content = append(content, c)
					continue
				}
				content = append(content, splitReasoning(text, openTag, closeTag)...)
			}
			res.Content = content
			return res, nil
		},

		WrapStream: func(ctx context.Context, _ Info, opts provider.CallOptions, next Next) (*provider.StreamResult, error) {
			res, err := next.Stream(ctx, opts)
			if err != nil {
				return nil, err
			}

			out := make(chan provider.StreamPart, streamBufferSize)
			source := res.Stream

			go func() {
				defer close(out)
				s := &tagSplitter{open: openTag, close: closeTag, out: out, ctx: ctx}
				for part := range source {
					if !s.handle(part) {
						return
					}
				}
				s.flush()
			}()

			forwarded := *res
			forwarded.Stream = out
			return &forwarded, nil
		},
	}
}

// splitReasoning breaks one completed text block into alternating text and
// reasoning content.
func splitReasoning(text provider.Text, openTag, closeTag string) []provider.Content {
	if !strings.Contains(text.Text, openTag) {
		return []provider.Content{text}
	}

	var out []provider.Content
	rest := text.Text

	for {
		before, after, found := strings.Cut(rest, openTag)
		if !found {
			break
		}
		if trimmed := strings.TrimSpace(before); trimmed != "" {
			out = append(out, provider.Text{Text: trimmed, ProviderMetadata: text.ProviderMetadata})
		}

		thought, remainder, closed := strings.Cut(after, closeTag)
		if !closed {
			// The model never closed the tag, so everything after it is
			// thinking that was cut off.
			out = append(out, provider.Reasoning{Text: strings.TrimSpace(after)})
			return out
		}
		if trimmed := strings.TrimSpace(thought); trimmed != "" {
			out = append(out, provider.Reasoning{Text: trimmed})
		}
		rest = remainder
	}

	if trimmed := strings.TrimSpace(rest); trimmed != "" {
		out = append(out, provider.Text{Text: trimmed, ProviderMetadata: text.ProviderMetadata})
	}
	if len(out) == 0 {
		return []provider.Content{text}
	}
	return out
}

// tagSplitter rewrites a stream, turning text deltas inside the tag into
// reasoning deltas.
//
// A tag can be split across deltas, so text is held back whenever its tail
// could still turn out to be the start of one.
type tagSplitter struct {
	open  string
	close string
	out   chan<- provider.StreamPart
	ctx   context.Context

	// buffer holds text that cannot be emitted yet because it might be a
	// partial tag.
	buffer string
	// inReasoning tracks which side of the tag the stream is on.
	inReasoning bool
	// reasoningID is the id of the synthetic reasoning block, which the
	// provider never opened.
	reasoningID string
	// textID is the id of the text block being rewritten, remembered so the
	// reasoning block can be closed before the text block ends.
	textID string
}

// handle processes one part, returning false when the consumer has gone.
func (s *tagSplitter) handle(part provider.StreamPart) bool {
	delta, ok := part.(provider.TextDelta)
	if !ok {
		// A text block ending while reasoning is open means the tag was never
		// closed; close the reasoning first so the parts stay balanced.
		if _, isEnd := part.(provider.TextEnd); isEnd && s.inReasoning {
			s.flushBuffer()
			s.closeReasoning()
		}
		return s.emit(part)
	}

	s.textID = delta.ID
	s.buffer += delta.Delta

	for {
		if s.inReasoning {
			before, after, found := strings.Cut(s.buffer, s.close)
			if !found {
				// Hold back only as much as could be a partial closing tag.
				emit, keep := splitAtPartial(s.buffer, s.close)
				s.buffer = keep
				if emit != "" && !s.emitReasoning(emit) {
					return false
				}
				return true
			}
			if before != "" && !s.emitReasoning(before) {
				return false
			}
			s.closeReasoning()
			s.buffer = after
			continue
		}

		before, after, found := strings.Cut(s.buffer, s.open)
		if !found {
			emit, keep := splitAtPartial(s.buffer, s.open)
			s.buffer = keep
			if emit != "" && !s.emitText(emit) {
				return false
			}
			return true
		}
		if before != "" && !s.emitText(before) {
			return false
		}
		s.openReasoning()
		s.buffer = after
	}
}

// flush emits whatever is left once the source stream ends.
func (s *tagSplitter) flush() {
	s.flushBuffer()
	if s.inReasoning {
		s.closeReasoning()
	}
}

// flushBuffer emits held-back text on whichever side of the tag it belongs.
func (s *tagSplitter) flushBuffer() {
	if s.buffer == "" {
		return
	}
	text := s.buffer
	s.buffer = ""
	if s.inReasoning {
		s.emitReasoning(text)
		return
	}
	s.emitText(text)
}

func (s *tagSplitter) openReasoning() {
	s.inReasoning = true
	s.reasoningID = providerutil.GenerateID("reasoning", 12)
	s.emit(provider.ReasoningStart{ID: s.reasoningID})
}

func (s *tagSplitter) closeReasoning() {
	s.emit(provider.ReasoningEnd{ID: s.reasoningID})
	s.inReasoning = false
	s.reasoningID = ""
}

func (s *tagSplitter) emitText(text string) bool {
	return s.emit(provider.TextDelta{ID: s.textID, Delta: text})
}

func (s *tagSplitter) emitReasoning(text string) bool {
	return s.emit(provider.ReasoningDelta{ID: s.reasoningID, Delta: text})
}

func (s *tagSplitter) emit(part provider.StreamPart) bool {
	select {
	case s.out <- part:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// splitAtPartial divides s into the part that can be emitted now and the tail
// that has to be held back because it could still grow into tag.
func splitAtPartial(s, tag string) (emit, keep string) {
	// The longest suffix of s that is a prefix of tag is the only thing that
	// could still become the tag.
	max := len(tag) - 1
	if max > len(s) {
		max = len(s)
	}
	for n := max; n > 0; n-- {
		if strings.HasPrefix(tag, s[len(s)-n:]) {
			return s[:len(s)-n], s[len(s)-n:]
		}
	}
	return s, ""
}
