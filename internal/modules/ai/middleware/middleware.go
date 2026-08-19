// Package middleware wraps a language model in behaviour that is not the
// provider's job: logging, cost accounting, default settings, and shims for
// models that fall short of the spec.
//
//	model = middleware.Wrap(model,
//	    middleware.Logging(slog.Default()),
//	    middleware.Observe(tracker.Record),
//	)
//
// A wrapped model is a provider.LanguageModel, so it goes anywhere a model
// goes and nothing downstream needs to know it was wrapped.
package middleware

import (
	"context"
	"regexp"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// Info identifies the model a middleware is running against.
type Info struct {
	Provider string
	ModelID  string
}

// GenerateFunc is a non-streaming model call.
type GenerateFunc func(ctx context.Context, opts provider.CallOptions) (*provider.GenerateResult, error)

// StreamFunc is a streaming model call.
type StreamFunc func(ctx context.Context, opts provider.CallOptions) (*provider.StreamResult, error)

// Next is the rest of the chain, below the middleware being run.
//
// Both directions are available whichever one was called, so a middleware can
// answer a stream request by generating — which is what SimulateStreaming
// does — or serve either from a cache.
type Next struct {
	Generate GenerateFunc
	Stream   StreamFunc
}

// Middleware is one layer of behaviour around a model. Every field is
// optional; a middleware that sets none is a no-op.
//
// TransformOptions runs before the call and applies to both generate and
// stream, which is where a middleware that only edits the request belongs.
// WrapGenerate and WrapStream see the call itself and must invoke next to let
// it proceed — or not, to serve a cached result.
type Middleware struct {
	// Name labels the layer in logs and errors.
	Name string

	// TransformOptions edits the call options on the way in.
	TransformOptions func(ctx context.Context, info Info, opts provider.CallOptions) (provider.CallOptions, error)

	// WrapGenerate wraps a non-streaming call.
	WrapGenerate func(ctx context.Context, info Info, opts provider.CallOptions, next Next) (*provider.GenerateResult, error)

	// WrapStream wraps a streaming call. A middleware that transforms the
	// stream must forward every part it does not consume, and must close its
	// output channel when the source closes.
	WrapStream func(ctx context.Context, info Info, opts provider.CallOptions, next Next) (*provider.StreamResult, error)

	// OverrideProvider and OverrideModelID change what the wrapped model
	// reports, for a gateway that stands in for another provider.
	OverrideProvider string
	OverrideModelID  string
}

// Wrap returns a model with the middleware applied.
//
// The first middleware listed is the outermost: it sees the caller's options
// before any other, and the finished result after every other. That matches
// the reading order of the call itself.
func Wrap(model provider.LanguageModel, middlewares ...Middleware) provider.LanguageModel {
	if len(middlewares) == 0 {
		return model
	}
	return &wrapped{inner: model, middlewares: middlewares}
}

// wrapped is a model with a middleware chain around it.
type wrapped struct {
	inner       provider.LanguageModel
	middlewares []Middleware
}

// Unwrap returns the model underneath, so a caller can reach a provider's own
// methods through the wrapper.
func (w *wrapped) Unwrap() provider.LanguageModel { return w.inner }

func (w *wrapped) SpecificationVersion() string { return w.inner.SpecificationVersion() }

func (w *wrapped) Provider() string {
	// Later layers win, so the outermost override is the one that shows.
	name := w.inner.Provider()
	for _, m := range w.middlewares {
		if m.OverrideProvider != "" {
			name = m.OverrideProvider
		}
	}
	return name
}

func (w *wrapped) ModelID() string {
	id := w.inner.ModelID()
	for _, m := range w.middlewares {
		if m.OverrideModelID != "" {
			id = m.OverrideModelID
		}
	}
	return id
}

func (w *wrapped) SupportedURLs(ctx context.Context) (map[string][]*regexp.Regexp, error) {
	return w.inner.SupportedURLs(ctx)
}

// info describes the wrapped model to the middleware, using the overridden
// identity so a layer sees the model as the rest of the program does.
func (w *wrapped) info() Info {
	return Info{Provider: w.Provider(), ModelID: w.ModelID()}
}

func (w *wrapped) DoGenerate(ctx context.Context, opts provider.CallOptions) (*provider.GenerateResult, error) {
	return w.chain().Generate(ctx, opts)
}

func (w *wrapped) DoStream(ctx context.Context, opts provider.CallOptions) (*provider.StreamResult, error) {
	return w.chain().Stream(ctx, opts)
}

// chain builds the call chain, innermost first, so that the first middleware
// in the list ends up outermost.
//
// Both directions are built together because a middleware may answer one by
// calling the other, and the layers below it must be in place for both.
func (w *wrapped) chain() Next {
	info := w.info()
	next := Next{Generate: w.inner.DoGenerate, Stream: w.inner.DoStream}

	for i := len(w.middlewares) - 1; i >= 0; i-- {
		next = w.middlewares[i].link(info, next)
	}
	return next
}

// link wraps one middleware around the rest of the chain.
func (m Middleware) link(info Info, next Next) Next {
	return Next{
		Generate: func(ctx context.Context, opts provider.CallOptions) (*provider.GenerateResult, error) {
			opts, err := m.transform(ctx, info, opts)
			if err != nil {
				return nil, err
			}
			if m.WrapGenerate == nil {
				return next.Generate(ctx, opts)
			}
			return m.WrapGenerate(ctx, info, opts, next)
		},

		Stream: func(ctx context.Context, opts provider.CallOptions) (*provider.StreamResult, error) {
			opts, err := m.transform(ctx, info, opts)
			if err != nil {
				return nil, err
			}
			if m.WrapStream == nil {
				return next.Stream(ctx, opts)
			}
			return m.WrapStream(ctx, info, opts, next)
		},
	}
}

// transform applies TransformOptions when the middleware defines one.
func (m Middleware) transform(ctx context.Context, info Info, opts provider.CallOptions) (provider.CallOptions, error) {
	if m.TransformOptions == nil {
		return opts, nil
	}
	return m.TransformOptions(ctx, info, opts)
}
