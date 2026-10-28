package agent

import (
	"maps"
	"regexp"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/config"
)

// Loop's thinking.ts: what a thinking level becomes on the wire for each
// provider — the portable effort the SDK translates, provider options where
// the SDK's own translation is wrong or absent, or nothing at all.

// communityReasoning are providers whose effort must NOT go as the portable
// param (loop's COMMUNITY_REASONING_PROVIDERS): it does not flow through
// their SDK, or the backend rejects the param it would become.
var communityReasoning = map[string]bool{"ollama": true, "glm": true, "zai": true, "openrouter": true, "kimi": true}

var openrouterEffort = map[string]string{"minimal": "low", "low": "low", "medium": "medium", "high": "high", "xhigh": "high"}

// xaiSupportsEffort: xAI honours reasoning_effort only on grok-3-mini-class
// models; grok-4 and the previews reason internally and 400 on it.
func xaiSupportsEffort(model string) bool { return strings.Contains(strings.ToLower(model), "mini") }

// anthropicLegacyThinking is the closed set of models that still take the
// legacy {type: enabled, budget_tokens} shape.
var anthropicLegacyThinking = []string{"opus-4-5", "opus-4-1", "opus-4-0", "opus-4-2025", "sonnet-4-5", "sonnet-4-0", "sonnet-4-2025", "haiku-4-5"}

func anthropicAdaptive(model string) bool {
	for _, id := range anthropicLegacyThinking {
		if strings.Contains(model, id) {
			return false
		}
	}
	return true
}

var (
	// Omitting `thinking` runs adaptive thinking by default on these, so
	// effort alone is sent — the shape proxies (bifrost) pass through.
	anthropicAdaptiveWhenOmitted = regexp.MustCompile(`claude-(opus-5|sonnet-5|fable-5|mythos-(5|preview))`)
	anthropicAlwaysThinks        = regexp.MustCompile(`claude-(fable-5|mythos-(5|preview))`)
	anthropicXHigh               = regexp.MustCompile(`claude-(opus-5|opus-4-[7-9]|sonnet-5|fable-5|mythos-(5|preview))`)
	anthropicAdaptiveEffort      = map[string]string{"minimal": "low", "low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh"}
)

func anthropicThinkingOptions(model, level string) provider.ProviderOptions {
	if level == "off" {
		if anthropicAlwaysThinks.MatchString(model) {
			return nil
		}
		return provider.ProviderOptions{"anthropic": {"thinking": map[string]any{"type": "disabled"}}}
	}
	effort := anthropicAdaptiveEffort[level]
	if level == "xhigh" && !anthropicXHigh.MatchString(model) {
		effort = "max"
	}
	if anthropicAdaptiveWhenOmitted.MatchString(model) {
		return provider.ProviderOptions{"anthropic": {"effort": effort}}
	}
	return provider.ProviderOptions{"anthropic": {"thinking": map[string]any{"type": "adaptive", "display": "summarized"}, "effort": effort}}
}

// reasoningParams is loop's buildReasoningParams for one call: the effort to
// send (empty for none) and provider options keyed by pi's provider ids.
// prov is the effective SDK provider; key is the id its options go under.
func reasoningParams(prov, key, model, level string, capable, known bool) (provider.ReasoningEffort, provider.ProviderOptions) {
	if !capable || level == "" {
		return "", nil
	}
	if prov == "anthropic" && anthropicAdaptive(model) {
		return "", rekey(anthropicThinkingOptions(model, level), "anthropic", key)
	}
	var effort provider.ReasoningEffort
	switch {
	case communityReasoning[prov]:
	case prov == "xai" && level != "off" && !xaiSupportsEffort(model):
	case level == "off":
		effort = provider.ReasoningNone
	default:
		effort = provider.ReasoningEffort(level)
	}
	on := level != "off"
	var opts provider.ProviderOptions
	switch prov {
	case "openai":
		if on {
			block := provider.JSONObject{"reasoningSummary": "auto"}
			// Gateway-prefixed ids defeat the SDK's id-based capability guess.
			if known && strings.Contains(model, "/") {
				block["forceReasoning"] = true
			}
			opts = provider.ProviderOptions{key: block}
		}
	case "openrouter":
		if on {
			opts = provider.ProviderOptions{key: {"reasoning": map[string]any{"effort": openrouterEffort[level]}}}
		}
	case "glm", "zai", "kimi":
		// A boolean toggle, merged into the body as the SDKs forward it.
		kind := "disabled"
		if on {
			kind = "enabled"
		}
		opts = provider.ProviderOptions{key: {"thinking": map[string]any{"type": kind}}}
	case "ollama":
		opts = provider.ProviderOptions{key: {"think": on}}
	}
	return effort, opts
}

func rekey(o provider.ProviderOptions, from, to string) provider.ProviderOptions {
	if o == nil || from == to {
		return o
	}
	return provider.ProviderOptions{to: o[from]}
}

// applyReasoning shapes a call's reasoning the way loop's
// buildAgentCallConfig does, from the effort the run was configured with.
func applyReasoning(opts *ai.Options, cfg config.Config) {
	level := config.EffortToThinking(opts.Reasoning)
	if level == "" {
		return
	}
	prov := config.EffectiveSDKProvider(cfg.Provider)
	// A builtin provider reads its options under its own id; a custom one
	// under its SDK's.
	key := cfg.Provider
	if prov != cfg.Provider {
		key = prov
	}
	// loop: reasoning-capable unless the catalog says otherwise, and known
	// to reason only when it says so. A custom model's entry carries no
	// reasoning flag, so it counts as unknown.
	capable, known := true, false
	if !config.IsCustom(cfg.Provider) && prov == cfg.Provider {
		if m, ok := config.ModelInfo(cfg.Provider, cfg.ModelID); ok {
			capable, known = m.Reasoning, m.Reasoning
		}
	}
	effort, po := reasoningParams(prov, key, cfg.ModelID, level, capable, known)
	opts.Reasoning = effort
	if len(po) == 0 {
		return
	}
	merged := maps.Clone(opts.ProviderOptions)
	if merged == nil {
		merged = provider.ProviderOptions{}
	}
	for k, block := range po {
		b := maps.Clone(merged[k])
		if b == nil {
			b = provider.JSONObject{}
		}
		for f, v := range block {
			if _, set := b[f]; !set {
				b[f] = v
			}
		}
		merged[k] = b
	}
	opts.ProviderOptions = merged
}
