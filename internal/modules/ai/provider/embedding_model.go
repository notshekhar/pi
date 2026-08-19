package provider

import "context"

// EmbeddingSpecificationVersion is the embedding model spec revision this
// package implements.
const EmbeddingSpecificationVersion = "v3"

// Embedding is one vector. Values are in the model's own scale: some providers
// return normalised vectors and some do not, so a caller comparing across
// models has to normalise itself.
type Embedding []float32

// EmbeddingCallOptions is the input to an embedding call.
type EmbeddingCallOptions struct {
	// Values are the texts to embed. A model reports its own limit through
	// MaxEmbeddingsPerCall; sending more than that is the caller's error.
	Values []string

	// Dimensions requests a shortened vector, for models supporting Matryoshka
	// truncation. Zero uses the model's native size.
	Dimensions int

	// Headers are extra HTTP headers for this call only.
	Headers Headers

	// ProviderOptions carries provider-specific settings, keyed by provider id.
	ProviderOptions ProviderOptions
}

// EmbeddingUsage reports what an embedding call cost. Tokens is nil when the
// provider does not report it, which is distinct from zero.
type EmbeddingUsage struct {
	Tokens *int64
}

// EmbeddingResult is the outcome of an embedding call. Embeddings are in the
// order of the input values.
type EmbeddingResult struct {
	Embeddings []Embedding
	Usage      EmbeddingUsage

	ProviderMetadata ProviderMetadata
	Response         *ResponseInfo
	Warnings         []Warning
}

// EmbeddingModel turns text into vectors.
//
// Application code should call ai.Embed or ai.EmbedMany, which add batching,
// parallelism and retry on top.
type EmbeddingModel interface {
	// SpecificationVersion reports the spec revision, always "v3".
	SpecificationVersion() string

	// Provider is the provider id, e.g. "google".
	Provider() string

	// ModelID is the provider-specific model id.
	ModelID() string

	// MaxEmbeddingsPerCall is how many values fit in one request. Zero means
	// no limit is known, in which case a caller should batch conservatively.
	MaxEmbeddingsPerCall() int

	// SupportsParallelCalls reports whether several requests may be in flight
	// at once. Providers with strict per-key rate limits say no.
	SupportsParallelCalls() bool

	// DoEmbed embeds a batch of values.
	DoEmbed(ctx context.Context, opts EmbeddingCallOptions) (*EmbeddingResult, error)
}
