package ai_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// mockEmbedder records the batches it was given and returns a vector encoding
// each value's position, so misalignment is visible in the result.
type mockEmbedder struct {
	maxPerCall int
	parallel   bool
	err        error
	// delay makes the first batch finish last, which is what exposes a
	// result assembled in completion order.
	reverseOrder bool

	mu                    sync.Mutex
	batches               [][]string
	inFlight, maxInFlight int
}

func (m *mockEmbedder) SpecificationVersion() string { return provider.EmbeddingSpecificationVersion }
func (m *mockEmbedder) Provider() string             { return "mock" }
func (m *mockEmbedder) ModelID() string              { return "mock-embed" }
func (m *mockEmbedder) MaxEmbeddingsPerCall() int    { return m.maxPerCall }
func (m *mockEmbedder) SupportsParallelCalls() bool  { return m.parallel }

func (m *mockEmbedder) DoEmbed(ctx context.Context, opts provider.EmbeddingCallOptions) (*provider.EmbeddingResult, error) {
	m.mu.Lock()
	m.batches = append(m.batches, opts.Values)
	m.inFlight++
	if m.inFlight > m.maxInFlight {
		m.maxInFlight = m.inFlight
	}
	first := len(m.batches) == 1
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.inFlight--
		m.mu.Unlock()
	}()

	if m.err != nil {
		return nil, m.err
	}
	if m.reverseOrder && first {
		// Let the later batches overtake this one.
		for range 100 {
			m.mu.Lock()
			done := len(m.batches) > 1
			m.mu.Unlock()
			if done {
				break
			}
		}
	}

	tokens := int64(len(opts.Values))
	res := &provider.EmbeddingResult{Usage: provider.EmbeddingUsage{Tokens: &tokens}}
	for _, v := range opts.Values {
		// The vector is the value itself, so a shuffled result is detectable.
		res.Embeddings = append(res.Embeddings, provider.Embedding{float32(len(v)), vectorFor(v)})
	}
	return res, nil
}

// vectorFor turns a value like "v7" into 7.
func vectorFor(value string) float32 {
	var n float32
	for _, c := range value {
		if c >= '0' && c <= '9' {
			n = n*10 + float32(c-'0')
		}
	}
	return n
}

func TestEmbedManyBatchesToTheModelsLimit(t *testing.T) {
	model := &mockEmbedder{maxPerCall: 3}

	values := make([]string, 7)
	for i := range values {
		values[i] = fmt.Sprintf("v%d", i)
	}

	res, err := ai.EmbedMany(context.Background(), values, ai.EmbedOptions{Model: model})
	if err != nil {
		t.Fatal(err)
	}

	if len(model.batches) != 3 {
		t.Errorf("batches = %d, want 3 (3+3+1)", len(model.batches))
	}
	for i, b := range model.batches {
		if len(b) > 3 {
			t.Errorf("batch %d has %d values, over the model's limit of 3", i, len(b))
		}
	}
	if len(res.Embeddings) != 7 {
		t.Fatalf("embeddings = %d, want 7", len(res.Embeddings))
	}
	if res.Usage.Tokens == nil || *res.Usage.Tokens != 7 {
		t.Errorf("usage = %v, want 7 summed across batches", res.Usage.Tokens)
	}
}

func TestEmbedManyKeepsInputOrder(t *testing.T) {
	// Batches finish out of order; the result must not.
	model := &mockEmbedder{maxPerCall: 2, parallel: true, reverseOrder: true}

	values := make([]string, 6)
	for i := range values {
		values[i] = fmt.Sprintf("v%d", i)
	}

	res, err := ai.EmbedMany(context.Background(), values, ai.EmbedOptions{Model: model})
	if err != nil {
		t.Fatal(err)
	}

	for i, e := range res.Embeddings {
		if got := e[1]; got != float32(i) {
			t.Fatalf("embedding %d encodes value %v; the result was assembled in completion order", i, got)
		}
	}
}

func TestEmbedManyRunsSeriallyWhenTheModelForbidsParallel(t *testing.T) {
	model := &mockEmbedder{maxPerCall: 1, parallel: false}

	values := []string{"a", "b", "c", "d"}
	if _, err := ai.EmbedMany(context.Background(), values, ai.EmbedOptions{Model: model}); err != nil {
		t.Fatal(err)
	}

	if model.maxInFlight != 1 {
		t.Errorf("max in flight = %d, want 1: the model said it cannot take parallel calls",
			model.maxInFlight)
	}
}

func TestEmbedManyReportsWhichBatchFailed(t *testing.T) {
	model := &mockEmbedder{maxPerCall: 2, err: errors.New("upstream down")}

	_, err := ai.EmbedMany(context.Background(), []string{"a", "b", "c"}, ai.EmbedOptions{Model: model})
	if err == nil {
		t.Fatal("err = nil, want the batch failure")
	}
	if !strings.Contains(err.Error(), "upstream down") || !strings.Contains(err.Error(), "batch") {
		t.Errorf("err = %v, want it to name the batch and the cause", err)
	}
}

func TestEmbedReturnsOneVector(t *testing.T) {
	model := &mockEmbedder{maxPerCall: 10}

	vec, err := ai.Embed(context.Background(), "v3", ai.EmbedOptions{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != 2 || vec[1] != 3 {
		t.Errorf("embedding = %v", vec)
	}
}

func TestCosineSimilarity(t *testing.T) {
	same, err := ai.CosineSimilarity(provider.Embedding{1, 0}, provider.Embedding{2, 0})
	if err != nil {
		t.Fatal(err)
	}
	if same < 0.999 {
		t.Errorf("parallel vectors = %v, want 1", same)
	}

	orthogonal, err := ai.CosineSimilarity(provider.Embedding{1, 0}, provider.Embedding{0, 1})
	if err != nil {
		t.Fatal(err)
	}
	if orthogonal > 0.001 || orthogonal < -0.001 {
		t.Errorf("orthogonal vectors = %v, want 0", orthogonal)
	}

	// Comparing different lengths would return a number that looks fine.
	if _, err := ai.CosineSimilarity(provider.Embedding{1}, provider.Embedding{1, 2}); err == nil {
		t.Error("comparing mismatched lengths did not fail")
	}
	if _, err := ai.CosineSimilarity(provider.Embedding{0, 0}, provider.Embedding{1, 2}); err == nil {
		t.Error("comparing a zero vector did not fail")
	}
}
