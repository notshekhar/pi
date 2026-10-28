// Package config resolves the provider, model, and working directory.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/ai/providers/anthropic"
	"github.com/notshekhar/pi/internal/ai/providers/bedrock"
	"github.com/notshekhar/pi/internal/ai/providers/deepseek"
	"github.com/notshekhar/pi/internal/ai/providers/google"
	"github.com/notshekhar/pi/internal/ai/providers/kimi"
	"github.com/notshekhar/pi/internal/ai/providers/ollama"
	"github.com/notshekhar/pi/internal/ai/providers/openaicompat"
	"github.com/notshekhar/pi/internal/ai/providers/openairesponses"
	"github.com/notshekhar/pi/internal/core/catalog"
)

// Config is the resolved run configuration.
type Config struct {
	Provider  string
	ModelID   string
	CWD       string
	Reasoning provider.ReasoningEffort
	MaxSteps  int
	// UnknownProvider is the stored provider that no longer resolves, when
	// Resolve had to fall back to a detected one. Set so the app can SAY so —
	// silently using a different provider than the one configured is worse
	// than the error this replaced.
	UnknownProvider string
}

// Defaults used when nothing is specified.
const (
	DefaultProvider = "google"
	DefaultMaxSteps = 24
)

// FullID is the loop-style provider/model id.
func (c Config) FullID() string {
	return catalog.FullID(c.Provider, c.ModelID)
}

// Resolve fills empty fields from the catalog, the environment, and
// ~/.loop/auth.json. -model accepts a short id or provider/model.
func Resolve(c Config) (Config, error) {
	if c.CWD == "" {
		wd, err := os.Getwd()
		if err != nil {
			return c, err
		}
		c.CWD = wd
	}
	c.CWD = filepath.Clean(c.CWD)

	if c.MaxSteps <= 0 {
		c.MaxSteps = DefaultMaxSteps
	}

	if c.ModelID != "" {
		// A custom provider's name is a valid prefix too, and it has to be
		// tried FIRST: catalog.Parse only knows the built-in list, so it
		// hands back `mygw/gw-model-2` whole and the provider is then
		// auto-detected into whatever else has a key.
		if prefix, model, ok := strings.Cut(c.ModelID, "/"); ok {
			if p, custom := LookupCustom(prefix); custom {
				c.Provider, c.ModelID = p.ID(), model
			}
		}
		if _, custom := LookupCustom(c.Provider); !custom {
			prov, model := catalog.Parse(c.ModelID, c.Provider)
			if catalog.IsProvider(prov) {
				c.Provider = prov
			}
			c.ModelID = model
		}
	}

	if c.Provider == "" {
		c.Provider = detectProvider()
	}
	if c.Provider == "gemini" {
		c.Provider = "google"
	}
	c.Provider = strings.ToLower(c.Provider)
	custom, isCustom := LookupCustom(c.Provider)
	if isCustom {
		c.Provider = custom.ID()
	}
	if !catalog.IsProvider(c.Provider) && !isCustom {
		// A provider that no longer exists must not be able to stop the agent
		// from starting. The stored one is a PREFERENCE, and the way to lose
		// it is ordinary — remove a custom endpoint while it is selected, or
		// open a config written by a newer build. Refusing to launch leaves
		// the user with an app they cannot reach the settings of; falling
		// back to detection leaves them with a working one.
		unknown := c.Provider
		c.Provider = detectProvider()
		if c.Provider == "" {
			return c, fmt.Errorf("config: no provider %q, and no credential for any other — try /login", unknown)
		}
		c.UnknownProvider = unknown
		// The model went with it: an id from a provider that is gone cannot
		// mean anything on the one detected in its place.
		c.ModelID = ""
		custom, isCustom = LookupCustom(c.Provider)
	}

	if c.ModelID == "" {
		// A custom endpoint is not in the catalog, so its default is the
		// first model the user listed for it — which is why the wizard asks
		// for them in preference order.
		if isCustom && len(custom.Models) > 0 {
			c.ModelID = custom.Models[0].ID
		} else if def, ok := catalog.Default(c.Provider, APIKey(c.Provider)); ok {
			c.ModelID = def.ShortID
		}
	}
	if c.ModelID == "" {
		return c, fmt.Errorf("config: no default model for %q; pass -model", c.Provider)
	}
	return c, nil
}

// LanguageModel builds a pigo model for the resolved config.
func LanguageModel(c Config) (provider.LanguageModel, error) {
	key := APIKey(c.Provider)
	switch c.Provider {
	case "google":
		return google.New(google.Options{APIKey: key}).LanguageModel(c.ModelID), nil
	case "anthropic":
		return anthropic.New(anthropic.Options{APIKey: key}).LanguageModel(c.ModelID), nil
	case "deepseek":
		return deepseek.New(deepseek.Options{APIKey: key}).LanguageModel(c.ModelID), nil
	case "kimi":
		return kimi.New(kimi.Options{APIKey: key}).LanguageModel(c.ModelID), nil
	case "bedrock":
		return bedrock.New(bedrock.Options{}).LanguageModel(c.ModelID), nil
	case "openai":
		// loop's createOpenAI(...)(model): the Responses API, which OpenAI's
		// newer models require for tools with a reasoning effort.
		return responses("openai", "", key, "OPENAI_API_KEY", false).LanguageModel(c.ModelID), nil
	case "openrouter":
		// loop's @openrouter/ai-sdk-provider: chat completions, with the
		// signed reasoning_details round-tripped.
		return openaicompat.New(openaicompat.Options{
			Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", APIKey: key, APIKeyEnv: "OPENROUTER_API_KEY",
			ReasoningDetails: true,
		}).LanguageModel(c.ModelID), nil
	case "xai":
		// A SuperGrok sign-in beats an API key: the user chose to bill their
		// subscription, and silently charging their pay-as-you-go account
		// instead because a key happened to be in the environment is the
		// wrong way round.
		//
		// loop's xai(model) is @ai-sdk/xai's Responses model, so this is too.
		x := openairesponses.Options{Name: "xai", XAI: true, BaseURL: "https://api.x.ai/v1", APIKey: key, APIKeyEnv: "XAI_API_KEY"}
		if XaiSignedIn() {
			x.AuthToken = XaiAccessToken
		}
		return openairesponses.New(x).LanguageModel(c.ModelID), nil
	case "mistral":
		// loop's @ai-sdk/mistral.
		return openaicompat.New(openaicompat.Options{
			Name: "mistral", BaseURL: "https://api.mistral.ai/v1", APIKey: key, APIKeyEnv: "MISTRAL_API_KEY", Mistral: true,
		}).LanguageModel(c.ModelID), nil
	case "glm":
		return compat("glm", "https://open.bigmodel.cn/api/paas/v4", key, "GLM_API_KEY", false, false).LanguageModel(c.ModelID), nil
	case "zai":
		return compat("zai", "https://api.z.ai/api/paas/v4", key, "ZAI_API_KEY", false, false).LanguageModel(c.ModelID), nil
	case "groq":
		// loop's @ai-sdk/groq: its effort map, reasoning replayed as "reasoning".
		return openaicompat.New(openaicompat.Options{
			Name: "groq", BaseURL: "https://api.groq.com/openai/v1", APIKey: key, APIKeyEnv: "GROQ_API_KEY",
			Effort: openaicompat.GroqEffort, ReplayReasoning: true, ReasoningField: "reasoning",
		}).LanguageModel(c.ModelID), nil
	case "cerebras":
		// loop's @ai-sdk/cerebras: max_completion_tokens, reasoning replayed
		// as "reasoning".
		return openaicompat.New(openaicompat.Options{
			Name: "cerebras", BaseURL: "https://api.cerebras.ai/v1", APIKey: key, APIKeyEnv: "CEREBRAS_API_KEY",
			UseMaxCompletionTokens: true, ReplayReasoning: true, ReasoningField: "reasoning",
		}).LanguageModel(c.ModelID), nil
	case "zenmux":
		// loop: createOpenAI with ZenMux's base URL — the Responses API.
		return responses("zenmux", "https://zenmux.ai/api/v1", key, "ZENMUX_API_KEY", false).LanguageModel(c.ModelID), nil
	case "vercel":
		return compat("vercel", "https://ai-gateway.vercel.sh/v1", key, "AI_GATEWAY_API_KEY", false, false).LanguageModel(c.ModelID), nil
	case "ollama":
		// loop's ollama-ai-provider-v2: the native /api, which alone takes
		// num_ctx (the agent sets it to the model's window).
		base := strings.TrimRight(os.Getenv("OLLAMA_HOST"), "/")
		if base == "" {
			base = "http://127.0.0.1:11434"
		}
		if !strings.Contains(base, "://") {
			base = "http://" + base
		}
		base = strings.TrimSuffix(strings.TrimSuffix(base, "/v1"), "/api") + "/api"
		var headers provider.Headers
		if key != "" {
			headers = provider.Headers{"Authorization": "Bearer " + key}
		}
		return ollama.New(ollama.Options{BaseURL: base, Headers: headers}).LanguageModel(c.ModelID), nil
	default:
		// A user-defined endpoint. This branch used to return "unknown
		// provider", which made every custom provider configurable and
		// unusable — the setting existed, the picker listed it, and choosing
		// it failed at the first turn.
		if p, ok := LookupCustom(c.Provider); ok {
			return customModel(c.Provider, c.ModelID, p)
		}
		return nil, fmt.Errorf("config: unknown provider %q", c.Provider)
	}
}

// customModel builds a model for a user-defined endpoint, in the API shape the
// endpoint actually speaks.
//
// The shape is stored, not sniffed. A gateway commonly serves several from one
// host — bifrost exposes /anthropic and /openai — and the URL says nothing
// about which is which; sending the wrong body shape fails in ways that read
// like a broken model rather than a misrouted request.
func customModel(name, modelID string, p CustomProvider) (provider.LanguageModel, error) {
	key := p.CustomKey()
	switch strings.ToLower(p.SDK) {
	case "anthropic":
		return anthropic.New(anthropic.Options{
			APIKey:  key,
			BaseURL: p.BaseURL,
			Headers: provider.Headers(p.Headers),
		}).LanguageModel(modelID), nil
	case "google", "gemini":
		return google.New(google.Options{
			APIKey:  key,
			BaseURL: p.BaseURL,
			Headers: provider.Headers(p.Headers),
		}).LanguageModel(modelID), nil
	case "openai", "openai-compatible", "":
		// loop's customModel: createOpenAI for both shapes, so the Responses
		// API at the base URL with /v1 appended when it names no version.
		return openairesponses.New(openairesponses.Options{
			Name:           name,
			BaseURL:        versionedBaseURL(p.BaseURL),
			APIKey:         key,
			Headers:        provider.Headers(p.Headers),
			ForceReasoning: knownReasoning(name),
			// An endpoint reached through headers or mTLS has no bearer token
			// at all, and demanding one would lock it out.
			AllowMissingAPIKey: key == "",
		}).LanguageModel(modelID), nil
	default:
		return openaicompat.New(openaicompat.Options{
			Name:               name,
			BaseURL:            p.BaseURL,
			APIKey:             key,
			Headers:            provider.Headers(p.Headers),
			AllowMissingAPIKey: key == "",
		}).LanguageModel(modelID), nil
	}
}

var versionSegment = regexp.MustCompile(`/v\d+(\b|/)`)

// versionedBaseURL is loop's normalizeBaseURL for the OpenAI shapes: the
// SDK expects the version segment in the base URL, so one that names none
// gets /v1.
func versionedBaseURL(base string) string {
	trimmed := strings.TrimRight(base, "/")
	if trimmed == "" || versionSegment.MatchString(trimmed) {
		return trimmed
	}
	return trimmed + "/v1"
}

// knownReasoning is loop's forceReasoning: the catalog positively knows the
// model reasons though its id does not say so.
func knownReasoning(prov string) func(string) bool {
	return func(modelID string) bool {
		m, ok := ModelInfo(prov, modelID)
		return ok && m.Reasoning
	}
}

func responses(name, baseURL, key, env string, allowMissing bool) *openairesponses.Provider {
	return openairesponses.New(openairesponses.Options{
		Name: name, BaseURL: baseURL, APIKey: key, APIKeyEnv: env,
		ForceReasoning: knownReasoning(name), AllowMissingAPIKey: allowMissing,
	})
}

func compat(name, baseURL, key, env string, maxCompletion, allowMissing bool) *openaicompat.Provider {
	return openaicompat.New(openaicompat.Options{
		Name:                   name,
		BaseURL:                baseURL,
		APIKey:                 key,
		APIKeyEnv:              env,
		UseMaxCompletionTokens: maxCompletion,
		AllowMissingAPIKey:     allowMissing,
	})
}

// detectProvider picks the first catalog provider that has a credential.
func detectProvider() string {
	for _, p := range catalog.Providers {
		if p.Keyless {
			continue
		}
		if Authorized(p.ID) {
			return p.ID
		}
	}
	if active := ActiveProvider(); catalog.IsProvider(active) {
		return active
	}
	return DefaultProvider
}

// Authorized reports that a provider can be used this session.
func Authorized(id string) bool {
	p, ok := catalog.LookupProvider(id)
	if !ok {
		return false
	}
	if p.Keyless {
		return true
	}
	if id == "bedrock" {
		return APIKey("bedrock") != "" ||
			os.Getenv("AWS_ACCESS_KEY_ID") != "" ||
			fileExists(filepath.Join(userHome(), ".aws", "credentials"))
	}
	// A subscription sign-in is a credential even though there is no key.
	if id == "xai" && XaiSignedIn() {
		return true
	}
	return APIKey(id) != ""
}

// APIKey reads a credential from the environment, then ~/.loop/auth.json.
func APIKey(id string) string {
	p, ok := catalog.LookupProvider(id)
	if ok {
		for _, env := range p.Envs {
			if v := os.Getenv(env); v != "" {
				return v
			}
		}
	}
	return storedAPIKey(id)
}

func userHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
