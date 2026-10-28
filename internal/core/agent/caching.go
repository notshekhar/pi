package agent

import (
	"context"
	"maps"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/config"
)

// Loop's model-call.ts and model-messages.ts: Anthropic only caches up to an
// explicit cache_control breakpoint, so a request without one re-bills the
// whole conversation at full input price every step. Other providers
// (OpenAI, xAI, Google, DeepSeek, Kimi) cache automatically server-side.

// AnthropicCaching reports a request shaped for Anthropic's API (loop's
// buildAgentCallConfig.anthropicCaching): the anthropic provider, or a
// custom gateway speaking its SDK.
func AnthropicCaching(prov string) bool {
	return config.EffectiveSDKProvider(prov) == "anthropic"
}

// cacheShape is how one provider takes an explicit breakpoint: the
// provider-options key its converter reads, and the mark it expects there.
type cacheShape struct {
	key  string
	mark provider.JSONObject
}

func (c cacheShape) options() provider.ProviderOptions {
	return provider.ProviderOptions{c.key: maps.Clone(c.mark)}
}

var anthropicShape = cacheShape{"anthropic", provider.JSONObject{"cacheControl": map[string]any{"type": "ephemeral"}}}

// explicitCacheShape is how a request must mark its cache breakpoints, or
// false for a provider that caches on its own. Beyond loop: Claude through
// OpenRouter's chat completions caches only past a cache_control (OpenRouter
// relays it), and Bedrock's Claude and Nova models only past a cachePoint.
func explicitCacheShape(cfg config.Config) (cacheShape, bool) {
	switch {
	case AnthropicCaching(cfg.Provider):
		return anthropicShape, true
	case config.EffectiveSDKProvider(cfg.Provider) == "openrouter" && strings.HasPrefix(cfg.ModelID, "anthropic/"):
		return cacheShape{"openrouter", anthropicShape.mark}, true
	case cfg.Provider == "bedrock" && (strings.Contains(cfg.ModelID, "anthropic.") || strings.Contains(cfg.ModelID, "amazon.nova")):
		return cacheShape{"bedrock", provider.JSONObject{"cachePoint": map[string]any{"type": "default"}}}, true
	}
	return cacheShape{}, false
}

// AnthropicCachedSystem is the system message with a cache breakpoint — the
// cache prefix is tools → system, so one anchor covers both.
func AnthropicCachedSystem(system string) provider.SystemMessage {
	return provider.SystemMessage{Content: system, ProviderOptions: anthropicShape.options()}
}

// WithAnthropicCaching is loop's withAnthropicCaching: two ephemeral
// breakpoints (the limit is 4). One on the system message, one on the last
// message so the whole conversation prefix is a cache read next turn.
func WithAnthropicCaching(system string, messages []provider.Message) []provider.Message {
	return withCaching(anthropicShape, system, messages)
}

func withCaching(c cacheShape, system string, messages []provider.Message) []provider.Message {
	out := make([]provider.Message, 0, len(messages)+1)
	out = append(out, provider.SystemMessage{Content: system, ProviderOptions: c.options()})
	out = append(out, messages...)
	out[len(out)-1] = withOptions(out[len(out)-1], func(provider.ProviderOptions) provider.ProviderOptions {
		return c.options()
	})
	return out
}

// MoveAnthropicCacheTail is loop's moveAnthropicCacheTail, the per-step
// moving breakpoint: re-anchoring the last message each step makes step N+1
// a cache read of everything step N sent, instead of re-billing every tool
// result at full price (quadratic in steps). The previous tail anchor is
// stripped (system anchors stay) so a long run never exceeds four.
func MoveAnthropicCacheTail(messages []provider.Message) []provider.Message {
	return moveCacheTail(anthropicShape, messages)
}

func moveCacheTail(c cacheShape, messages []provider.Message) []provider.Message {
	if len(messages) == 0 {
		return messages
	}
	out := make([]provider.Message, len(messages))
	for i, m := range messages {
		if _, system := m.(provider.SystemMessage); system || options(m).Get(c.key) == nil {
			out[i] = m
			continue
		}
		out[i] = withOptions(m, func(o provider.ProviderOptions) provider.ProviderOptions {
			rest := maps.Clone(o)
			delete(rest, c.key)
			if len(rest) == 0 {
				return nil
			}
			return rest
		})
	}
	last := out[len(out)-1]
	if _, system := last.(provider.SystemMessage); system {
		return out
	}
	out[len(out)-1] = withOptions(last, func(o provider.ProviderOptions) provider.ProviderOptions {
		merged := maps.Clone(o)
		if merged == nil {
			merged = provider.ProviderOptions{}
		}
		maps.Copy(merged, c.options())
		return merged
	})
	return out
}

// AnthropicPrepareStep re-anchors the tail breakpoint before every step.
func AnthropicPrepareStep(_ context.Context, step ai.StepContext) (ai.StepOverrides, error) {
	return ai.StepOverrides{Messages: MoveAnthropicCacheTail(step.Messages)}, nil
}

// cachedOptions shapes a run's prompt for its provider: where caching needs
// explicit breakpoints the system prompt moves into the messages with one,
// a moving tail anchor is added per step, and for Anthropic output is capped
// at the model's real maximum (loop's buildAgentCallConfig). Providers that
// cache on their own are left alone.
//
// conv is the conversation the request belongs to. xAI keeps its cache on
// the server that answered, and routes requests carrying the same
// x-grok-conv-id to the same server; without it a step lands anywhere and
// re-reads the whole prompt at full price.
func cachedOptions(opts ai.Options, cfg config.Config, conv string) ai.Options {
	if config.EffectiveSDKProvider(cfg.Provider) == "xai" && conv != "" {
		headers := maps.Clone(opts.Headers)
		if headers == nil {
			headers = provider.Headers{}
		}
		headers["x-grok-conv-id"] = conv
		opts.Headers = headers
	}
	shape, ok := explicitCacheShape(cfg)
	if !ok {
		return opts
	}
	opts.Messages = withCaching(shape, opts.System, opts.Messages)
	opts.System = ""
	opts.PrepareStep = func(_ context.Context, step ai.StepContext) (ai.StepOverrides, error) {
		return ai.StepOverrides{Messages: moveCacheTail(shape, step.Messages)}, nil
	}
	if AnthropicCaching(cfg.Provider) && opts.MaxOutputTokens == nil {
		if m, ok := config.ModelInfo(cfg.Provider, cfg.ModelID); ok && m.MaxOut > 0 {
			n := int64(m.MaxOut)
			opts.MaxOutputTokens = &n
		}
	}
	return opts
}

func options(m provider.Message) provider.ProviderOptions {
	switch v := m.(type) {
	case provider.SystemMessage:
		return v.ProviderOptions
	case provider.UserMessage:
		return v.ProviderOptions
	case provider.AssistantMessage:
		return v.ProviderOptions
	case provider.ToolMessage:
		return v.ProviderOptions
	}
	return nil
}

// withOptions copies a message with its provider options replaced by f's.
func withOptions(m provider.Message, f func(provider.ProviderOptions) provider.ProviderOptions) provider.Message {
	switch v := m.(type) {
	case provider.SystemMessage:
		v.ProviderOptions = f(v.ProviderOptions)
		return v
	case provider.UserMessage:
		v.ProviderOptions = f(v.ProviderOptions)
		return v
	case provider.AssistantMessage:
		v.ProviderOptions = f(v.ProviderOptions)
		return v
	case provider.ToolMessage:
		v.ProviderOptions = f(v.ProviderOptions)
		return v
	}
	return m
}
