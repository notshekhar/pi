package ai

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// ChunkDetector splits buffered text into the next chunk to emit and the
// remainder to hold back. Returning an empty chunk means "not yet".
type ChunkDetector func(buffer string) (chunk, rest string)

// SmoothOptions configures SmoothStream.
type SmoothOptions struct {
	// Delay is the pause between chunks. Zero uses 10ms, which reads as
	// natural typing without making a long answer feel slow.
	Delay time.Duration

	// Detector decides where to split. Nil emits a word at a time.
	Detector ChunkDetector
}

// SmoothStream re-paces a stream's text deltas so they arrive in even chunks
// rather than in whatever bursts the provider sent.
//
// Providers emit text in uneven lumps — a few characters, then a paragraph —
// which reads as stuttering. This buffers the text and releases it on a timer.
// Nothing else is touched: every other part passes through in order, and the
// stream still ends when the source ends.
//
// It costs latency equal to the delay, so it is for rendering to a person, not
// for a pipe.
func SmoothStream(ctx context.Context, in <-chan StreamPart, opts SmoothOptions) <-chan StreamPart {
	delay := opts.Delay
	if delay <= 0 {
		delay = 10 * time.Millisecond
	}
	detect := opts.Detector
	if detect == nil {
		detect = ChunkByWord
	}

	out := make(chan StreamPart, streamBufferSize)

	go func() {
		defer close(out)

		s := &smoother{out: out, ctx: ctx, delay: delay, detect: detect}
		for part := range in {
			if !s.handle(part) {
				// The consumer is gone. Drain the source so its goroutine
				// finishes and the provider connection is released.
				go func() {
					for range in { //nolint:revive // draining
					}
				}()
				return
			}
		}
		s.flush()
	}()

	return out
}

// smoother buffers one text block at a time and releases it in chunks.
type smoother struct {
	out    chan<- StreamPart
	ctx    context.Context
	delay  time.Duration
	detect ChunkDetector

	// buffer holds text not yet released, and id is the block it belongs to.
	buffer string
	id     string
}

// handle processes one part, reporting false when the consumer has gone.
func (s *smoother) handle(part StreamPart) bool {
	// Only answer text is re-paced. Reasoning is left alone: a thinking trace
	// is either rendered as it arrives or not shown at all, and delaying it
	// would hold up the answer behind it.
	delta, ok := part.(provider.TextDelta)
	if !ok {
		// Anything that is not text has to be preceded by the text before it,
		// or a tool call would appear before the sentence that introduced it.
		if !s.flush() {
			return false
		}
		return s.emit(part)
	}

	// A new block means the previous one is finished.
	if s.id != "" && delta.ID != s.id {
		if !s.flush() {
			return false
		}
	}
	s.id = delta.ID
	s.buffer += delta.Delta

	for {
		chunk, rest := s.detect(s.buffer)
		if chunk == "" {
			return true
		}
		s.buffer = rest
		if !s.emitText(chunk) {
			return false
		}
	}
}

// flush releases whatever is buffered, without waiting for a chunk boundary.
func (s *smoother) flush() bool {
	if s.buffer == "" {
		return true
	}
	text := s.buffer
	s.buffer = ""
	return s.emitText(text)
}

// emitText releases one chunk after the delay.
func (s *smoother) emitText(text string) bool {
	timer := time.NewTimer(s.delay)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-s.ctx.Done():
		return false
	}

	return s.emit(provider.TextDelta{ID: s.id, Delta: text})
}

// emit forwards a part.
func (s *smoother) emit(part StreamPart) bool {
	select {
	case s.out <- part:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// ChunkByWord releases a word at a time, including its trailing whitespace.
func ChunkByWord(buffer string) (chunk, rest string) {
	for i, r := range buffer {
		if !unicode.IsSpace(r) {
			continue
		}
		// Include the whitespace so words do not run together, and so the
		// chunk boundary is invisible in the rendered output.
		width := len(string(r))
		return buffer[:i+width], buffer[i+width:]
	}
	return "", buffer
}

// ChunkByLine releases a line at a time, which suits rendering code or a table
// where a half-written line is worse than a pause.
func ChunkByLine(buffer string) (chunk, rest string) {
	if i := strings.IndexByte(buffer, '\n'); i >= 0 {
		return buffer[:i+1], buffer[i+1:]
	}
	return "", buffer
}

// ChunkByRune releases one character at a time, for a typewriter effect.
func ChunkByRune(buffer string) (chunk, rest string) {
	for i := range buffer {
		if i == 0 {
			continue
		}
		return buffer[:i], buffer[i:]
	}
	if buffer != "" {
		return buffer, ""
	}
	return "", ""
}
