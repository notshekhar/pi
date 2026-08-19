// Package deepseek implements the pi language model spec against the DeepSeek
// API.
//
// DeepSeek speaks the OpenAI chat completions protocol, so this package is a
// thin configuration over providers/openaicompat rather than a second
// implementation. The two places DeepSeek diverges — the
// prompt_cache_hit_tokens usage field and the insufficient_system_resource
// finish reason — are handled in that shared layer.
//
//	model := deepseek.New(deepseek.Options{}).LanguageModel(deepseek.Reasoner)
//
// deepseek-reasoner emits its thinking in reasoning_content, which surfaces as
// Reasoning parts. That reasoning cannot be replayed as input: the chat API
// has no field for it, so core drops it from the history on the next turn.
//
// DeepSeek has no json_schema response format, so structured output falls back
// to describing the schema in the prompt and every such call carries a warning.
package deepseek

import (
	"net/http"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providers/openaicompat"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// Model ids.
const (
	// Chat is the general-purpose model.
	Chat = "deepseek-chat"
	// Reasoner emits reasoning before its answer.
	Reasoner = "deepseek-reasoner"
)

const (
	providerID     = "deepseek"
	defaultBaseURL = "https://api.deepseek.com"
	apiKeyEnv      = "DEEPSEEK_API_KEY"
)

// Options configures a Provider. The zero value works when DEEPSEEK_API_KEY
// is set in the environment.
type Options struct {
	// APIKey overrides the DEEPSEEK_API_KEY environment variable.
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

// Provider creates DeepSeek language models.
type Provider struct {
	inner *openaicompat.Provider
}

// New returns a Provider.
func New(opts Options) *Provider {
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	return &Provider{inner: openaicompat.New(openaicompat.Options{
		Name:       providerID,
		APIKey:     opts.APIKey,
		APIKeyEnv:  apiKeyEnv,
		BaseURL:    baseURL,
		Headers:    opts.Headers,
		HTTPClient: opts.HTTPClient,
		Retry:      opts.Retry,
		// Measured: a json_schema response format returns "This
		// response_format type is unavailable now". Only json_object exists.
		DisableJSONSchema: true,
	})}
}

// LanguageModel returns a model handle. Use the Chat and Reasoner constants
// for the documented ids.
func (p *Provider) LanguageModel(modelID string) provider.LanguageModel {
	return p.inner.LanguageModel(modelID)
}

// Name returns the provider id.
func (p *Provider) Name() string { return providerID }
