package ai_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

type review struct {
	Summary string   `json:"summary"`
	Score   int      `json:"score"`
	Tags    []string `json:"tags,omitempty"`
}

func TestGenerateObjectDecodesReply(t *testing.T) {
	model := &mockModel{turns: []mockTurn{{parts: []provider.StreamPart{
		provider.Text{Text: `{"summary":"good","score":4,"tags":["go"]}`},
	}}}}

	res, err := ai.GenerateObject[review](context.Background(), ai.Options{
		Model:    model,
		Messages: []provider.Message{ai.UserText("review this")},
	})
	if err != nil {
		t.Fatal(err)
	}

	if res.Object.Summary != "good" || res.Object.Score != 4 {
		t.Errorf("object = %+v", res.Object)
	}
	if len(res.Object.Tags) != 1 || res.Object.Tags[0] != "go" {
		t.Errorf("tags = %v", res.Object.Tags)
	}

	// The provider has to be told what shape to produce, or the model is only
	// being asked nicely.
	format := model.options[0].ResponseFormat
	if format == nil || format.Type != "json" {
		t.Fatalf("response format = %+v, want json", format)
	}
	if format.Schema == nil {
		t.Fatal("response format carries no schema")
	}
	if _, ok := format.Schema.Properties.Get("summary"); !ok {
		t.Errorf("schema has no summary property: %+v", format.Schema)
	}
}

func TestGenerateObjectStripsCodeFence(t *testing.T) {
	model := &mockModel{turns: []mockTurn{{parts: []provider.StreamPart{
		provider.Text{Text: "```json\n{\"summary\":\"fenced\",\"score\":1}\n```"},
	}}}}

	res, err := ai.GenerateObject[review](context.Background(), ai.Options{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if res.Object.Summary != "fenced" {
		t.Errorf("summary = %q", res.Object.Summary)
	}
}

func TestGenerateObjectReportsUnparseableOutput(t *testing.T) {
	model := &mockModel{turns: []mockTurn{{parts: []provider.StreamPart{
		provider.Text{Text: "I cannot do that"},
	}}}}

	_, err := ai.GenerateObject[review](context.Background(), ai.Options{Model: model})

	var parseErr *ai.ObjectParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("err = %v, want *ai.ObjectParseError", err)
	}
	// The raw output is the only way to debug a model that refused.
	if parseErr.Text != "I cannot do that" {
		t.Errorf("raw text = %q", parseErr.Text)
	}
}

func TestGenerateObjectRejectsTools(t *testing.T) {
	_, err := ai.GenerateObject[review](context.Background(), ai.Options{
		Model: &mockModel{},
		Tools: []ai.Tool{ai.NewTool("noop", "does nothing",
			func(context.Context, struct{}) (ai.ToolResult, error) { return ai.ToolText(""), nil })},
	})
	if err == nil || !strings.Contains(err.Error(), "do not accept tools") {
		t.Fatalf("err = %v, want a tools rejection", err)
	}
}

func TestStreamObjectEmitsGrowingSnapshots(t *testing.T) {
	chunks := []string{`{"summ`, `ary":"gr`, `owing","sc`, `ore":5}`}

	parts := []provider.StreamPart{provider.TextStart{ID: "0"}}
	for _, c := range chunks {
		parts = append(parts, provider.TextDelta{ID: "0", Delta: c})
	}
	parts = append(parts,
		provider.TextEnd{ID: "0"},
		provider.Finish{FinishReason: provider.FinishReason{Unified: provider.FinishStop}},
	)

	res, err := ai.StreamObject[review](context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{{parts: parts}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var summaries []string
	for part := range res.Stream {
		summaries = append(summaries, part.Snapshot.Summary)
	}

	final, err := res.Final()
	if err != nil {
		t.Fatal(err)
	}
	if final.Object.Summary != "growing" || final.Object.Score != 5 {
		t.Errorf("final object = %+v", final.Object)
	}

	// The point of streaming is seeing the value before it is finished.
	if len(summaries) < 2 {
		t.Fatalf("snapshots = %v, want the summary to fill in progressively", summaries)
	}
	if last := summaries[len(summaries)-1]; last != "growing" {
		t.Errorf("last snapshot summary = %q, want growing", last)
	}
}

func TestStreamObjectReportsUnparseableOutput(t *testing.T) {
	res, err := ai.StreamObject[review](context.Background(), ai.Options{
		Model: &mockModel{turns: []mockTurn{{parts: []provider.StreamPart{
			provider.TextDelta{ID: "0", Delta: "sorry, no"},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range res.Stream { //nolint:revive // draining
	}

	if _, err := res.Final(); err == nil {
		t.Fatal("Final() = nil error, want a parse failure")
	}
}
