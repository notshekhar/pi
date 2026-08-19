package middleware

import (
	"context"
	"time"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// ImageFunc is an image generation call.
type ImageFunc func(ctx context.Context, opts provider.ImageCallOptions) (*provider.ImageResult, error)

// ImageMiddleware is one layer of behaviour around an image model.
type ImageMiddleware struct {
	// Name labels the layer in logs and errors.
	Name string

	// TransformOptions edits the call options on the way in. This is where a
	// prompt prefix or a house style belongs.
	TransformOptions func(ctx context.Context, info Info, opts provider.ImageCallOptions) (provider.ImageCallOptions, error)

	// WrapGenerate wraps the call itself.
	WrapGenerate func(ctx context.Context, info Info, opts provider.ImageCallOptions, next ImageFunc) (*provider.ImageResult, error)

	// OverrideProvider and OverrideModelID change what the wrapped model
	// reports.
	OverrideProvider string
	OverrideModelID  string
}

// WrapImage returns an image model with the middleware applied. As with Wrap,
// the first listed is the outermost.
func WrapImage(model provider.ImageModel, middlewares ...ImageMiddleware) provider.ImageModel {
	if len(middlewares) == 0 {
		return model
	}
	return &wrappedImage{inner: model, middlewares: middlewares}
}

// wrappedImage is an image model with a middleware chain around it.
type wrappedImage struct {
	inner       provider.ImageModel
	middlewares []ImageMiddleware
}

// Unwrap returns the model underneath.
func (w *wrappedImage) Unwrap() provider.ImageModel { return w.inner }

func (w *wrappedImage) SpecificationVersion() string { return w.inner.SpecificationVersion() }
func (w *wrappedImage) MaxImagesPerCall() int        { return w.inner.MaxImagesPerCall() }

func (w *wrappedImage) Provider() string {
	name := w.inner.Provider()
	for _, m := range w.middlewares {
		if m.OverrideProvider != "" {
			name = m.OverrideProvider
		}
	}
	return name
}

func (w *wrappedImage) ModelID() string {
	id := w.inner.ModelID()
	for _, m := range w.middlewares {
		if m.OverrideModelID != "" {
			id = m.OverrideModelID
		}
	}
	return id
}

// DoGenerate implements provider.ImageModel.
func (w *wrappedImage) DoGenerate(ctx context.Context, opts provider.ImageCallOptions) (*provider.ImageResult, error) {
	info := Info{Provider: w.Provider(), ModelID: w.ModelID()}
	next := w.inner.DoGenerate

	for i := len(w.middlewares) - 1; i >= 0; i-- {
		next = w.middlewares[i].link(info, next)
	}
	return next(ctx, opts)
}

// link wraps one middleware around the rest of the chain.
func (m ImageMiddleware) link(info Info, next ImageFunc) ImageFunc {
	return func(ctx context.Context, opts provider.ImageCallOptions) (*provider.ImageResult, error) {
		if m.TransformOptions != nil {
			transformed, err := m.TransformOptions(ctx, info, opts)
			if err != nil {
				return nil, err
			}
			opts = transformed
		}
		if m.WrapGenerate == nil {
			return next(ctx, opts)
		}
		return m.WrapGenerate(ctx, info, opts, next)
	}
}

// ImageSettings are call options applied when the caller left them unset.
type ImageSettings struct {
	Size            string
	AspectRatio     string
	Headers         provider.Headers
	ProviderOptions provider.ProviderOptions
}

// DefaultImageSettings fills in options the caller did not set.
//
// Size and AspectRatio are alternatives, so a default is only applied when the
// caller set neither: filling in a default size next to an explicit ratio
// would build a request the provider rejects.
func DefaultImageSettings(s ImageSettings) ImageMiddleware {
	return ImageMiddleware{
		Name: "default-image-settings",
		TransformOptions: func(_ context.Context, _ Info, opts provider.ImageCallOptions) (provider.ImageCallOptions, error) {
			if opts.Size == "" && opts.AspectRatio == "" {
				opts.Size = s.Size
				opts.AspectRatio = s.AspectRatio
			}
			opts.Headers = mergeHeaders(s.Headers, opts.Headers)
			opts.ProviderOptions = mergeProviderOptions(s.ProviderOptions, opts.ProviderOptions)
			return opts, nil
		},
	}
}

// ImageRecord is one completed image call.
type ImageRecord struct {
	Provider string
	ModelID  string

	// Requested is how many images the call asked for and Produced how many
	// came back. They differ when a safety filter withheld some, which is the
	// thing worth alerting on.
	Requested int
	Produced  int

	Duration time.Duration
	Err      error
}

// ObserveImage reports every completed image call to fn.
func ObserveImage(fn func(ImageRecord)) ImageMiddleware {
	return ImageMiddleware{
		Name: "observe-image",
		WrapGenerate: func(ctx context.Context, info Info, opts provider.ImageCallOptions, next ImageFunc) (*provider.ImageResult, error) {
			start := time.Now()
			res, err := next(ctx, opts)

			requested := opts.N
			if requested <= 0 {
				requested = 1
			}

			record := ImageRecord{
				Provider:  info.Provider,
				ModelID:   info.ModelID,
				Requested: requested,
				Duration:  time.Since(start),
				Err:       err,
			}
			if res != nil {
				record.Produced = len(res.Images)
			}
			fn(record)

			return res, err
		},
	}
}
