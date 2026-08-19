// Package kimi implements the pi language model spec against Moonshot AI
// (Kimi). The chat API is OpenAI-compatible and emits DeepSeek-shaped
// reasoning_content, so this package is a thin configuration over
// providers/openaicompat.
//
// Two key kinds, two endpoints, two catalogs — mixing them 401s:
//
//   - Platform keys (sk-…) hit api.moonshot.ai with pay-per-token ids
//     (kimi-k3, kimi-k2.7-code, kimi-k2.6).
//   - Kimi Code subscription keys (sk-kimi-…) hit api.kimi.com/coding
//     with the plan's ids (k3, kimi-for-coding, kimi-for-coding-highspeed).
//
// KIMI_BASE_URL or LOOP_KIMI_BASE_URL overrides both (e.g. api.moonshot.cn
// for the China endpoint).
//
// Moonshot does not document reasoning_effort. Thinking is a boolean
// toggle, {thinking: {type: enabled|disabled}}, which this package maps
// from the portable Reasoning setting so callers do not have to.
package kimi

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providers/openaicompat"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// Platform (pay-per-token) model ids.
const (
	K3      = "kimi-k3"
	K27Code = "kimi-k2.7-code"
	K26     = "kimi-k2.6"
)

// Kimi Code subscription model ids. Only valid on api.kimi.com/coding.
const (
	CodeK3             = "k3"
	ForCoding          = "kimi-for-coding"
	ForCodingHighspeed = "kimi-for-coding-highspeed"
)

const (
	providerID      = "kimi"
	platformBaseURL = "https://api.moonshot.ai/v1"
	codingBaseURL   = "https://api.kimi.com/coding/v1"
	apiKeyEnv       = "KIMI_API_KEY"
)

// Options configures a Provider. The zero value works when KIMI_API_KEY
// is set in the environment.
type Options struct {
	// APIKey overrides the KIMI_API_KEY environment variable. The key's
	// prefix also picks the default BaseURL when BaseURL is empty.
	APIKey string

	// BaseURL overrides key-kind routing and the KIMI_BASE_URL env vars.
	BaseURL string

	// Headers are sent with every request.
	Headers provider.Headers

	// HTTPClient overrides the default client. Do not set a Timeout on a
	// client used for streaming; bound calls with a context instead.
	HTTPClient *http.Client

	// Retry overrides the backoff policy.
	Retry providerutil.RetryConfig
}

// Provider creates Kimi language models.
type Provider struct {
	inner *openaicompat.Provider
}

// IsSubscriptionKey reports a Kimi Code plan key. Those only work on
// the coding endpoint; a platform key on that URL (and vice versa) 401s.
func IsSubscriptionKey(key string) bool {
	return strings.HasPrefix(key, "sk-kimi-")
}

// BaseURL returns the endpoint for a key. An explicit env override wins
// so a China or local gateway does not need a code change.
func BaseURL(key string) string {
	if v := os.Getenv("KIMI_BASE_URL"); v != "" {
		return v
	}
	if v := os.Getenv("LOOP_KIMI_BASE_URL"); v != "" {
		return v
	}
	if IsSubscriptionKey(key) {
		return codingBaseURL
	}
	return platformBaseURL
}

// DefaultModel is K3 on whichever catalog the key unlocks.
func DefaultModel(key string) string {
	if IsSubscriptionKey(key) {
		return CodeK3
	}
	return K3
}

// New returns a Provider.
func New(opts Options) *Provider {
	key := opts.APIKey
	if key == "" {
		key = os.Getenv(apiKeyEnv)
	}
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = BaseURL(key)
	}

	return &Provider{inner: openaicompat.New(openaicompat.Options{
		Name:       providerID,
		APIKey:     opts.APIKey,
		APIKeyEnv:  apiKeyEnv,
		BaseURL:    baseURL,
		Headers:    opts.Headers,
		HTTPClient: opts.HTTPClient,
		Retry:      opts.Retry,
		// Moonshot has no json_schema response format in the same way
		// DeepSeek does not; fall back to describing the schema.
		DisableJSONSchema: true,
	})}
}

// LanguageModel returns a model handle. Thinking is mapped from the
// portable Reasoning setting onto Moonshot's toggle.
func (p *Provider) LanguageModel(modelID string) provider.LanguageModel {
	return &thinkingModel{inner: p.inner.LanguageModel(modelID)}
}

// Name returns the provider id.
func (p *Provider) Name() string { return providerID }

// thinkingModel wraps the chat-completions model so a Reasoning setting
// becomes Moonshot's thinking toggle instead of reasoning_effort, which
// K2.x does not document and may 400 on.
type thinkingModel struct {
	inner provider.LanguageModel
}

func (m *thinkingModel) SpecificationVersion() string { return m.inner.SpecificationVersion() }
func (m *thinkingModel) Provider() string             { return m.inner.Provider() }
func (m *thinkingModel) ModelID() string              { return m.inner.ModelID() }

func (m *thinkingModel) SupportedURLs(ctx context.Context) (map[string][]*regexp.Regexp, error) {
	return m.inner.SupportedURLs(ctx)
}

func (m *thinkingModel) DoGenerate(ctx context.Context, opts provider.CallOptions) (*provider.GenerateResult, error) {
	return m.inner.DoGenerate(ctx, applyThinking(opts))
}

func (m *thinkingModel) DoStream(ctx context.Context, opts provider.CallOptions) (*provider.StreamResult, error) {
	return m.inner.DoStream(ctx, applyThinking(opts))
}

func applyThinking(opts provider.CallOptions) provider.CallOptions {
	effort := opts.Reasoning
	if effort == "" || effort == provider.ReasoningDefault {
		return opts
	}

	// Drop the portable effort so openaicompat does not emit reasoning_effort.
	opts.Reasoning = ""

	kind := "enabled"
	if effort == provider.ReasoningNone {
		kind = "disabled"
	}

	copied := make(provider.ProviderOptions, len(opts.ProviderOptions)+1)
	for k, v := range opts.ProviderOptions {
		copied[k] = v
	}
	extra := copied.Get(providerID)
	if extra == nil {
		extra = provider.JSONObject{}
	} else {
		dup := make(provider.JSONObject, len(extra)+1)
		for k, v := range extra {
			dup[k] = v
		}
		extra = dup
	}
	if _, exists := extra["thinking"]; !exists {
		extra["thinking"] = map[string]any{"type": kind}
	}
	copied[providerID] = extra
	opts.ProviderOptions = copied
	return opts
}
