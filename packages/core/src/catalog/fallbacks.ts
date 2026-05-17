/**
 * Curated latest models per provider with pricing.
 * Used as catalog seed — overlaid by models.dev + live availability + user overrides.
 */
import type { ModelInfo, ProviderId } from "../types";

const COST_ZERO = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };

function m(
  provider: ProviderId,
  id: string,
  name: string,
  ctx: number,
  maxOut: number,
  cost: { input: number; output: number; cacheRead?: number; cacheWrite?: number },
  reasoning = false,
  modalities: string[] = ["text"],
  kind: "language-model" | "external-agent" = "language-model",
): ModelInfo {
  return {
    id: `${provider}/${id}`,
    provider,
    name,
    contextWindow: ctx,
    maxOutput: maxOut,
    cost: {
      input: cost.input,
      output: cost.output,
      cacheRead: cost.cacheRead ?? 0,
      cacheWrite: cost.cacheWrite ?? 0,
    },
    reasoning,
    modalities,
    available: true,
    kind,
  };
}

// xAI — pi-grok subscription + public API
const XAI: ModelInfo[] = [
  m("xai", "grok-build", "Grok Build", 1_000_000, 30_000, COST_ZERO, true, ["text", "image"]),
  m("xai", "grok-4.3", "Grok 4.3", 1_000_000, 30_000, { input: 1.25, output: 2.5, cacheRead: 0.2 }, true, ["text", "image"]),
  m("xai", "grok-4.20-0309-reasoning", "Grok 4.20 Reasoning", 2_000_000, 30_000, { input: 2, output: 6, cacheRead: 0.2 }, true, ["text", "image"]),
  m("xai", "grok-4.20-0309-non-reasoning", "Grok 4.20 Non-Reasoning", 2_000_000, 30_000, { input: 2, output: 6, cacheRead: 0.2 }, false, ["text", "image"]),
  m("xai", "grok-4.20-multi-agent-0309", "Grok 4.20 Multi-Agent", 2_000_000, 30_000, { input: 2, output: 6, cacheRead: 0.2 }, true, ["text", "image"]),
  m("xai", "grok-4-fast", "Grok 4 Fast", 2_000_000, 30_000, { input: 0.2, output: 0.5, cacheRead: 0.05 }, true, ["text", "image"]),
  m("xai", "grok-4", "Grok 4", 256_000, 30_000, { input: 3, output: 15, cacheRead: 0.75 }, true, ["text", "image"]),
  m("xai", "grok-code-fast-1", "Grok Code Fast 1", 256_000, 30_000, { input: 0.2, output: 1.5, cacheRead: 0.02 }, true),
  m("xai", "grok-3", "Grok 3", 131_072, 8_192, { input: 3, output: 15 }),
];

// Anthropic — Claude 4.x
const ANTHROPIC: ModelInfo[] = [
  m("anthropic", "claude-opus-4-7", "Claude Opus 4.7", 1_000_000, 128_000, { input: 15, output: 75, cacheRead: 1.5, cacheWrite: 18.75 }, true, ["text", "image"]),
  m("anthropic", "claude-sonnet-4-6", "Claude Sonnet 4.6", 1_000_000, 128_000, { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 }, true, ["text", "image"]),
  m("anthropic", "claude-haiku-4-5", "Claude Haiku 4.5", 200_000, 64_000, { input: 0.8, output: 4, cacheRead: 0.08, cacheWrite: 1 }, true, ["text", "image"]),
  m("anthropic", "claude-opus-4-5", "Claude Opus 4.5", 1_000_000, 128_000, { input: 15, output: 75, cacheRead: 1.5, cacheWrite: 18.75 }, true, ["text", "image"]),
  m("anthropic", "claude-sonnet-4-5", "Claude Sonnet 4.5", 1_000_000, 128_000, { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 }, true, ["text", "image"]),
];

// OpenAI — GPT-5 family + o-series
const OPENAI: ModelInfo[] = [
  m("openai", "gpt-5", "GPT-5", 400_000, 128_000, { input: 1.25, output: 10, cacheRead: 0.125 }, true, ["text", "image"]),
  m("openai", "gpt-5-mini", "GPT-5 Mini", 400_000, 128_000, { input: 0.25, output: 2, cacheRead: 0.025 }, true, ["text", "image"]),
  m("openai", "gpt-5-nano", "GPT-5 Nano", 400_000, 128_000, { input: 0.05, output: 0.4, cacheRead: 0.005 }, true, ["text"]),
  m("openai", "o3", "o3", 200_000, 100_000, { input: 2, output: 8, cacheRead: 0.5 }, true, ["text", "image"]),
  m("openai", "o3-mini", "o3 Mini", 200_000, 100_000, { input: 1.1, output: 4.4, cacheRead: 0.55 }, true),
  m("openai", "gpt-4.1", "GPT-4.1", 1_000_000, 32_000, { input: 2, output: 8, cacheRead: 0.5 }, false, ["text", "image"]),
  m("openai", "gpt-4.1-mini", "GPT-4.1 Mini", 1_000_000, 32_000, { input: 0.4, output: 1.6, cacheRead: 0.1 }, false, ["text", "image"]),
];

// Google — Gemini 2.5
const GOOGLE: ModelInfo[] = [
  m("google", "gemini-2.5-pro", "Gemini 2.5 Pro", 2_000_000, 64_000, { input: 1.25, output: 10, cacheRead: 0.31 }, true, ["text", "image"]),
  m("google", "gemini-2.5-flash", "Gemini 2.5 Flash", 1_000_000, 64_000, { input: 0.3, output: 2.5, cacheRead: 0.075 }, true, ["text", "image"]),
  m("google", "gemini-2.5-flash-lite", "Gemini 2.5 Flash Lite", 1_000_000, 64_000, { input: 0.1, output: 0.4 }, false, ["text", "image"]),
];

// OpenRouter — popular routes (full list comes from models.dev / live)
const OPENROUTER: ModelInfo[] = [
  m("openrouter", "anthropic/claude-opus-4-7", "OR · Claude Opus 4.7", 1_000_000, 128_000, { input: 15, output: 75 }, true, ["text", "image"]),
  m("openrouter", "anthropic/claude-sonnet-4-6", "OR · Claude Sonnet 4.6", 1_000_000, 128_000, { input: 3, output: 15 }, true, ["text", "image"]),
  m("openrouter", "openai/gpt-5", "OR · GPT-5", 400_000, 128_000, { input: 1.25, output: 10 }, true, ["text", "image"]),
  m("openrouter", "google/gemini-2.5-pro", "OR · Gemini 2.5 Pro", 2_000_000, 64_000, { input: 1.25, output: 10 }, true, ["text", "image"]),
  m("openrouter", "x-ai/grok-4", "OR · Grok 4", 256_000, 30_000, { input: 3, output: 15 }, true, ["text", "image"]),
  m("openrouter", "meta-llama/llama-3.3-70b-instruct", "OR · Llama 3.3 70B", 131_072, 16_000, { input: 0.13, output: 0.4 }),
];

// GitHub Copilot — proxied OpenAI/Anthropic models (subscription-billed)
const GITHUB_COPILOT: ModelInfo[] = [
  m("github-copilot", "gpt-5", "Copilot · GPT-5", 400_000, 128_000, { input: 0, output: 0 }, true, ["text", "image"]),
  m("github-copilot", "gpt-5-mini", "Copilot · GPT-5 Mini", 400_000, 128_000, { input: 0, output: 0 }, true, ["text", "image"]),
  m("github-copilot", "claude-opus-4-7", "Copilot · Claude Opus 4.7", 200_000, 64_000, { input: 0, output: 0 }, true, ["text", "image"]),
  m("github-copilot", "claude-sonnet-4-6", "Copilot · Claude Sonnet 4.6", 200_000, 64_000, { input: 0, output: 0 }, true, ["text", "image"]),
  m("github-copilot", "gemini-2.5-pro", "Copilot · Gemini 2.5 Pro", 1_000_000, 64_000, { input: 0, output: 0 }, true, ["text", "image"]),
];

// Claude Agent SDK — external agent runtime (Anthropic-billed or Pro/Max OAuth)
const CLAUDE_AGENT: ModelInfo[] = [
  m("claude-agent", "claude-opus-4-7", "Claude Agent · Opus 4.7", 1_000_000, 128_000, { input: 15, output: 75, cacheRead: 1.5, cacheWrite: 18.75 }, true, ["text", "image"], "external-agent"),
  m("claude-agent", "claude-sonnet-4-6", "Claude Agent · Sonnet 4.6", 1_000_000, 128_000, { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 }, true, ["text", "image"], "external-agent"),
  m("claude-agent", "claude-haiku-4-5", "Claude Agent · Haiku 4.5", 200_000, 64_000, { input: 0.8, output: 4, cacheRead: 0.08, cacheWrite: 1 }, true, ["text", "image"], "external-agent"),
];

// Cursor Agent SDK — Cursor-billed via Pro/Pro+/Ultra credit pool
const CURSOR_AGENT: ModelInfo[] = [
  m("cursor-agent", "composer-2", "Cursor · Composer 2", 200_000, 32_000, { input: 0.5, output: 2.5 }, true, ["text", "image"], "external-agent"),
  m("cursor-agent", "gpt-5.5", "Cursor · GPT-5.5", 400_000, 128_000, { input: 0, output: 0 }, true, ["text", "image"], "external-agent"),
  m("cursor-agent", "claude-opus-4-7", "Cursor · Claude Opus 4.7", 200_000, 64_000, { input: 0, output: 0 }, true, ["text", "image"], "external-agent"),
  m("cursor-agent", "claude-sonnet-4-6", "Cursor · Claude Sonnet 4.6", 200_000, 64_000, { input: 0, output: 0 }, true, ["text", "image"], "external-agent"),
];

export const FALLBACK_MODELS: ModelInfo[] = [
  ...XAI,
  ...ANTHROPIC,
  ...OPENAI,
  ...GOOGLE,
  ...OPENROUTER,
  ...GITHUB_COPILOT,
  ...CLAUDE_AGENT,
  ...CURSOR_AGENT,
];

export const XAI_FALLBACK_MODELS = XAI;
export const ANTHROPIC_FALLBACK_MODELS = ANTHROPIC;
export const OPENAI_FALLBACK_MODELS = OPENAI;
export const GOOGLE_FALLBACK_MODELS = GOOGLE;

export function fallbackModelsForSdk(sdk: "openai" | "anthropic" | "google" | "openai-compatible"): ModelInfo[] {
  if (sdk === "anthropic") return ANTHROPIC;
  if (sdk === "google") return GOOGLE;
  return OPENAI; // openai + openai-compatible default to OpenAI list
}
