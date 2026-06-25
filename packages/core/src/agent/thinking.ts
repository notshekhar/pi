import type { ProviderId } from "../types";

export type ThinkingLevel = "off" | "minimal" | "low" | "medium" | "high" | "xhigh";

export const THINKING_LEVELS: ThinkingLevel[] = ["off", "minimal", "low", "medium", "high", "xhigh"];

export const THINKING_LEVEL_DESCRIPTIONS: Record<ThinkingLevel, string> = {
    off: "No reasoning",
    minimal: "Very brief reasoning (~1k tokens)",
    low: "Light reasoning (~2k tokens)",
    medium: "Moderate reasoning (~8k tokens)",
    high: "Deep reasoning (~16k tokens)",
    xhigh: "Maximum reasoning (~32k tokens)",
};

/**
 * AI SDK v7's portable `reasoning` effort value. Our ThinkingLevel maps onto it
 * 1:1 (`off` → `none`), so first-party providers translate the level into the
 * right provider-specific request themselves — no hand-rolled per-provider
 * budget/effort tables needed (the SDK owns anthropic adaptive-thinking,
 * openai reasoningEffort, google thinkingConfig, etc).
 */
export type ReasoningEffort = "none" | "minimal" | "low" | "medium" | "high" | "xhigh";

/**
 * Providers reached through a community AI-SDK package (not a first-party
 * @ai-sdk provider) — the portable `reasoning` param doesn't flow through them,
 * so they keep the hand-built `providerOptions` path in buildProviderOptions.
 * Everything else (anthropic/openai/google/xai/groq/mistral/deepseek/cerebras,
 * plus the openai-compatible routes: zenmux/github-copilot/chatgpt) uses the
 * native `reasoning` param.
 */
const COMMUNITY_REASONING_PROVIDERS = new Set(["ollama", "glm", "zai", "openrouter"]);

const OPENROUTER_EFFORT: Record<Exclude<ThinkingLevel, "off">, "low" | "medium" | "high"> = {
    minimal: "low",
    low: "low",
    medium: "medium",
    high: "high",
    xhigh: "high",
};

/**
 * xAI only honors `reasoning_effort` on grok-3-mini-class models. Grok-4 and
 * preview models (grok-build, grok-4.20-*) reason internally and reject the
 * parameter with a 400 (see x.ai/api error: "Model X does not support
 * parameter reasoningEffort.").
 */
function xaiSupportsEffort(modelShortId: string): boolean {
    return /mini/i.test(modelShortId);
}

/**
 * The portable v7 `reasoning` value for a provider + level, or undefined when
 * this provider should NOT use the native param:
 *  - community providers (ollama/glm/zai) → handled by buildProviderOptions;
 *  - non-mini xAI models that 400 on reasoning effort → omit entirely.
 */
export function reasoningEffort(
    provider: ProviderId | string,
    level: ThinkingLevel,
    modelShortId = "",
): ReasoningEffort | undefined {
    if (COMMUNITY_REASONING_PROVIDERS.has(provider)) return undefined;
    if (provider === "xai" && level !== "off" && !xaiSupportsEffort(modelShortId)) return undefined;
    return level === "off" ? "none" : level;
}

/**
 * Provider-specific reasoning options the portable `reasoning` param does NOT
 * express. Returns undefined when the native param fully covers the provider.
 *
 *  - openai / github-copilot: `reasoningSummary: "auto"` so reasoning summaries
 *    still surface (additive — composes with the native effort);
 *  - openrouter: `reasoning: { effort }` (community provider, own option shape);
 *  - glm/zai:  zhipu `thinking: { type: enabled|disabled }` (boolean, no budget);
 *  - ollama:   `think: boolean` (DeepSeek-R1, Qwen3, …; no budget).
 */
export function buildProviderOptions(
    provider: ProviderId | string,
    level: ThinkingLevel,
): Record<string, Record<string, unknown>> | undefined {
    const on = level !== "off";
    switch (provider) {
        case "openai":
        case "github-copilot":
            // Effort comes from the native `reasoning` param; this only adds the
            // human-readable summary stream. Skip when reasoning is off.
            return on ? { openai: { reasoningSummary: "auto" } } : undefined;
        case "openrouter":
            return on
                ? { openrouter: { reasoning: { effort: OPENROUTER_EFFORT[level as Exclude<ThinkingLevel, "off">] } } }
                : undefined;
        case "glm":
        case "zai":
            // GLM-4.5+ thinking is a boolean toggle (no budget), merged into the
            // request body via the zhipu provider's providerOptions.
            return { zhipu: { thinking: { type: on ? "enabled" : "disabled" } } };
        case "ollama":
            return { ollama: { think: on } };
        default:
            return undefined;
    }
}
