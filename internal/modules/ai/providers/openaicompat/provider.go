// Package openaicompat implements the pi language model spec against the
// OpenAI chat completions API.
//
// It targets the protocol rather than one vendor, so it also drives the many
// gateways that speak it: Groq, DeepSeek, Cerebras, Together, OpenRouter,
// Ollama, vLLM and others. Point BaseURL at the endpoint and set the
// environment variable name the credential lives in.
//
//	groq := openaicompat.New(openaicompat.Options{
//	    Name:      "groq",
//	    BaseURL:   "https://api.groq.com/openai/v1",
//	    APIKeyEnv: "GROQ_API_KEY",
//	})
//	model := groq.LanguageModel("llama-3.3-70b-versatile")
//
// Supported: streaming and non-streaming text, tool calling, reasoning where
// the gateway exposes it, images, and JSON output.
//
// Not supported: provider-hosted tools, and replaying reasoning as input,
// which the chat API has no field for.
package openaicompat

import (
	"context"
	"net/http"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// providerID is the default key for provider-specific options and metadata.
const providerID = "openai-compatible"

const (
	defaultBaseURL  = "https://api.openai.com/v1"
	defaultChatPath = "/chat/completions"
)

// Options configures a Provider.
type Options struct {
	// Name is the provider id reported by models and the default key for
	// provider-specific options. Defaults to "openai-compatible".
	Name string

	// APIKey overrides the environment variable named by APIKeyEnv.
	APIKey string

	// APIKeyEnv is the environment variable holding the credential.
	// Defaults to OPENAI_API_KEY.
	APIKeyEnv string

	// BaseURL is the API root including any version segment, e.g.
	// "https://api.groq.com/openai/v1". Defaults to OpenAI's.
	BaseURL string

	// ChatPath overrides the completions path. Defaults to
	// "/chat/completions".
	ChatPath string

	// Headers are sent with every request.
	Headers provider.Headers
	// AuthToken supplies a bearer token PER REQUEST, for a provider whose
	// credential expires — an OAuth access token rather than an API key.
	//
	// A function rather than a string because the token is refreshed while
	// the process runs: a value captured when the model was built goes stale
	// mid-session, and the failure is a 401 an hour into a conversation. It
	// takes precedence over APIKey, and a provider that has both is one where
	// the user signed in and the environment also happens to hold a key.
	AuthToken func(context.Context) (string, error)

	// HTTPClient overrides the default client. Do not set a Timeout on a
	// client used for streaming; bound calls with a context instead.
	HTTPClient *http.Client

	// Retry overrides the backoff policy.
	Retry providerutil.RetryConfig

	// UseMaxCompletionTokens sends max_completion_tokens instead of
	// max_tokens. OpenAI's reasoning models require it and reject max_tokens;
	// most gateways only understand max_tokens.
	UseMaxCompletionTokens bool

	// DisableUsageInStream omits stream_options.include_usage. Enable it for
	// gateways that reject the field, at the cost of usage reporting on
	// streaming calls.
	DisableUsageInStream bool

	// ImageResponseFormat sets response_format on image generation, for
	// gateways that need to be asked for inline bytes. Leave it empty for
	// OpenAI's own gpt-image-1, which always returns base64 and rejects the
	// field; set "b64_json" for DALL·E-era endpoints that default to a URL.
	ImageResponseFormat string

	// DisableJSONSchema is for gateways with only the schema-less
	// {"type":"json_object"} mode. DeepSeek is one: a json_schema request
	// returns "This response_format type is unavailable now".
	//
	// Structured output then falls back to describing the schema in the
	// prompt, which is unenforced, so a call that uses it carries a warning.
	DisableJSONSchema bool

	// RequireAPIKey is false for local endpoints such as Ollama or vLLM,
	// which accept requests with no credential.
	//
	// The zero value requires a key, since a silently unauthenticated request
	// to a hosted endpoint just fails later with a worse message.
	AllowMissingAPIKey bool
}

// Provider creates chat-completions language models.
type Provider struct {
	name         string
	optionsKey   string
	chatPath     string
	client       *providerutil.Client
	retry        providerutil.RetryConfig
	includeUsage bool
	noJSONSchema bool
	// imageResponseFormat is sent on image calls when non-empty.
	imageResponseFormat string

	useMaxCompletionTokens bool
}

// New returns a Provider.
//
// The credential is resolved lazily on the first request, so a missing key
// surfaces as a call error naming the environment variable.
func New(opts Options) *Provider {
	name := opts.Name
	if name == "" {
		name = providerID
	}

	apiKeyEnv := opts.APIKeyEnv
	if apiKeyEnv == "" {
		apiKeyEnv = "OPENAI_API_KEY"
	}

	baseURL := providerutil.TrimTrailingSlash(
		providerutil.LoadSetting(opts.BaseURL, "", defaultBaseURL),
	)

	chatPath := opts.ChatPath
	if chatPath == "" {
		chatPath = defaultChatPath
	}

	retry := opts.Retry
	if retry == (providerutil.RetryConfig{}) {
		retry = providerutil.DefaultRetryConfig
	}

	p := &Provider{
		name:                   name,
		optionsKey:             name,
		chatPath:               chatPath,
		retry:                  retry,
		includeUsage:           !opts.DisableUsageInStream,
		noJSONSchema:           opts.DisableJSONSchema,
		imageResponseFormat:    opts.ImageResponseFormat,
		useMaxCompletionTokens: opts.UseMaxCompletionTokens,
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
				for k, v := range opts.Headers {
					headers[k] = v
				}
				return headers, nil
			}

			key, err := providerutil.LoadAPIKey(opts.APIKey, apiKeyEnv, name)
			switch {
			case err == nil:
				headers["Authorization"] = "Bearer " + key
			case opts.AllowMissingAPIKey:
				// A local endpoint needs no credential.
			default:
				return nil, err
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
