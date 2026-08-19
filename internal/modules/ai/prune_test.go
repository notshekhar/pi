package ai_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// conversation builds n alternating user/assistant messages labelled by index.
func conversation(n int) []provider.Message {
	out := make([]provider.Message, 0, n)
	for i := range n {
		text := string(rune('a' + i))
		if i%2 == 0 {
			out = append(out, ai.UserText(text))
			continue
		}
		out = append(out, ai.AssistantText(text))
	}
	return out
}

// labels renders a conversation as a comma-separated list of its text.
func labels(messages []provider.Message) string {
	var out []string
	for _, m := range messages {
		switch v := m.(type) {
		case provider.UserMessage:
			out = append(out, v.Content[0].(provider.TextPart).Text)
		case provider.AssistantMessage:
			out = append(out, v.Content[0].(provider.TextPart).Text)
		case provider.ToolMessage:
			out = append(out, "tool")
		}
	}
	return strings.Join(out, ",")
}

func TestPruneKeepsTheEndsAndDropsTheMiddle(t *testing.T) {
	messages := conversation(8) // a..h

	pruned := ai.PruneMessages(messages, ai.PruneOptions{KeepFirst: 2, KeepLast: 3})

	if got := labels(pruned); got != "a,b,f,g,h" {
		t.Errorf("pruned = %q, want a,b,f,g,h", got)
	}
}

func TestPruneInsertsTheMarker(t *testing.T) {
	messages := conversation(8)

	pruned := ai.PruneMessages(messages, ai.PruneOptions{
		KeepFirst: 1,
		KeepLast:  1,
		Marker:    "[earlier turns omitted]",
	})

	// Without a marker the model reads a conversation with a hole in it and
	// cannot tell that anything is missing.
	if got := labels(pruned); got != "a,[earlier turns omitted],h" {
		t.Errorf("pruned = %q", got)
	}
}

func TestPruneReturnsEverythingWhenNothingNeedsDropping(t *testing.T) {
	messages := conversation(3)

	pruned := ai.PruneMessages(messages, ai.PruneOptions{KeepFirst: 2, KeepLast: 2})

	if got := labels(pruned); got != "a,b,c" {
		t.Errorf("pruned = %q, want everything", got)
	}
}

func TestPruneNeverSeparatesAToolResultFromItsCall(t *testing.T) {
	// The tool message at index 5 answers the assistant message at 4. Cutting
	// between them produces a history every provider rejects.
	messages := []provider.Message{
		ai.UserText("a"),
		ai.AssistantText("b"),
		ai.UserText("c"),
		ai.AssistantText("d"),
		provider.AssistantMessage{Content: []provider.AssistantPart{
			provider.ToolCallPart{ToolCallID: "t1", ToolName: "read", Input: map[string]any{}},
		}},
		provider.ToolMessage{Content: []provider.ToolPart{
			provider.ToolResultPart{ToolCallID: "t1", ToolName: "read",
				Output: provider.ToolOutputText{Value: "ok"}},
		}},
		ai.AssistantText("g"),
	}

	// KeepLast 2 would begin the tail at the tool message, orphaning it.
	pruned := ai.PruneMessages(messages, ai.PruneOptions{KeepFirst: 1, KeepLast: 2})

	for i, m := range pruned {
		if _, ok := m.(provider.ToolMessage); !ok {
			continue
		}
		if i == 0 {
			t.Fatal("the pruned history starts with a tool result whose call was dropped")
		}
		if _, ok := pruned[i-1].(provider.AssistantMessage); !ok {
			t.Fatalf("the tool result at %d is not preceded by an assistant message", i)
		}
	}
}

func TestPruneDoesNotModifyTheInput(t *testing.T) {
	messages := conversation(6)
	before := labels(messages)

	ai.PruneMessages(messages, ai.PruneOptions{KeepFirst: 1, KeepLast: 1})

	if after := labels(messages); after != before {
		t.Errorf("the input was modified: %q became %q", before, after)
	}
}

func TestSmoothStreamRepacesTextAndPassesEverythingElseThrough(t *testing.T) {
	in := make(chan ai.StreamPart, 8)
	// One burst carrying several words, which is what a provider really sends.
	in <- provider.TextStart{ID: "0"}
	in <- provider.TextDelta{ID: "0", Delta: "the quick brown "}
	in <- provider.TextDelta{ID: "0", Delta: "fox"}
	in <- provider.TextEnd{ID: "0"}
	close(in)

	out := ai.SmoothStream(context.Background(), in, ai.SmoothOptions{Delay: time.Millisecond})

	var text strings.Builder
	var deltas int
	var sawStart, sawEnd bool

	for part := range out {
		switch v := part.(type) {
		case provider.TextStart:
			sawStart = true
		case provider.TextDelta:
			deltas++
			text.WriteString(v.Delta)
		case provider.TextEnd:
			sawEnd = true
		}
	}

	// Nothing may be lost or reordered; only the pacing changes.
	if text.String() != "the quick brown fox" {
		t.Errorf("text = %q", text.String())
	}
	if deltas < 4 {
		t.Errorf("deltas = %d, want the burst split into words", deltas)
	}
	if !sawStart || !sawEnd {
		t.Error("the block delimiters did not pass through")
	}
}

func TestSmoothStreamFlushesTextBeforeANonTextPart(t *testing.T) {
	in := make(chan ai.StreamPart, 4)
	// "fox" has no trailing space, so it is still buffered when the tool call
	// arrives. Emitting the call first would put it before its own preamble.
	in <- provider.TextDelta{ID: "0", Delta: "calling fox"}
	in <- provider.ToolCall{ToolCallID: "c1", ToolName: "read", Input: "{}"}
	close(in)

	out := ai.SmoothStream(context.Background(), in, ai.SmoothOptions{Delay: time.Millisecond})

	var order []string
	var text strings.Builder
	for part := range out {
		switch v := part.(type) {
		case provider.TextDelta:
			text.WriteString(v.Delta)
			if len(order) == 0 || order[len(order)-1] != "text" {
				order = append(order, "text")
			}
		case provider.ToolCall:
			order = append(order, "tool")
		}
	}

	if text.String() != "calling fox" {
		t.Errorf("text = %q, want the tail flushed", text.String())
	}
	if strings.Join(order, ",") != "text,tool" {
		t.Errorf("order = %v, want the text before the tool call", order)
	}
}

func TestChunkDetectors(t *testing.T) {
	chunk, rest := ai.ChunkByWord("hello world")
	if chunk != "hello " || rest != "world" {
		t.Errorf("ChunkByWord = %q, %q", chunk, rest)
	}
	// A word with no trailing space is not a word yet: more may be coming.
	if chunk, rest := ai.ChunkByWord("hello"); chunk != "" || rest != "hello" {
		t.Errorf("ChunkByWord on a partial word = %q, %q", chunk, rest)
	}

	if chunk, rest := ai.ChunkByLine("one\ntwo"); chunk != "one\n" || rest != "two" {
		t.Errorf("ChunkByLine = %q, %q", chunk, rest)
	}
	if chunk, rest := ai.ChunkByRune("abc"); chunk != "a" || rest != "bc" {
		t.Errorf("ChunkByRune = %q, %q", chunk, rest)
	}
}
