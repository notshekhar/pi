// Package anthropic implements the pi language model spec against
// Anthropic's Messages API.
//
// Supported: streaming and non-streaming text, extended thinking (both the
// budget and adaptive shapes), client-executed tools, prompt caching,
// images and documents.
//
// Not yet supported: provider-hosted tools (web search, code execution, MCP),
// structured output, and the batch API. Requesting any of them produces a
// warning rather than an error.
package anthropic

import (
	"context"
	"net/http"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// providerID is the key under which this provider reads and writes
// provider-specific options and metadata.
const providerID = "anthropic"

const (
	defaultBaseURL = "https://api.anthropic.com"
	// apiVersion is the dated Messages API contract this code targets.
	apiVersion = "2023-06-01"
)

// Options configures a Provider. The zero value works when ANTHROPIC_API_KEY
// is set in the environment.
type Options struct {
	// APIKey overrides the ANTHROPIC_API_KEY environment variable.
	APIKey string

	// BaseURL overrides the API root, for proxies and compatible gateways.
	BaseURL string

	// Headers are sent with every request, and are how beta features get
	// enabled: {"anthropic-beta": "..."}.
	Headers provider.Headers

	// HTTPClient overrides the default client.
	//
	// Do not set a Timeout on a client used for streaming: it bounds the whole
	// exchange including the body read, so it truncates long generations.
	// Bound the call with a context instead.
	HTTPClient *http.Client

	// Retry overrides the backoff policy. The zero value uses
	// providerutil.DefaultRetryConfig.
	Retry providerutil.RetryConfig

	// Name overrides the provider id reported by models, for gateways that
	// speak the Anthropic protocol under a different name.
	Name string
}

// Provider creates Anthropic language models.
type Provider struct {
	name   string
	client *providerutil.Client
	retry  providerutil.RetryConfig
}

// New returns a Provider.
//
// The credential is resolved lazily, on the first request, so constructing a
// provider never fails and a missing key surfaces as a call error naming the
// environment variable.
func New(opts Options) *Provider {
	baseURL := providerutil.TrimTrailingSlash(
		providerutil.LoadSetting(opts.BaseURL, "ANTHROPIC_BASE_URL", defaultBaseURL),
	)

	name := opts.Name
	if name == "" {
		name = providerID
	}

	retry := opts.Retry
	if retry == (providerutil.RetryConfig{}) {
		retry = providerutil.DefaultRetryConfig
	}

	p := &Provider{name: name, retry: retry}

	p.client = &providerutil.Client{
		BaseURL:    baseURL,
		HTTPClient: opts.HTTPClient,
		Headers: func(context.Context) (provider.Headers, error) {
			key, err := providerutil.LoadAPIKey(opts.APIKey, "ANTHROPIC_API_KEY", "Anthropic")
			if err != nil {
				return nil, err
			}
			headers := provider.Headers{
				"anthropic-version": apiVersion,
				"x-api-key":         key,
			}
			for k, v := range opts.Headers {
				headers[k] = v
			}
			return headers, nil
		},
	}

	return p
}

// LanguageModel returns a model handle for the given Anthropic model id.
//
// Unknown ids are accepted: capabilities fall back to conservative defaults so
// that a model released after this build still works.
func (p *Provider) LanguageModel(modelID string) provider.LanguageModel {
	return &languageModel{
		modelID:  modelID,
		client:   p.client,
		caps:     modelCapabilities(modelID),
		provider: p,
	}
}

// Name returns the provider id.
func (p *Provider) Name() string { return p.name }
