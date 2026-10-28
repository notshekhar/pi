// Package catalog is loop's model catalog (packages/core/src/catalog): the
// curated fallbacks, the models.dev snapshot and its runtime refresh, each
// provider's availability, custom gateways' models, and the user's
// ~/.loop/models.json patches — merged in loop's order, cached in loop's
// ~/.loop/catalog.json.
//
// Full ids are "provider/model". Gateways keep slashes in the model half
// (openrouter/anthropic/claude-sonnet-4-6); custom providers are
// "custom:<name>/<model>".
package catalog

import (
	"strings"
)

// Cost is USD per million tokens. Reasoning is a distinct reasoning-token
// rate; nil bills reasoning as plain output.
type Cost struct {
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  float64  `json:"cacheRead"`
	CacheWrite float64  `json:"cacheWrite"`
	Reasoning  *float64 `json:"reasoning,omitempty"`
}

// Model is loop's ModelInfo.
type Model struct {
	ID         string   `json:"id"`
	Provider   string   `json:"provider"`
	ShortID    string   `json:"-"`
	Name       string   `json:"name"`
	Context    int      `json:"contextWindow"`
	MaxOut     int      `json:"maxOutput"`
	Cost       Cost     `json:"cost"`
	Reasoning  bool     `json:"reasoning"`
	Modalities []string `json:"modalities"`
	Available  bool     `json:"available"`
}

// withShort fills ShortID from ID.
func (m Model) withShort() Model {
	if _, short, ok := strings.Cut(m.ID, "/"); ok {
		m.ShortID = short
	} else {
		m.ShortID = m.ID
	}
	return m
}

// Provider is a built-in vendor or gateway.
type Provider struct {
	ID      string
	Name    string
	Envs    []string
	Keyless bool
}

// Providers is loop's built-in list, minus OAuth-only entries
// (github-copilot, openai-chatgpt) that pi cannot sign in yet.
var Providers = []Provider{
	{ID: "xai", Name: "xAI (Grok)", Envs: []string{"XAI_API_KEY"}},
	{ID: "anthropic", Name: "Anthropic", Envs: []string{"ANTHROPIC_API_KEY"}},
	{ID: "openai", Name: "OpenAI", Envs: []string{"OPENAI_API_KEY"}},
	{ID: "google", Name: "Google", Envs: []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"}},
	{ID: "openrouter", Name: "OpenRouter", Envs: []string{"OPENROUTER_API_KEY"}},
	{ID: "deepseek", Name: "DeepSeek", Envs: []string{"DEEPSEEK_API_KEY"}},
	{ID: "mistral", Name: "Mistral", Envs: []string{"MISTRAL_API_KEY"}},
	{ID: "glm", Name: "Zhipu GLM (open.bigmodel.cn)", Envs: []string{"GLM_API_KEY"}},
	{ID: "zai", Name: "z.ai (GLM, international)", Envs: []string{"ZAI_API_KEY"}},
	{ID: "kimi", Name: "Kimi (Moonshot AI)", Envs: []string{"KIMI_API_KEY"}},
	{ID: "groq", Name: "Groq", Envs: []string{"GROQ_API_KEY"}},
	{ID: "cerebras", Name: "Cerebras", Envs: []string{"CEREBRAS_API_KEY"}},
	{ID: "zenmux", Name: "ZenMux", Envs: []string{"ZENMUX_API_KEY"}},
	{ID: "vercel", Name: "Vercel AI Gateway", Envs: []string{"AI_GATEWAY_API_KEY", "VERCEL_API_KEY"}},
	{ID: "bedrock", Name: "Amazon Bedrock", Envs: []string{"AWS_ACCESS_KEY_ID", "AWS_BEARER_TOKEN_BEDROCK"}},
	{ID: "ollama", Name: "Ollama (local)", Keyless: true},
}

// IsProvider reports a built-in provider id.
func IsProvider(id string) bool {
	_, ok := LookupProvider(id)
	return ok
}

// LookupProvider returns the descriptor for id.
func LookupProvider(id string) (Provider, bool) {
	id = canonicalProvider(id)
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Parse splits a loop-style id. The first slash (or colon, when the left
// side is a known provider) is the split, so gateway ids keep their slashes.
func Parse(s, currentProvider string) (provider, model string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return currentProvider, ""
	}
	if i := strings.IndexByte(s, '/'); i > 0 && IsProvider(s[:i]) {
		return canonicalProvider(s[:i]), s[i+1:]
	}
	if i := strings.IndexByte(s, ':'); i > 0 && IsProvider(s[:i]) {
		return canonicalProvider(s[:i]), s[i+1:]
	}
	return currentProvider, s
}

// FullID is provider/short.
func FullID(provider, model string) string {
	if provider == "" {
		return model
	}
	return provider + "/" + model
}

func canonicalProvider(id string) string {
	id = strings.ToLower(id)
	if id == "gemini" {
		return "google"
	}
	return id
}

// Lookup finds a model by full id, or by short id on provider (loop's
// modelInfoIn: custom gateway models the catalog does not list still
// resolve). The trailing argument is accepted for older callers and ignored:
// the Kimi plan is detected from the stored key.
func Lookup(provider, model string, _ ...string) (Model, bool) {
	provider = canonicalProvider(provider)
	c := current()
	if m, ok := c.byID[FullID(provider, model)]; ok {
		return m, true
	}
	if m, ok := c.byID[model]; ok && (provider == "" || m.Provider == provider) {
		return m, true
	}
	if strings.HasPrefix(provider, "custom:") {
		if m, ok := c.customFallback(FullID(provider, model)); ok {
			return m, true
		}
	}
	for _, m := range c.models {
		if m.Provider == provider && m.ShortID == model {
			return m, true
		}
	}
	return Model{}, false
}

// Models is a provider's models in catalog order.
func Models(provider string, _ ...string) []Model {
	provider = canonicalProvider(provider)
	var out []Model
	for _, m := range current().models {
		if m.Provider == provider {
			out = append(out, m)
		}
	}
	return out
}

// Default is the first catalog entry for the provider.
func Default(provider string, _ ...string) (Model, bool) {
	ms := Models(provider)
	if len(ms) == 0 {
		return Model{}, false
	}
	return ms[0], true
}

// All is every model, in catalog order.
func All(_ ...string) []Model { return append([]Model(nil), current().models...) }
