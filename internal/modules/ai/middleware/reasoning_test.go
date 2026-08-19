package middleware

import (
	"context"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

func TestExtractReasoningFromGenerate(t *testing.T) {
	inner := &fakeModel{generateResult: &provider.GenerateResult{
		Content: []provider.Content{
			provider.Text{Text: "<think>weighing it up</think>The answer is 42."},
		},
	}}

	model := Wrap(inner, ExtractReasoning("think"))

	res, err := model.DoGenerate(context.Background(), provider.CallOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var text, reasoning strings.Builder
	for _, c := range res.Content {
		switch v := c.(type) {
		case provider.Text:
			text.WriteString(v.Text)
		case provider.Reasoning:
			reasoning.WriteString(v.Text)
		}
	}

	if reasoning.String() != "weighing it up" {
		t.Errorf("reasoning = %q", reasoning.String())
	}
	// The tags must not survive into the answer; that is the whole point.
	if text.String() != "The answer is 42." {
		t.Errorf("text = %q", text.String())
	}
}

func TestExtractReasoningHandlesTagsSplitAcrossDeltas(t *testing.T) {
	// The tag arrives one character at a time, which is what a real gateway
	// does and what a naive implementation gets wrong.
	chunks := []string{"<th", "ink>we", "ighing", "</thi", "nk>The ans", "wer."}

	parts := []provider.StreamPart{provider.TextStart{ID: "0"}}
	for _, c := range chunks {
		parts = append(parts, provider.TextDelta{ID: "0", Delta: c})
	}
	parts = append(parts, provider.TextEnd{ID: "0"})

	model := Wrap(&fakeModel{streamParts: parts}, ExtractReasoning("think"))

	res, err := model.DoStream(context.Background(), provider.CallOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var text, reasoning strings.Builder
	for _, part := range drain(res) {
		switch v := part.(type) {
		case provider.TextDelta:
			text.WriteString(v.Delta)
		case provider.ReasoningDelta:
			reasoning.WriteString(v.Delta)
		}
	}

	if reasoning.String() != "weighing" {
		t.Errorf("reasoning = %q, want weighing", reasoning.String())
	}
	if text.String() != "The answer." {
		t.Errorf("text = %q, want the answer with no tag fragments", text.String())
	}
}

func TestExtractReasoningLeavesPlainTextAlone(t *testing.T) {
	parts := []provider.StreamPart{
		provider.TextStart{ID: "0"},
		provider.TextDelta{ID: "0", Delta: "no tags "},
		provider.TextDelta{ID: "0", Delta: "at all"},
		provider.TextEnd{ID: "0"},
	}

	model := Wrap(&fakeModel{streamParts: parts}, ExtractReasoning("think"))

	res, err := model.DoStream(context.Background(), provider.CallOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	for _, part := range drain(res) {
		if d, ok := part.(provider.TextDelta); ok {
			text.WriteString(d.Delta)
		}
		if _, ok := part.(provider.ReasoningDelta); ok {
			t.Error("plain text produced a reasoning part")
		}
	}
	if text.String() != "no tags at all" {
		t.Errorf("text = %q", text.String())
	}
}

func TestExtractReasoningClosesAnUnterminatedTag(t *testing.T) {
	// A model that hits its token limit mid-thought never closes the tag.
	parts := []provider.StreamPart{
		provider.TextStart{ID: "0"},
		provider.TextDelta{ID: "0", Delta: "<think>cut off mid-th"},
		provider.TextEnd{ID: "0"},
	}

	model := Wrap(&fakeModel{streamParts: parts}, ExtractReasoning("think"))

	res, err := model.DoStream(context.Background(), provider.CallOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var reasoning strings.Builder
	var opened, closed int
	for _, part := range drain(res) {
		switch v := part.(type) {
		case provider.ReasoningStart:
			opened++
		case provider.ReasoningDelta:
			reasoning.WriteString(v.Delta)
		case provider.ReasoningEnd:
			closed++
		}
	}

	if reasoning.String() != "cut off mid-th" {
		t.Errorf("reasoning = %q", reasoning.String())
	}
	// Unbalanced start/end parts break any consumer accumulating by block id.
	if opened != 1 || closed != 1 {
		t.Errorf("reasoning blocks opened = %d, closed = %d, want 1 and 1", opened, closed)
	}
}

func TestSplitAtPartial(t *testing.T) {
	cases := []struct {
		in, tag    string
		emit, keep string
	}{
		{"hello", "<think>", "hello", ""},
		{"hello<", "<think>", "hello", "<"},
		{"hello<th", "<think>", "hello", "<th"},
		// A complete tag is not a partial one: the caller has already cut it.
		{"<think>", "<think>", "<think>", ""},
		{"a<b", "<think>", "a<b", ""},
	}

	for _, tc := range cases {
		emit, keep := splitAtPartial(tc.in, tc.tag)
		if emit != tc.emit || keep != tc.keep {
			t.Errorf("splitAtPartial(%q, %q) = (%q, %q), want (%q, %q)",
				tc.in, tc.tag, emit, keep, tc.emit, tc.keep)
		}
	}
}
