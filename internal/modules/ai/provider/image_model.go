package provider

import "context"

// ImageSpecificationVersion is the image model spec revision this package
// implements.
const ImageSpecificationVersion = "v3"

// GeneratedImage is one image a model produced. Exactly one of Base64 or URL
// is set: providers return either inline bytes or a link to fetch, and
// converting between them would mean an HTTP call the caller did not ask for.
type GeneratedImage struct {
	Base64 string
	URL    string

	// MediaType is the image's type, e.g. "image/png". It may be empty when
	// the provider does not say.
	MediaType string
}

// ImageCallOptions is the input to an image generation call.
type ImageCallOptions struct {
	// Prompt describes the image. Required.
	Prompt string

	// N is how many images to produce. Zero means one.
	N int

	// Size is "{width}x{height}", e.g. "1024x1024". Providers accept different
	// sets, and one that cannot honour a size warns rather than substituting.
	//
	// Size and AspectRatio are alternatives; setting both is an error.
	Size string

	// AspectRatio is "{w}:{h}", e.g. "16:9", for providers that take a ratio
	// instead of pixel dimensions.
	AspectRatio string

	// Seed requests a reproducible image where the provider supports it.
	Seed *int64

	// Headers are extra HTTP headers for this call only.
	Headers Headers

	// ProviderOptions carries provider-specific settings, keyed by provider id.
	ProviderOptions ProviderOptions
}

// ImageResult is the outcome of an image generation call.
type ImageResult struct {
	Images []GeneratedImage

	ProviderMetadata ProviderMetadata
	Response         *ResponseInfo
	Warnings         []Warning
}

// ImageModel turns a prompt into images.
//
// Application code should call ai.GenerateImage, which splits a large request
// into the batches a model accepts and runs them in parallel.
type ImageModel interface {
	// SpecificationVersion reports the spec revision, always "v3".
	SpecificationVersion() string

	// Provider is the provider id, e.g. "google".
	Provider() string

	// ModelID is the provider-specific model id.
	ModelID() string

	// MaxImagesPerCall is how many images fit in one request. Zero means no
	// limit is known, in which case a caller should assume one.
	MaxImagesPerCall() int

	// DoGenerate produces images.
	DoGenerate(ctx context.Context, opts ImageCallOptions) (*ImageResult, error)
}
