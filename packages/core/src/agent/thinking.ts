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

const BUDGETS: Record<Exclude<ThinkingLevel, "off">, number> = {
  minimal: 1024,
  low: 2048,
  medium: 8192,
  high: 16384,
  xhigh: 32768,
};

const OPENAI_EFFORT: Record<Exclude<ThinkingLevel, "off">, "minimal" | "low" | "medium" | "high"> = {
  minimal: "minimal",
  low: "low",
  medium: "medium",
  high: "high",
  xhigh: "high",
};

const XAI_EFFORT: Record<Exclude<ThinkingLevel, "off">, "low" | "high"> = {
  minimal: "low",
  low: "low",
  medium: "high",
  high: "high",
  xhigh: "high",
};

const OPENROUTER_EFFORT: Record<Exclude<ThinkingLevel, "off">, "low" | "medium" | "high"> = {
  minimal: "low",
  low: "low",
  medium: "medium",
  high: "high",
  xhigh: "high",
};

/**
 * Translate a generic ThinkingLevel into AI-SDK v6 `providerOptions` for the
 * given provider. Returns undefined when no provider-specific options apply.
 *
 * Provider-specific keys:
 *  - anthropic: `thinking: { type, budgetTokens }` + `sendReasoning`
 *  - openai:    `reasoningEffort: minimal|low|medium|high`
 *  - google:    `thinkingConfig: { thinkingBudget, includeThoughts }`
 *  - xai:       `reasoningEffort: low|high`
 *  - openrouter:`reasoning: { effort } | { exclude: true }`
 */
/**
 * xAI only honors `reasoning_effort` on grok-3-mini-class models. Grok-4 and
 * preview models (grok-build, grok-4.20-*) reason internally and reject the
 * parameter with a 400 (see x.ai/api error: "Model X does not support
 * parameter reasoningEffort.").
 */
function xaiSupportsEffort(modelShortId: string): boolean {
  return /mini/i.test(modelShortId);
}

export function buildProviderOptions(
  provider: ProviderId | string,
  level: ThinkingLevel,
  modelShortId = "",
): Record<string, Record<string, unknown>> | undefined {
  if (level === "off") {
    switch (provider) {
      case "anthropic":
        return { anthropic: { thinking: { type: "disabled" } } };
      case "google":
        return { google: { thinkingConfig: { thinkingBudget: 0 } } };
      case "ollama":
        return { ollama: { think: false } };
      default:
        return undefined;
    }
  }

  const lv = level as Exclude<ThinkingLevel, "off">;
  const budget = BUDGETS[lv];

  switch (provider) {
    case "anthropic":
      return {
        anthropic: {
          thinking: { type: "enabled", budgetTokens: budget },
          sendReasoning: true,
        },
      };
    case "openai":
    case "github-copilot":
      return { openai: { reasoningEffort: OPENAI_EFFORT[lv], reasoningSummary: "auto" } };
    case "google":
      return { google: { thinkingConfig: { thinkingBudget: budget, includeThoughts: true } } };
    case "xai":
      return xaiSupportsEffort(modelShortId) ? { xai: { reasoningEffort: XAI_EFFORT[lv] } } : undefined;
    case "openrouter":
      return { openrouter: { reasoning: { effort: OPENROUTER_EFFORT[lv] } } };
    case "ollama":
      // Ollama thinking is a boolean toggle (DeepSeek-R1, Qwen3, etc.); no budget.
      return { ollama: { think: true } };
    default:
      return undefined;
  }
}
