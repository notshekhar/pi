package ai

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// defaultEmbedBatchSize is used when a model reports no limit of its own. It
// is small enough to stay inside every provider's request size ceiling.
const defaultEmbedBatchSize = 96

// defaultEmbedParallelism bounds how many batches are in flight at once for a
// model that allows parallel calls. Higher mostly buys rate-limit errors.
const defaultEmbedParallelism = 4

// EmbedOptions configures an embedding call.
type EmbedOptions struct {
	// Model is required.
	Model provider.EmbeddingModel

	// Dimensions requests a shortened vector where the model supports it.
	// Zero uses the model's native size.
	Dimensions int

	// MaxParallelCalls bounds concurrent requests. Zero uses a small default,
	// and one forces the batches to run in series.
	MaxParallelCalls int

	// Headers are extra HTTP headers for every call.
	Headers provider.Headers

	// ProviderOptions carries provider-specific settings, keyed by provider id.
	ProviderOptions provider.ProviderOptions
}

// EmbedResult is the outcome of an EmbedMany call.
type EmbedResult struct {
	// Embeddings are in the order of the input values.
	Embeddings []provider.Embedding

	// Usage is the total across every batch.
	Usage provider.EmbeddingUsage

	// Warnings is the union of the warnings from every batch.
	Warnings []provider.Warning
}

// Embed turns one value into a vector.
func Embed(ctx context.Context, value string, opts EmbedOptions) (provider.Embedding, error) {
	res, err := EmbedMany(ctx, []string{value}, opts)
	if err != nil {
		return nil, err
	}
	if len(res.Embeddings) == 0 {
		return nil, errors.New("pi: the model returned no embedding")
	}
	return res.Embeddings[0], nil
}

// EmbedMany turns a list of values into vectors, splitting the work into
// batches the model accepts and running them in parallel where it allows.
//
// The result is in input order regardless of the order the batches finish in,
// so an embedding always lines up with the value it came from.
func EmbedMany(ctx context.Context, values []string, opts EmbedOptions) (*EmbedResult, error) {
	if opts.Model == nil {
		return nil, errors.New("pi: EmbedOptions.Model is required")
	}
	if len(values) == 0 {
		return &EmbedResult{}, nil
	}

	batches := chunk(values, embedBatchSize(opts.Model))
	results := make([]*provider.EmbeddingResult, len(batches))

	parallel := embedParallelism(opts)
	if err := forEachParallel(ctx, len(batches), parallel, func(ctx context.Context, i int) error {
		res, err := opts.Model.DoEmbed(ctx, provider.EmbeddingCallOptions{
			Values:          batches[i],
			Dimensions:      opts.Dimensions,
			Headers:         opts.Headers,
			ProviderOptions: opts.ProviderOptions,
		})
		if err != nil {
			return fmt.Errorf("pi: embedding batch %d of %d: %w", i+1, len(batches), err)
		}
		if len(res.Embeddings) != len(batches[i]) {
			// A short batch would silently misalign every embedding after it,
			// which is far worse than failing here.
			return fmt.Errorf("pi: batch %d returned %d embeddings for %d values",
				i+1, len(res.Embeddings), len(batches[i]))
		}
		results[i] = res
		return nil
	}); err != nil {
		return nil, err
	}

	out := &EmbedResult{Embeddings: make([]provider.Embedding, 0, len(values))}
	for _, res := range results {
		out.Embeddings = append(out.Embeddings, res.Embeddings...)
		out.Warnings = append(out.Warnings, res.Warnings...)
		out.Usage.Tokens = addTokens(out.Usage.Tokens, res.Usage.Tokens)
	}
	return out, nil
}

// embedBatchSize resolves how many values go in one request.
func embedBatchSize(model provider.EmbeddingModel) int {
	if n := model.MaxEmbeddingsPerCall(); n > 0 {
		return n
	}
	return defaultEmbedBatchSize
}

// embedParallelism resolves how many requests may be in flight.
func embedParallelism(opts EmbedOptions) int {
	if !opts.Model.SupportsParallelCalls() {
		return 1
	}
	if opts.MaxParallelCalls > 0 {
		return opts.MaxParallelCalls
	}
	return defaultEmbedParallelism
}

// chunk splits values into slices of at most size.
func chunk(values []string, size int) [][]string {
	if size <= 0 {
		size = defaultEmbedBatchSize
	}
	batches := make([][]string, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := min(start+size, len(values))
		batches = append(batches, values[start:end])
	}
	return batches
}

// forEachParallel runs fn for each index with at most parallel running at
// once, returning the first error and cancelling the rest.
func forEachParallel(ctx context.Context, n, parallel int, fn func(context.Context, int) error) error {
	if n == 0 {
		return nil
	}
	if parallel < 1 {
		parallel = 1
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		sem      = make(chan struct{}, parallel)
	)

	for i := range n {
		wg.Add(1)
		sem <- struct{}{}

		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			if ctx.Err() != nil {
				return
			}
			if err := fn(ctx, i); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	return firstErr
}

// CosineSimilarity measures how alike two embeddings are, from -1 to 1.
//
// It is the usual way to rank embeddings against a query. Vectors of different
// lengths cannot be compared, and an error says so rather than returning a
// number that looks meaningful.
func CosineSimilarity(a, b provider.Embedding) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("pi: cannot compare embeddings of length %d and %d", len(a), len(b))
	}
	if len(a) == 0 {
		return 0, errors.New("pi: cannot compare empty embeddings")
	}

	var dot, normA, normB float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		normA += x * x
		normB += y * y
	}

	if normA == 0 || normB == 0 {
		// A zero vector has no direction, so similarity is undefined rather
		// than zero.
		return 0, errors.New("pi: cannot compare a zero embedding")
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB)), nil
}
