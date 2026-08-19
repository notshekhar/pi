// Package google implements the pi language model spec against the Google
// Generative Language API, the endpoint behind Google AI Studio keys.
//
// For Vertex AI, which speaks the same wire format under Google Cloud
// credentials, use providers/googlevertex.
//
//	model := google.New(google.Options{}).LanguageModel("gemini-3-pro")
//
// Supported: streaming and non-streaming text, thinking (both the Gemini 2.5
// budget and the Gemini 3 level shapes), tool calling, images and documents,
// JSON output, and grounding sources.
//
// Not yet supported: provider-hosted tools such as Google Search grounding and
// code execution, which produce a warning rather than an error.
//
// Two things differ from the other providers and are worth knowing:
//
//   - Tool input schemas are converted to OpenAPI 3.0, which is a smaller
//     language than JSON Schema. A recursive tool input cannot be expressed
//     and must be flattened.
//   - Replayed reasoning needs its thoughtSignature. Core carries it through
//     automatically; a hand-built history must preserve it.
package google

import (
	"context"
	"net/http"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// providerID is the key for provider-specific options and metadata.
const providerID = "google"

const defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// Options configures a Provider. The zero value works when GOOGLE_API_KEY or
// GEMINI_API_KEY is set in the environment.
type Options struct {
	// APIKey overrides the environment variable.
	APIKey string

	// BaseURL overrides the API root.
	BaseURL string

	// Headers are sent with every request.
	Headers provider.Headers

	// HTTPClient overrides the default client. Do not set a Timeout on a
	// client used for streaming; bound calls with a context instead.
	HTTPClient *http.Client

	// Retry overrides the backoff policy.
	Retry providerutil.RetryConfig
}

// Provider creates Google language models.
type Provider struct {
	name   string
	client *providerutil.Client
	retry  providerutil.RetryConfig

	// pathFor builds the request path for a model and method. It is a field so
	// that the Vertex provider, which uses a different resource layout, can
	// reuse this whole implementation.
	pathFor func(modelID, method string, stream bool) string
}

// New returns a Provider.
//
// The credential is resolved lazily on the first request, so a missing key
// surfaces as a call error naming the environment variables.
func New(opts Options) *Provider {
	baseURL := providerutil.TrimTrailingSlash(
		providerutil.LoadSetting(opts.BaseURL, "GOOGLE_BASE_URL", defaultBaseURL),
	)

	retry := opts.Retry
	if retry == (providerutil.RetryConfig{}) {
		retry = providerutil.DefaultRetryConfig
	}

	p := &Provider{name: providerID, retry: retry}

	p.client = &providerutil.Client{
		BaseURL:    baseURL,
		HTTPClient: opts.HTTPClient,
		Headers: func(context.Context) (provider.Headers, error) {
			key, err := loadAPIKey(opts.APIKey)
			if err != nil {
				return nil, err
			}
			// The key goes in a header rather than the query string so it does
			// not end up in logs or proxy access records.
			headers := provider.Headers{"x-goog-api-key": key}
			for k, v := range opts.Headers {
				headers[k] = v
			}
			return headers, nil
		},
	}

	return p
}

// loadAPIKey resolves the credential, accepting either of the two environment
// variables Google's own tooling uses.
func loadAPIKey(explicit string) (string, error) {
	key, err := providerutil.LoadAPIKey(explicit, "GOOGLE_API_KEY", "Google")
	if err == nil {
		return key, nil
	}
	key, fallbackErr := providerutil.LoadAPIKey("", "GEMINI_API_KEY", "Google")
	if fallbackErr == nil {
		return key, nil
	}
	return "", &provider.LoadAPIKeyError{
		Message: "Google API key is missing: pass it explicitly or set GOOGLE_API_KEY or GEMINI_API_KEY",
	}
}

// path builds the request path for a model and method.
func (p *Provider) path(modelID, method string, stream bool) string {
	if p.pathFor != nil {
		return p.pathFor(modelID, method, stream)
	}
	path := "/" + modelPath(modelID) + ":" + method
	if stream {
		// Without alt=sse the streaming endpoint returns a JSON array rather
		// than server-sent events.
		path += "?alt=sse"
	}
	return path
}

// LanguageModel returns a model handle for the given model id.
func (p *Provider) LanguageModel(modelID string) provider.LanguageModel {
	return &languageModel{
		modelID:  modelID,
		caps:     modelCapabilities(modelID),
		provider: p,
	}
}

// Name returns the provider id.
func (p *Provider) Name() string { return p.name }

// NewWithTransport builds a Provider around a caller-supplied client and path
// layout. It exists so providers/googlevertex can reuse this implementation
// with Google Cloud credentials and Vertex's resource paths.
func NewWithTransport(
	name string,
	client *providerutil.Client,
	retry providerutil.RetryConfig,
	pathFor func(modelID, method string, stream bool) string,
) *Provider {
	return &Provider{name: name, client: client, retry: retry, pathFor: pathFor}
}
