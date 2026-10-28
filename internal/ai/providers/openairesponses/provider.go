// Package openairesponses implements the pi language model spec against the
// OpenAI Responses API (POST /responses) — what the AI SDK's createOpenAI
// uses by default, and so what loop sends to OpenAI and to custom gateways
// configured with sdk "openai".
//
// The chat completions API is not a substitute: OpenAI's newer models (and
// gateways in front of them) refuse function tools together with a
// reasoning effort there ("To use function tools, use /v1/responses").
//
// Ported from @ai-sdk/openai's responses model: system prompts go in as
// developer messages for reasoning models, reasoning is requested with a
// summary and replayed by item reference (the API stores responses by
// default), tool calls replay as function_call items, and the stream's
// output items map onto text, reasoning and tool-call parts.
package openairesponses

import (
	"context"
	"net/http"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/ai/providerutil"
)

const (
	providerID     = "openai"
	defaultBaseURL = "https://api.openai.com/v1"
	responsesPath  = "/responses"
)

// Options configures a Provider.
type Options struct {
	// Name is the provider id models report. Defaults to "openai".
	Name string

	// APIKey overrides the environment variable named by APIKeyEnv.
	APIKey string
	// APIKeyEnv is the environment variable holding the credential.
	// Defaults to OPENAI_API_KEY.
	APIKeyEnv string
	// AuthToken supplies a bearer token per request, for an expiring
	// credential; it takes precedence over APIKey.
	AuthToken func(context.Context) (string, error)

	// BaseURL is the API root including its version segment. Defaults to
	// OpenAI's.
	BaseURL string

	// Headers are sent with every request.
	Headers provider.Headers

	// HTTPClient overrides the default client. Do not set a Timeout on a
	// client used for streaming; bound calls with a context instead.
	HTTPClient *http.Client

	// Retry overrides the backoff policy.
	Retry providerutil.RetryConfig

	// ForceReasoning reports models that reason though their id does not
	// say so — a gateway's vendor-prefixed or renamed ids (loop's
	// forceReasoning). Recognised OpenAI ids need no help.
	ForceReasoning func(modelID string) bool

	// AllowMissingAPIKey is for an endpoint reached through headers or mTLS,
	// with no bearer token at all.
	AllowMissingAPIKey bool

	// XAI speaks xAI's dialect of the Responses API (@ai-sdk/xai's
	// responses model): system prompts as system messages, earlier output
	// replayed in full with its ids rather than by item reference, efforts
	// mapped onto low/medium/high, and options and metadata under "xai".
	XAI bool
}

// Provider creates Responses API language models.
type Provider struct {
	name           string
	client         *providerutil.Client
	retry          providerutil.RetryConfig
	forceReasoning func(string) bool
	xai            bool
	// key names this dialect's provider options and metadata.
	key string
}

// New returns a Provider.
func New(opts Options) *Provider {
	name := opts.Name
	if name == "" {
		name = providerID
	}
	apiKeyEnv := opts.APIKeyEnv
	if apiKeyEnv == "" {
		apiKeyEnv = "OPENAI_API_KEY"
	}
	retry := opts.Retry
	if retry == (providerutil.RetryConfig{}) {
		retry = providerutil.DefaultRetryConfig
	}
	baseURL := providerutil.TrimTrailingSlash(providerutil.LoadSetting(opts.BaseURL, "", defaultBaseURL))

	p := &Provider{name: name, retry: retry, forceReasoning: opts.ForceReasoning, xai: opts.XAI, key: providerID}
	if opts.XAI {
		p.key = "xai"
	}
	p.client = &providerutil.Client{
		BaseURL:    baseURL,
		HTTPClient: opts.HTTPClient,
		Headers: func(ctx context.Context) (provider.Headers, error) {
			headers := provider.Headers{}
			if opts.AuthToken != nil {
				token, err := opts.AuthToken(ctx)
				if err != nil {
					return nil, err
				}
				headers["Authorization"] = "Bearer " + token
			} else {
				key, err := providerutil.LoadAPIKey(opts.APIKey, apiKeyEnv, name)
				switch {
				case err == nil:
					headers["Authorization"] = "Bearer " + key
				case !opts.AllowMissingAPIKey:
					return nil, err
				}
			}
			for k, v := range opts.Headers {
				headers[k] = v
			}
			return headers, nil
		},
	}
	return p
}

// LanguageModel returns a model handle for the given model id.
func (p *Provider) LanguageModel(modelID string) provider.LanguageModel {
	return &languageModel{modelID: modelID, provider: p}
}

// Name returns the provider id.
func (p *Provider) Name() string { return p.name }
