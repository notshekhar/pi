package ai

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// ImageOptions configures an image generation call.
type ImageOptions struct {
	// Model is required.
	Model provider.ImageModel

	// N is how many images to produce. Zero means one. Requests larger than
	// the model's per-call limit are split into several calls.
	N int

	// Size is "{width}x{height}", e.g. "1024x1024".
	//
	// Size and AspectRatio are alternatives; setting both is an error rather
	// than a silent preference, since which one wins would differ per provider.
	Size string

	// AspectRatio is "{w}:{h}", e.g. "16:9".
	AspectRatio string

	// Seed makes the result reproducible where the provider supports it. When
	// several calls are needed, each gets a distinct derived seed: reusing one
	// would return the same image several times.
	Seed *int64

	// MaxParallelCalls bounds concurrent requests. Zero uses a small default.
	MaxParallelCalls int

	// Headers are extra HTTP headers for every call.
	Headers provider.Headers

	// ProviderOptions carries provider-specific settings, keyed by provider id.
	ProviderOptions provider.ProviderOptions
}

// ImageResult is the outcome of a GenerateImage call.
type ImageResult struct {
	// Images are in request order.
	Images []provider.GeneratedImage

	// Warnings is the union of the warnings from every call.
	Warnings []provider.Warning
}

// First returns the first image, which is what a caller asking for one wants.
func (r *ImageResult) First() (provider.GeneratedImage, error) {
	if len(r.Images) == 0 {
		return provider.GeneratedImage{}, errors.New("pi: the model returned no images")
	}
	return r.Images[0], nil
}

// Bytes decodes an image's inline data.
//
// It fails for an image the provider returned as a URL: fetching it is an HTTP
// call with its own timeout and failure modes, which belongs to the caller.
func Bytes(img provider.GeneratedImage) ([]byte, error) {
	if img.Base64 == "" {
		if img.URL != "" {
			return nil, fmt.Errorf("pi: the image is a URL, not inline data: %s", img.URL)
		}
		return nil, errors.New("pi: the image carries no data")
	}
	return base64.StdEncoding.DecodeString(img.Base64)
}

// GenerateImage produces images from a prompt.
//
// A request for more images than the model accepts per call is split across
// several calls, which run in parallel and are reassembled in order.
func GenerateImage(ctx context.Context, prompt string, opts ImageOptions) (*ImageResult, error) {
	if opts.Model == nil {
		return nil, errors.New("pi: ImageOptions.Model is required")
	}
	if prompt == "" {
		return nil, errors.New("pi: a prompt is required")
	}
	if opts.Size != "" && opts.AspectRatio != "" {
		return nil, errors.New("pi: set either ImageOptions.Size or AspectRatio, not both")
	}

	n := opts.N
	if n <= 0 {
		n = 1
	}

	counts := imageBatches(n, opts.Model.MaxImagesPerCall())
	results := make([]*provider.ImageResult, len(counts))

	parallel := opts.MaxParallelCalls
	if parallel <= 0 {
		parallel = defaultEmbedParallelism
	}

	if err := forEachParallel(ctx, len(counts), parallel, func(ctx context.Context, i int) error {
		res, err := opts.Model.DoGenerate(ctx, provider.ImageCallOptions{
			Prompt:          prompt,
			N:               counts[i],
			Size:            opts.Size,
			AspectRatio:     opts.AspectRatio,
			Seed:            seedForCall(opts.Seed, i),
			Headers:         opts.Headers,
			ProviderOptions: opts.ProviderOptions,
		})
		if err != nil {
			return fmt.Errorf("pi: image call %d of %d: %w", i+1, len(counts), err)
		}
		results[i] = res
		return nil
	}); err != nil {
		return nil, err
	}

	out := &ImageResult{Images: make([]provider.GeneratedImage, 0, n)}
	for _, res := range results {
		out.Images = append(out.Images, res.Images...)
		out.Warnings = append(out.Warnings, res.Warnings...)
	}
	return out, nil
}

// imageBatches splits a request for n images into per-call counts.
func imageBatches(n, perCall int) []int {
	if perCall <= 0 {
		// An unknown limit means one at a time: too many in one request fails,
		// while too few only costs an extra round trip.
		perCall = 1
	}

	counts := make([]int, 0, (n+perCall-1)/perCall)
	for remaining := n; remaining > 0; remaining -= perCall {
		counts = append(counts, min(remaining, perCall))
	}
	return counts
}

// seedForCall derives a distinct seed per call, so that splitting a request
// does not return the same image several times.
func seedForCall(seed *int64, call int) *int64 {
	if seed == nil {
		return nil
	}
	derived := *seed + int64(call)
	return &derived
}
