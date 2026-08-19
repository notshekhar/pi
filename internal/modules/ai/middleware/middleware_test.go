package middleware

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// fakeModel records what it was called with and replays a scripted result.
type fakeModel struct {
	generateResult *provider.GenerateResult
	streamParts    []provider.StreamPart
	err            error

	seen  provider.CallOptions
	calls int
}

func (m *fakeModel) SpecificationVersion() string { return provider.SpecificationVersion }
func (m *fakeModel) Provider() string             { return "fake" }
func (m *fakeModel) ModelID() string              { return "fake-1" }

func (m *fakeModel) SupportedURLs(context.Context) (map[string][]*regexp.Regexp, error) {
	return nil, nil
}

func (m *fakeModel) DoGenerate(_ context.Context, opts provider.CallOptions) (*provider.GenerateResult, error) {
	m.seen = opts
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	if m.generateResult != nil {
		// Copy so a middleware editing the result cannot corrupt the script.
		res := *m.generateResult
		return &res, nil
	}
	return &provider.GenerateResult{}, nil
}

func (m *fakeModel) DoStream(_ context.Context, opts provider.CallOptions) (*provider.StreamResult, error) {
	m.seen = opts
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	ch := make(chan provider.StreamPart, len(m.streamParts)+1)
	for _, p := range m.streamParts {
		ch <- p
	}
	close(ch)
	return &provider.StreamResult{Stream: ch}, nil
}

// drain collects a stream.
func drain(res *provider.StreamResult) []provider.StreamPart {
	var parts []provider.StreamPart
	for p := range res.Stream {
		parts = append(parts, p)
	}
	return parts
}

func TestWrapAppliesOutermostFirst(t *testing.T) {
	var order []string

	label := func(name string) Middleware {
		return Middleware{
			Name: name,
			TransformOptions: func(_ context.Context, _ Info, opts provider.CallOptions) (provider.CallOptions, error) {
				order = append(order, name)
				opts.StopSequences = append(opts.StopSequences, name)
				return opts, nil
			},
		}
	}

	inner := &fakeModel{}
	model := Wrap(inner, label("outer"), label("inner"))

	if _, err := model.DoGenerate(context.Background(), provider.CallOptions{}); err != nil {
		t.Fatal(err)
	}

	// The first middleware listed must see the call first: that is the reading
	// order the caller wrote.
	if strings.Join(order, ",") != "outer,inner" {
		t.Errorf("order = %v, want outer then inner", order)
	}
	if got := strings.Join(inner.seen.StopSequences, ","); got != "outer,inner" {
		t.Errorf("stop sequences = %q, want both transforms applied in order", got)
	}
}

func TestTransformErrorStopsTheCall(t *testing.T) {
	sentinel := errors.New("nope")
	inner := &fakeModel{}

	model := Wrap(inner, Middleware{
		TransformOptions: func(context.Context, Info, provider.CallOptions) (provider.CallOptions, error) {
			return provider.CallOptions{}, sentinel
		},
	})

	if _, err := model.DoGenerate(context.Background(), provider.CallOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the transform's error", err)
	}
	if inner.calls != 0 {
		t.Error("the model was called despite a failed transform")
	}
}

func TestDefaultSettingsDoesNotOverrideExplicitValues(t *testing.T) {
	zero := 0.0
	defaultTemp := 0.7
	maxTokens := int64(4096)

	inner := &fakeModel{}
	model := Wrap(inner, DefaultSettings(Settings{
		Temperature:     &defaultTemp,
		MaxOutputTokens: &maxTokens,
		Reasoning:       provider.ReasoningHigh,
		Headers:         provider.Headers{"x-default": "1", "x-both": "default"},
	}))

	_, err := model.DoGenerate(context.Background(), provider.CallOptions{
		// Temperature 0 is a deliberate setting, not an absent one.
		Temperature: &zero,
		Reasoning:   provider.ReasoningLow,
		Headers:     provider.Headers{"x-both": "call"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if *inner.seen.Temperature != 0 {
		t.Errorf("temperature = %v, want the caller's 0", *inner.seen.Temperature)
	}
	if inner.seen.Reasoning != provider.ReasoningLow {
		t.Errorf("reasoning = %q, want the caller's low", inner.seen.Reasoning)
	}
	if inner.seen.MaxOutputTokens == nil || *inner.seen.MaxOutputTokens != 4096 {
		t.Errorf("max tokens = %v, want the default filled in", inner.seen.MaxOutputTokens)
	}
	if inner.seen.Headers["x-default"] != "1" {
		t.Error("default header was dropped")
	}
	if inner.seen.Headers["x-both"] != "call" {
		t.Errorf("x-both = %q, want the call to win", inner.seen.Headers["x-both"])
	}
}

func TestObserveRecordsGenerate(t *testing.T) {
	total := int64(120)
	inner := &fakeModel{generateResult: &provider.GenerateResult{
		FinishReason: provider.FinishReason{Unified: provider.FinishStop},
		Usage:        provider.Usage{InputTokens: provider.InputTokens{Total: &total}},
	}}

	var records []Record
	model := Wrap(inner, Observe(func(r Record) { records = append(records, r) }))

	if _, err := model.DoGenerate(context.Background(), provider.CallOptions{}); err != nil {
		t.Fatal(err)
	}

	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	r := records[0]
	if r.Provider != "fake" || r.ModelID != "fake-1" {
		t.Errorf("record identity = %s/%s", r.Provider, r.ModelID)
	}
	if r.Usage.InputTokens.Total == nil || *r.Usage.InputTokens.Total != 120 {
		t.Error("usage was not carried into the record")
	}
	if r.Stream {
		t.Error("a generate call was recorded as streaming")
	}
}

func TestObserveRecordsStreamAtTheEnd(t *testing.T) {
	total := int64(7)
	inner := &fakeModel{streamParts: []provider.StreamPart{
		provider.TextDelta{ID: "0", Delta: "hi"},
		provider.Finish{
			FinishReason: provider.FinishReason{Unified: provider.FinishStop},
			Usage:        provider.Usage{OutputTokens: provider.OutputTokens{Total: &total}},
		},
	}}

	var records []Record
	model := Wrap(inner, Observe(func(r Record) { records = append(records, r) }))

	res, err := model.DoStream(context.Background(), provider.CallOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Totals only exist at the end, so nothing should have been recorded yet.
	parts := drain(res)
	if len(parts) != 2 {
		t.Errorf("forwarded %d parts, want every part passed through", len(parts))
	}

	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if !records[0].Stream {
		t.Error("a stream call was not marked as streaming")
	}
	if records[0].Usage.OutputTokens.Total == nil || *records[0].Usage.OutputTokens.Total != 7 {
		t.Error("usage from the Finish part did not reach the record")
	}
}

func TestSimulateStreamingReplaysAGenerateCall(t *testing.T) {
	inner := &fakeModel{generateResult: &provider.GenerateResult{
		Content: []provider.Content{
			provider.Reasoning{Text: "thinking"},
			provider.Text{Text: "answer"},
			provider.ToolCall{ToolCallID: "1", ToolName: "read", Input: "{}"},
		},
		FinishReason: provider.FinishReason{Unified: provider.FinishToolCalls},
	}}

	model := Wrap(inner, SimulateStreaming())

	res, err := model.DoStream(context.Background(), provider.CallOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var text, reasoning strings.Builder
	var sawToolCall, sawFinish bool
	for _, part := range drain(res) {
		switch v := part.(type) {
		case provider.TextDelta:
			text.WriteString(v.Delta)
		case provider.ReasoningDelta:
			reasoning.WriteString(v.Delta)
		case provider.ToolCall:
			sawToolCall = true
		case provider.Finish:
			sawFinish = true
		}
	}

	if text.String() != "answer" || reasoning.String() != "thinking" {
		t.Errorf("text = %q, reasoning = %q", text.String(), reasoning.String())
	}
	if !sawToolCall {
		t.Error("the tool call did not survive the replay")
	}
	if !sawFinish {
		t.Error("no Finish part was emitted")
	}
	// The point of the shim: the model itself was never asked to stream.
	if inner.calls != 1 {
		t.Errorf("model calls = %d, want exactly one generate call", inner.calls)
	}
}

func TestOverridesChangeReportedIdentity(t *testing.T) {
	model := Wrap(&fakeModel{}, Middleware{
		OverrideProvider: "gateway",
		OverrideModelID:  "vendor/model",
	})

	if model.Provider() != "gateway" || model.ModelID() != "vendor/model" {
		t.Errorf("identity = %s/%s, want the overrides", model.Provider(), model.ModelID())
	}
}

// fakeEmbedder records what it was called with.
type fakeEmbedder struct {
	seen  provider.EmbeddingCallOptions
	calls int
}

func (m *fakeEmbedder) SpecificationVersion() string {
	return provider.EmbeddingSpecificationVersion
}
func (m *fakeEmbedder) Provider() string            { return "fake" }
func (m *fakeEmbedder) ModelID() string             { return "fake-embed" }
func (m *fakeEmbedder) MaxEmbeddingsPerCall() int   { return 8 }
func (m *fakeEmbedder) SupportsParallelCalls() bool { return true }

func (m *fakeEmbedder) DoEmbed(_ context.Context, opts provider.EmbeddingCallOptions) (*provider.EmbeddingResult, error) {
	m.seen = opts
	m.calls++
	tokens := int64(len(opts.Values))
	return &provider.EmbeddingResult{
		Embeddings: make([]provider.Embedding, len(opts.Values)),
		Usage:      provider.EmbeddingUsage{Tokens: &tokens},
	}, nil
}

// fakeImager records what it was called with.
type fakeImager struct {
	produce int
	seen    provider.ImageCallOptions
}

func (m *fakeImager) SpecificationVersion() string { return provider.ImageSpecificationVersion }
func (m *fakeImager) Provider() string             { return "fake" }
func (m *fakeImager) ModelID() string              { return "fake-image" }
func (m *fakeImager) MaxImagesPerCall() int        { return 4 }

func (m *fakeImager) DoGenerate(_ context.Context, opts provider.ImageCallOptions) (*provider.ImageResult, error) {
	m.seen = opts
	return &provider.ImageResult{Images: make([]provider.GeneratedImage, m.produce)}, nil
}

func TestWrapEmbeddingAppliesDefaultsAndObserves(t *testing.T) {
	inner := &fakeEmbedder{}
	var records []EmbeddingRecord

	model := WrapEmbedding(inner,
		ObserveEmbedding(func(r EmbeddingRecord) { records = append(records, r) }),
		DefaultEmbeddingSettings(EmbeddingSettings{
			Dimensions:      256,
			ProviderOptions: provider.ProviderOptions{"google": {"taskType": "RETRIEVAL_DOCUMENT"}},
		}),
	)

	res, err := model.DoEmbed(context.Background(), provider.EmbeddingCallOptions{
		Values: []string{"a", "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Embeddings) != 2 {
		t.Fatalf("embeddings = %d", len(res.Embeddings))
	}

	if inner.seen.Dimensions != 256 {
		t.Errorf("dimensions = %d, want the default filled in", inner.seen.Dimensions)
	}
	if inner.seen.ProviderOptions["google"]["taskType"] != "RETRIEVAL_DOCUMENT" {
		t.Errorf("provider options = %v, want the default task type", inner.seen.ProviderOptions)
	}

	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Values != 2 || records[0].ModelID != "fake-embed" {
		t.Errorf("record = %+v", records[0])
	}
	if records[0].Usage.Tokens == nil || *records[0].Usage.Tokens != 2 {
		t.Errorf("usage = %v, want it carried into the record", records[0].Usage.Tokens)
	}
}

func TestEmbeddingDefaultsDoNotOverrideExplicitValues(t *testing.T) {
	inner := &fakeEmbedder{}

	model := WrapEmbedding(inner, DefaultEmbeddingSettings(EmbeddingSettings{Dimensions: 256}))

	if _, err := model.DoEmbed(context.Background(), provider.EmbeddingCallOptions{
		Values:     []string{"a"},
		Dimensions: 1024,
	}); err != nil {
		t.Fatal(err)
	}

	if inner.seen.Dimensions != 1024 {
		t.Errorf("dimensions = %d, want the caller's 1024", inner.seen.Dimensions)
	}
}

func TestWrapImageAppliesDefaultsAndObserves(t *testing.T) {
	inner := &fakeImager{produce: 1}
	var records []ImageRecord

	model := WrapImage(inner,
		ObserveImage(func(r ImageRecord) { records = append(records, r) }),
		DefaultImageSettings(ImageSettings{Size: "1024x1024"}),
	)

	// Two asked for, one produced: the gap is what a safety filter looks like.
	if _, err := model.DoGenerate(context.Background(), provider.ImageCallOptions{
		Prompt: "a lighthouse",
		N:      2,
	}); err != nil {
		t.Fatal(err)
	}

	if inner.seen.Size != "1024x1024" {
		t.Errorf("size = %q, want the default", inner.seen.Size)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
	if records[0].Requested != 2 || records[0].Produced != 1 {
		t.Errorf("record = %+v, want 2 requested and 1 produced", records[0])
	}
}

func TestImageDefaultsDoNotBuildAnImpossibleRequest(t *testing.T) {
	inner := &fakeImager{produce: 1}

	model := WrapImage(inner, DefaultImageSettings(ImageSettings{Size: "1024x1024"}))

	if _, err := model.DoGenerate(context.Background(), provider.ImageCallOptions{
		Prompt:      "a lighthouse",
		AspectRatio: "16:9",
	}); err != nil {
		t.Fatal(err)
	}

	// Size and AspectRatio are alternatives; filling in a default size next to
	// an explicit ratio makes a request the provider rejects.
	if inner.seen.Size != "" {
		t.Errorf("size = %q, want it left unset beside an explicit ratio", inner.seen.Size)
	}
	if inner.seen.AspectRatio != "16:9" {
		t.Errorf("aspect ratio = %q", inner.seen.AspectRatio)
	}
}

func TestWrappedModelsKeepTheirCapabilities(t *testing.T) {
	embedding := WrapEmbedding(&fakeEmbedder{}, DefaultEmbeddingSettings(EmbeddingSettings{}))
	image := WrapImage(&fakeImager{}, DefaultImageSettings(ImageSettings{}))

	// ai.EmbedMany and ai.GenerateImage batch against these; a wrapper
	// reporting zero would silently change how the work is split.
	if embedding.MaxEmbeddingsPerCall() != 8 {
		t.Errorf("max embeddings = %d, want the inner model's 8", embedding.MaxEmbeddingsPerCall())
	}
	if !embedding.SupportsParallelCalls() {
		t.Error("parallel support was lost through the wrapper")
	}
	if image.MaxImagesPerCall() != 4 {
		t.Errorf("max images = %d, want the inner model's 4", image.MaxImagesPerCall())
	}
}
