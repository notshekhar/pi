package middleware

import (
	"context"
	"time"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// EmbedFunc is an embedding call.
type EmbedFunc func(ctx context.Context, opts provider.EmbeddingCallOptions) (*provider.EmbeddingResult, error)

// EmbeddingMiddleware is one layer of behaviour around an embedding model.
type EmbeddingMiddleware struct {
	// Name labels the layer in logs and errors.
	Name string

	// TransformOptions edits the call options on the way in.
	TransformOptions func(ctx context.Context, info Info, opts provider.EmbeddingCallOptions) (provider.EmbeddingCallOptions, error)

	// WrapEmbed wraps the call itself. It must invoke next to let the call
	// proceed — or not, to serve a cached result.
	WrapEmbed func(ctx context.Context, info Info, opts provider.EmbeddingCallOptions, next EmbedFunc) (*provider.EmbeddingResult, error)

	// OverrideProvider and OverrideModelID change what the wrapped model
	// reports.
	OverrideProvider string
	OverrideModelID  string
}

// WrapEmbedding returns an embedding model with the middleware applied. As
// with Wrap, the first listed is the outermost.
func WrapEmbedding(model provider.EmbeddingModel, middlewares ...EmbeddingMiddleware) provider.EmbeddingModel {
	if len(middlewares) == 0 {
		return model
	}
	return &wrappedEmbedding{inner: model, middlewares: middlewares}
}

// wrappedEmbedding is an embedding model with a middleware chain around it.
type wrappedEmbedding struct {
	inner       provider.EmbeddingModel
	middlewares []EmbeddingMiddleware
}

// Unwrap returns the model underneath.
func (w *wrappedEmbedding) Unwrap() provider.EmbeddingModel { return w.inner }

func (w *wrappedEmbedding) SpecificationVersion() string { return w.inner.SpecificationVersion() }
func (w *wrappedEmbedding) MaxEmbeddingsPerCall() int    { return w.inner.MaxEmbeddingsPerCall() }
func (w *wrappedEmbedding) SupportsParallelCalls() bool  { return w.inner.SupportsParallelCalls() }

func (w *wrappedEmbedding) Provider() string {
	name := w.inner.Provider()
	for _, m := range w.middlewares {
		if m.OverrideProvider != "" {
			name = m.OverrideProvider
		}
	}
	return name
}

func (w *wrappedEmbedding) ModelID() string {
	id := w.inner.ModelID()
	for _, m := range w.middlewares {
		if m.OverrideModelID != "" {
			id = m.OverrideModelID
		}
	}
	return id
}

// DoEmbed implements provider.EmbeddingModel.
func (w *wrappedEmbedding) DoEmbed(ctx context.Context, opts provider.EmbeddingCallOptions) (*provider.EmbeddingResult, error) {
	info := Info{Provider: w.Provider(), ModelID: w.ModelID()}
	next := w.inner.DoEmbed

	// Built inside out so the first middleware ends up outermost.
	for i := len(w.middlewares) - 1; i >= 0; i-- {
		next = w.middlewares[i].link(info, next)
	}
	return next(ctx, opts)
}

// link wraps one middleware around the rest of the chain.
func (m EmbeddingMiddleware) link(info Info, next EmbedFunc) EmbedFunc {
	return func(ctx context.Context, opts provider.EmbeddingCallOptions) (*provider.EmbeddingResult, error) {
		if m.TransformOptions != nil {
			transformed, err := m.TransformOptions(ctx, info, opts)
			if err != nil {
				return nil, err
			}
			opts = transformed
		}
		if m.WrapEmbed == nil {
			return next(ctx, opts)
		}
		return m.WrapEmbed(ctx, info, opts, next)
	}
}

// EmbeddingSettings are call options applied when the caller left them unset.
type EmbeddingSettings struct {
	Dimensions      int
	Headers         provider.Headers
	ProviderOptions provider.ProviderOptions
}

// DefaultEmbeddingSettings fills in options the caller did not set.
//
// This is how a program applies a house task type or vector size once rather
// than at every call site.
func DefaultEmbeddingSettings(s EmbeddingSettings) EmbeddingMiddleware {
	return EmbeddingMiddleware{
		Name: "default-embedding-settings",
		TransformOptions: func(_ context.Context, _ Info, opts provider.EmbeddingCallOptions) (provider.EmbeddingCallOptions, error) {
			if opts.Dimensions == 0 {
				opts.Dimensions = s.Dimensions
			}
			opts.Headers = mergeHeaders(s.Headers, opts.Headers)
			opts.ProviderOptions = mergeProviderOptions(s.ProviderOptions, opts.ProviderOptions)
			return opts, nil
		},
	}
}

// EmbeddingRecord is one completed embedding call.
type EmbeddingRecord struct {
	Provider string
	ModelID  string

	// Values is how many texts were embedded in this call. A batch split by
	// ai.EmbedMany produces one record per request, not one per run.
	Values int

	Duration time.Duration
	Usage    provider.EmbeddingUsage
	Err      error
}

// ObserveEmbedding reports every completed embedding call to fn, which is the
// hook for cost tracking.
func ObserveEmbedding(fn func(EmbeddingRecord)) EmbeddingMiddleware {
	return EmbeddingMiddleware{
		Name: "observe-embedding",
		WrapEmbed: func(ctx context.Context, info Info, opts provider.EmbeddingCallOptions, next EmbedFunc) (*provider.EmbeddingResult, error) {
			start := time.Now()
			res, err := next(ctx, opts)

			record := EmbeddingRecord{
				Provider: info.Provider,
				ModelID:  info.ModelID,
				Values:   len(opts.Values),
				Duration: time.Since(start),
				Err:      err,
			}
			if res != nil {
				record.Usage = res.Usage
			}
			fn(record)

			return res, err
		},
	}
}
