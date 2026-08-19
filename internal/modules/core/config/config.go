// Package config resolves the provider, model, and working directory.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providers/anthropic"
	"github.com/notshekhar/pi/internal/modules/ai/providers/bedrock"
	"github.com/notshekhar/pi/internal/modules/ai/providers/deepseek"
	"github.com/notshekhar/pi/internal/modules/ai/providers/google"
	"github.com/notshekhar/pi/internal/modules/ai/providers/kimi"
	"github.com/notshekhar/pi/internal/modules/ai/providers/openaicompat"
	"github.com/notshekhar/pi/internal/modules/core/catalog"
)

// Config is the resolved run configuration.
type Config struct {
	Provider  string
	ModelID   string
	CWD       string
	Reasoning provider.ReasoningEffort
	MaxSteps  int
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
			if _, custom := LookupCustom(prefix); custom {
				c.Provider, c.ModelID = prefix, model
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
	if !catalog.IsProvider(c.Provider) && !isCustom {
		return c, fmt.Errorf("config: unknown provider %q — try /provider", c.Provider)
	}

	if c.ModelID == "" {
		// A custom endpoint is not in the catalog, so its default is the
		// first model the user listed for it — which is why the wizard asks
		// for them in preference order.
		if isCustom && len(custom.Models) > 0 {
			c.ModelID = custom.Models[0]
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
		return compat("openai", "", key, "OPENAI_API_KEY", true, false).LanguageModel(c.ModelID), nil
	case "openrouter":
		return compat("openrouter", "https://openrouter.ai/api/v1", key, "OPENROUTER_API_KEY", false, false).LanguageModel(c.ModelID), nil
	case "xai":
		// A SuperGrok sign-in beats an API key: the user chose to bill their
		// subscription, and silently charging their pay-as-you-go account
		// instead because a key happened to be in the environment is the
		// wrong way round.
		if XaiSignedIn() {
			return openaicompat.New(openaicompat.Options{
				Name:      "xai",
				BaseURL:   "https://api.x.ai/v1",
				AuthToken: XaiAccessToken,
			}).LanguageModel(c.ModelID), nil
		}
		return compat("xai", "https://api.x.ai/v1", key, "XAI_API_KEY", false, false).LanguageModel(c.ModelID), nil
	case "mistral":
		return compat("mistral", "https://api.mistral.ai/v1", key, "MISTRAL_API_KEY", false, false).LanguageModel(c.ModelID), nil
	case "glm":
		return compat("glm", "https://open.bigmodel.cn/api/paas/v4", key, "GLM_API_KEY", false, false).LanguageModel(c.ModelID), nil
	case "zai":
		return compat("zai", "https://api.z.ai/api/paas/v4", key, "ZAI_API_KEY", false, false).LanguageModel(c.ModelID), nil
	case "groq":
		return compat("groq", "https://api.groq.com/openai/v1", key, "GROQ_API_KEY", false, false).LanguageModel(c.ModelID), nil
	case "cerebras":
		return compat("cerebras", "https://api.cerebras.ai/v1", key, "CEREBRAS_API_KEY", false, false).LanguageModel(c.ModelID), nil
	case "zenmux":
		return compat("zenmux", "https://zenmux.ai/api/v1", key, "ZENMUX_API_KEY", false, false).LanguageModel(c.ModelID), nil
	case "vercel":
		return compat("vercel", "https://ai-gateway.vercel.sh/v1", key, "AI_GATEWAY_API_KEY", false, false).LanguageModel(c.ModelID), nil
	case "ollama":
		base := os.Getenv("OLLAMA_HOST")
		if base == "" {
			base = "http://127.0.0.1:11434/v1"
		} else if !strings.HasSuffix(base, "/v1") {
			base = strings.TrimRight(base, "/") + "/v1"
		}
		return compat("ollama", base, key, "", false, true).LanguageModel(c.ModelID), nil
	default:
		// A user-defined endpoint. This branch used to return "unknown
		// provider", which made every custom provider configurable and
		// unusable — the setting existed, the picker listed it, and choosing
		// it failed at the first turn.
		if p, ok := LookupCustom(c.Provider); ok {
			return openaicompat.New(openaicompat.Options{
				Name:    c.Provider,
				BaseURL: p.BaseURL,
				APIKey:  p.CustomKey(),
				Headers: p.Headers,
				// An endpoint reached through headers or mTLS has no bearer
				// token at all, and demanding one would lock it out.
				AllowMissingAPIKey: p.CustomKey() == "",
			}).LanguageModel(c.ModelID), nil
		}
		return nil, fmt.Errorf("config: unknown provider %q", c.Provider)
	}
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
	if active := loopActiveProvider(); catalog.IsProvider(active) {
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
			fileExists(filepath.Join(home(), ".aws", "credentials"))
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
	return loopAPIKey(id)
}

type loopAuth struct {
	Active    string `json:"active"`
	Providers map[string]struct {
		Mode   string `json:"mode"`
		APIKey string `json:"apiKey"`
	} `json:"providers"`
}

func loadLoopAuth() loopAuth {
	raw, err := os.ReadFile(filepath.Join(home(), ".loop", "auth.json"))
	if err != nil {
		return loopAuth{}
	}
	var auth loopAuth
	_ = json.Unmarshal(raw, &auth)
	return auth
}

func loopAPIKey(id string) string {
	auth := loadLoopAuth()
	entry, ok := auth.Providers[id]
	if !ok || entry.Mode != "apikey" {
		return ""
	}
	return entry.APIKey
}

func loopActiveProvider() string {
	return loadLoopAuth().Active
}

func home() string {
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

// Logout removes a provider's stored credential from ~/.loop/auth.json.
//
// Rewrites the file rather than editing it in place, and only touches the
// provider named: the file is shared with loop, and clobbering an unrelated
// entry would break a different program.
func Logout(provider string) error {
	path := filepath.Join(home(), ".loop", "auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("no stored credentials")
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("credentials file is not readable: %w", err)
	}
	providers, ok := doc["providers"].(map[string]any)
	if !ok {
		return fmt.Errorf("no stored credentials")
	}
	if _, found := providers[provider]; !found {
		return fmt.Errorf("no credential stored for %q", provider)
	}
	// Removes an OAuth entry as readily as an API key: whichever way the user
	// signed in, signing out has to actually sign them out, and an /logout
	// that leaves a live subscription token on disk is worse than none.
	delete(providers, provider)

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
