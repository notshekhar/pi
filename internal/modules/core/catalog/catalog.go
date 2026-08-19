// Package catalog is loop's curated model seed: provider id, short id,
// display name, context, and whether the model thinks.
//
// Full ids are "provider/model". OpenRouter and similar gateways keep
// slashes in the model half (openrouter/anthropic/claude-sonnet-4-6).
package catalog

import (
	"fmt"
	"strings"
)

// Cost is USD per million tokens.
type Cost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// Model is one catalog entry.
type Model struct {
	// ID is provider/short, the same shape loop uses.
	ID        string
	Provider  string
	ShortID   string
	Name      string
	Context   int
	MaxOut    int
	Cost      Cost
	Reasoning bool
}

// Provider is a built-in vendor or gateway.
type Provider struct {
	ID      string
	Name    string
	Envs    []string
	Keyless bool
}

// Providers is loop's built-in list, minus OAuth-only entries
// (github-copilot, openai-chatgpt) that pigo cannot sign in yet.
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
	id = strings.ToLower(id)
	if id == "gemini" {
		return true
	}
	for _, p := range Providers {
		if p.ID == id {
			return true
		}
	}
	return false
}

// LookupProvider returns the descriptor for id.
func LookupProvider(id string) (Provider, bool) {
	id = strings.ToLower(id)
	if id == "gemini" {
		id = "google"
	}
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
		return strings.ToLower(s[:i]), s[i+1:]
	}
	if i := strings.IndexByte(s, ':'); i > 0 && IsProvider(s[:i]) {
		return strings.ToLower(s[:i]), s[i+1:]
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

// Lookup finds a model by full id or by short id on provider.
//
// Takes the key for the same reason Models does: Kimi's Code-plan catalog is
// a different list, so a lookup without the key silently misses every model
// on it — and a missed lookup means no context window and no pricing, which
// shows up as a session that reports $0.0000 forever rather than as an error.
func Lookup(provider, model string, kimiKey ...string) (Model, bool) {
	want := FullID(canonicalProvider(provider), model)
	for _, m := range Models(provider, kimiKey...) {
		if m.ID == want || m.ShortID == model {
			return m, true
		}
	}
	return Model{}, false
}

// Models returns the seed list for a provider. Kimi swaps in the Code
// plan catalog when the key is a subscription key.
func Models(provider string, kimiKey ...string) []Model {
	provider = canonicalProvider(provider)
	if provider == "kimi" && len(kimiKey) > 0 && strings.HasPrefix(kimiKey[0], "sk-kimi-") {
		return append([]Model{}, kimiCode...)
	}
	var out []Model
	for _, m := range catalog {
		if m.Provider == provider {
			out = append(out, m)
		}
	}
	return out
}

// Default is the first catalog entry for the provider.
func Default(provider string, kimiKey ...string) (Model, bool) {
	ms := Models(provider, kimiKey...)
	if len(ms) == 0 {
		return Model{}, false
	}
	return ms[0], true
}

// All returns every seed model, with Kimi already swapped when needed.
func All(kimiKey string) []Model {
	var out []Model
	seenKimi := false
	for _, m := range catalog {
		if m.Provider == "kimi" {
			if !seenKimi {
				out = append(out, Models("kimi", kimiKey)...)
				seenKimi = true
			}
			continue
		}
		out = append(out, m)
	}
	return out
}

func canonicalProvider(id string) string {
	id = strings.ToLower(id)
	if id == "gemini" {
		return "google"
	}
	return id
}

func m(provider, id, name string, ctx, maxOut int, cost Cost, reasoning bool) Model {
	return Model{
		ID:        fmt.Sprintf("%s/%s", provider, id),
		Provider:  provider,
		ShortID:   id,
		Name:      name,
		Context:   ctx,
		MaxOut:    maxOut,
		Cost:      cost,
		Reasoning: reasoning,
	}
}
