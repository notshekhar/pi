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
    };
}

// xAI — loop-grok subscription + public API
const XAI: ModelInfo[] = [
    // Composer 2.5: xAI's agentic coding model. Not listed by /v1/models but
    // callable (subscription/preview — see catalog/index.ts note). It reasons
    // internally but rejects reasoningEffort with a 400, so reasoning:false here
    // forces providerOptions=undefined (no effort param is ever sent).
    // Pricing reverse-engineered from cost_in_usd_ticks, calibrated vs grok-4.3.
    m("xai", "composer-2.5", "Composer 2.5", 256_000, 30_000, { input: 0.9, output: 1.8, cacheRead: 0.2 }, false),
    m("xai", "grok-build-0.1", "Grok Build 0.1", 256_000, 256_000, { input: 1, output: 2, cacheRead: 0.2 }, true, [
        "text",
        "image",
    ]),
    m("xai", "grok-4.3", "Grok 4.3", 1_000_000, 30_000, { input: 1.25, output: 2.5, cacheRead: 0.2 }, true, [
        "text",
        "image",
    ]),
    m(
        "xai",
        "grok-4.20-0309-reasoning",
        "Grok 4.20 Reasoning",
        2_000_000,
        30_000,
        { input: 2, output: 6, cacheRead: 0.2 },
        true,
        ["text", "image"],
    ),
    m(
        "xai",
        "grok-4.20-0309-non-reasoning",
        "Grok 4.20 Non-Reasoning",
        2_000_000,
        30_000,
        { input: 2, output: 6, cacheRead: 0.2 },
        false,
        ["text", "image"],
    ),
    m(
        "xai",
        "grok-4.20-multi-agent-0309",
        "Grok 4.20 Multi-Agent",
        2_000_000,
        30_000,
        { input: 2, output: 6, cacheRead: 0.2 },
        true,
        ["text", "image"],
    ),
    m("xai", "grok-4-fast", "Grok 4 Fast", 2_000_000, 30_000, { input: 0.2, output: 0.5, cacheRead: 0.05 }, true, [
        "text",
        "image",
    ]),
    m("xai", "grok-4", "Grok 4", 256_000, 30_000, { input: 3, output: 15, cacheRead: 0.75 }, true, ["text", "image"]),
    m(
        "xai",
        "grok-code-fast-1",
        "Grok Code Fast 1",
        256_000,
        30_000,
        { input: 0.2, output: 1.5, cacheRead: 0.02 },
        true,
    ),
    m("xai", "grok-3", "Grok 3", 131_072, 8_192, { input: 3, output: 15 }),
];

// Anthropic — Claude 4.x / 5
const ANTHROPIC: ModelInfo[] = [
    m(
        "anthropic",
        "claude-opus-4-8",
        "Claude Opus 4.8",
        1_000_000,
        128_000,
        { input: 5, output: 25, cacheRead: 0.5, cacheWrite: 6.25 },
        true,
        ["text", "image"],
    ),
    m(
        "anthropic",
        "claude-sonnet-5",
        "Claude Sonnet 5",
        1_000_000,
        128_000,
        { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 },
        true,
        ["text", "image"],
    ),
    m(
        "anthropic",
        "claude-opus-4-7",
        "Claude Opus 4.7",
        1_000_000,
        128_000,
        { input: 5, output: 25, cacheRead: 0.5, cacheWrite: 6.25 },
        true,
        ["text", "image"],
    ),
    m(
        "anthropic",
        "claude-sonnet-4-6",
        "Claude Sonnet 4.6",
        1_000_000,
        128_000,
        { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 },
        true,
        ["text", "image"],
    ),
    m(
        "anthropic",
        "claude-haiku-4-5",
        "Claude Haiku 4.5",
        200_000,
        64_000,
        { input: 1, output: 5, cacheRead: 0.1, cacheWrite: 1.25 },
        true,
        ["text", "image"],
    ),
    m(
        "anthropic",
        "claude-opus-4-5",
        "Claude Opus 4.5",
        1_000_000,
        128_000,
        { input: 5, output: 25, cacheRead: 0.5, cacheWrite: 6.25 },
        true,
        ["text", "image"],
    ),
    m(
        "anthropic",
        "claude-sonnet-4-5",
        "Claude Sonnet 4.5",
        1_000_000,
        128_000,
        { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 },
        true,
        ["text", "image"],
    ),
];

// OpenAI — GPT-5 family + o-series
const OPENAI: ModelInfo[] = [
    m("openai", "gpt-5", "GPT-5", 400_000, 128_000, { input: 1.25, output: 10, cacheRead: 0.125 }, true, [
        "text",
        "image",
    ]),
    m("openai", "gpt-5-mini", "GPT-5 Mini", 400_000, 128_000, { input: 0.25, output: 2, cacheRead: 0.025 }, true, [
        "text",
        "image",
    ]),
    m("openai", "gpt-5-nano", "GPT-5 Nano", 400_000, 128_000, { input: 0.05, output: 0.4, cacheRead: 0.005 }, true, [
        "text",
    ]),
    m("openai", "o3", "o3", 200_000, 100_000, { input: 2, output: 8, cacheRead: 0.5 }, true, ["text", "image"]),
    m("openai", "o3-mini", "o3 Mini", 200_000, 100_000, { input: 1.1, output: 4.4, cacheRead: 0.55 }, true),
    m("openai", "gpt-4.1", "GPT-4.1", 1_000_000, 32_000, { input: 2, output: 8, cacheRead: 0.5 }, false, [
        "text",
        "image",
    ]),
    m("openai", "gpt-4.1-mini", "GPT-4.1 Mini", 1_000_000, 32_000, { input: 0.4, output: 1.6, cacheRead: 0.1 }, false, [
        "text",
        "image",
    ]),
];

// Google — Gemini 2.5
const GOOGLE: ModelInfo[] = [
    m(
        "google",
        "gemini-2.5-pro",
        "Gemini 2.5 Pro",
        2_000_000,
        64_000,
        { input: 1.25, output: 10, cacheRead: 0.31 },
        true,
        ["text", "image"],
    ),
    m(
        "google",
        "gemini-2.5-flash",
        "Gemini 2.5 Flash",
        1_000_000,
        64_000,
        { input: 0.3, output: 2.5, cacheRead: 0.075 },
        true,
        ["text", "image"],
    ),
    m(
        "google",
        "gemini-2.5-flash-lite",
        "Gemini 2.5 Flash Lite",
        1_000_000,
        64_000,
        { input: 0.1, output: 0.4 },
        false,
        ["text", "image"],
    ),
];

// OpenRouter — popular routes (full list comes from models.dev / live)
const OPENROUTER: ModelInfo[] = [
    m(
        "openrouter",
        "anthropic/claude-opus-4-7",
        "OR · Claude Opus 4.7",
        1_000_000,
        128_000,
        { input: 5, output: 25 },
        true,
        ["text", "image"],
    ),
    m(
        "openrouter",
        "anthropic/claude-sonnet-4-6",
        "OR · Claude Sonnet 4.6",
        1_000_000,
        128_000,
        { input: 3, output: 15 },
        true,
        ["text", "image"],
    ),
    m("openrouter", "openai/gpt-5", "OR · GPT-5", 400_000, 128_000, { input: 1.25, output: 10 }, true, [
        "text",
        "image",
    ]),
    m(
        "openrouter",
        "google/gemini-2.5-pro",
        "OR · Gemini 2.5 Pro",
        2_000_000,
        64_000,
        { input: 1.25, output: 10 },
        true,
        ["text", "image"],
    ),
    m("openrouter", "x-ai/grok-4", "OR · Grok 4", 256_000, 30_000, { input: 3, output: 15 }, true, ["text", "image"]),
    m("openrouter", "meta-llama/llama-3.3-70b-instruct", "OR · Llama 3.3 70B", 131_072, 16_000, {
        input: 0.13,
        output: 0.4,
    }),
    m(
        "openrouter",
        "nex-agi/nex-n2-pro:free",
        "OR · Nex-N2-Pro (free)",
        262_144,
        262_144,
        { input: 0, output: 0 },
        true,
        ["text", "image"],
    ),
];

// DeepSeek — chat (V3) + reasoner (R1). models.dev overrides pricing at runtime.
const DEEPSEEK: ModelInfo[] = [
    m("deepseek", "deepseek-chat", "DeepSeek Chat", 128_000, 8_192, { input: 0.28, output: 0.42, cacheRead: 0.028 }),
    m(
        "deepseek",
        "deepseek-reasoner",
        "DeepSeek Reasoner",
        128_000,
        64_000,
        {
            input: 0.28,
            output: 0.42,
            cacheRead: 0.028,
        },
        true,
    ),
];

// Mistral — flagship + small + Codestral. models.dev overrides pricing at runtime.
const MISTRAL: ModelInfo[] = [
    m("mistral", "mistral-large-latest", "Mistral Large", 131_072, 8_192, { input: 2, output: 6 }),
    m("mistral", "mistral-small-latest", "Mistral Small", 131_072, 8_192, { input: 0.1, output: 0.3 }),
    m("mistral", "magistral-medium-latest", "Magistral Medium", 40_960, 40_960, { input: 2, output: 5 }, true),
    m("mistral", "codestral-latest", "Codestral", 256_000, 8_192, { input: 0.3, output: 0.9 }),
];

// Zhipu GLM via open.bigmodel.cn. GLM-4.5+ are thinking-capable (reasoning:true).
const GLM: ModelInfo[] = [
    m("glm", "glm-5.2", "GLM-5.2", 200_000, 128_000, { input: 0.6, output: 2.2 }, true),
    m("glm", "glm-4.7", "GLM-4.7", 200_000, 128_000, { input: 0.6, output: 2.2 }, true),
    m("glm", "glm-4.6", "GLM-4.6", 200_000, 128_000, { input: 0.6, output: 2.2 }, true),
    m("glm", "glm-4.5-air", "GLM-4.5 Air", 128_000, 96_000, { input: 0.2, output: 1.1 }, true),
];

// Same GLM models via the international z.ai endpoint.
const ZAI: ModelInfo[] = [
    m("zai", "glm-5.2", "GLM-5.2 (z.ai)", 200_000, 128_000, { input: 0.6, output: 2.2 }, true),
    m("zai", "glm-4.7", "GLM-4.7 (z.ai)", 200_000, 128_000, { input: 0.6, output: 2.2 }, true),
    m("zai", "glm-4.6", "GLM-4.6 (z.ai)", 200_000, 128_000, { input: 0.6, output: 2.2 }, true),
    m("zai", "glm-4.5-air", "GLM-4.5 Air (z.ai)", 128_000, 96_000, { input: 0.2, output: 1.1 }, true),
];

// Groq — OSS models on LPU hardware. models.dev overrides pricing at runtime.
const GROQ: ModelInfo[] = [
    m("groq", "openai/gpt-oss-120b", "GPT-OSS 120B (Groq)", 131_072, 32_768, { input: 0.15, output: 0.75 }, true),
    m("groq", "openai/gpt-oss-20b", "GPT-OSS 20B (Groq)", 131_072, 32_768, { input: 0.1, output: 0.5 }, true),
    m("groq", "moonshotai/kimi-k2-instruct", "Kimi K2 (Groq)", 131_072, 16_384, { input: 1, output: 3 }),
    m("groq", "llama-3.3-70b-versatile", "Llama 3.3 70B (Groq)", 131_072, 32_768, { input: 0.59, output: 0.79 }),
];

// Cerebras — OSS models on wafer-scale hardware. models.dev overrides pricing.
const CEREBRAS: ModelInfo[] = [
    m("cerebras", "gpt-oss-120b", "GPT-OSS 120B (Cerebras)", 131_072, 32_768, { input: 0.25, output: 0.69 }, true),
    m("cerebras", "qwen-3-235b-a22b-instruct-2507", "Qwen3 235B (Cerebras)", 131_072, 32_768, {
        input: 0.6,
        output: 1.2,
    }),
    m("cerebras", "llama-3.3-70b", "Llama 3.3 70B (Cerebras)", 131_072, 32_768, { input: 0.85, output: 1.2 }),
];

// ZenMux — OpenAI-compatible gateway (author/model ids, like OpenRouter).
// Curated flagship seed; the full 200+ catalog is reachable via "+ add model".
const ZENMUX: ModelInfo[] = [
    m("zenmux", "z-ai/glm-5.2", "ZM · GLM-5.2", 200_000, 128_000, { input: 0.6, output: 2.2 }, true),
    m("zenmux", "anthropic/claude-opus-4.8", "ZM · Claude Opus 4.8", 200_000, 64_000, { input: 5, output: 25 }, true, [
        "text",
        "image",
    ]),
    m(
        "zenmux",
        "anthropic/claude-sonnet-4.6",
        "ZM · Claude Sonnet 4.6",
        200_000,
        64_000,
        { input: 3, output: 15 },
        true,
        ["text", "image"],
    ),
    m("zenmux", "openai/gpt-5.5", "ZM · GPT-5.5", 400_000, 128_000, { input: 1.25, output: 10 }, true, [
        "text",
        "image",
    ]),
    m(
        "zenmux",
        "google/gemini-3.1-pro-preview",
        "ZM · Gemini 3.1 Pro",
        1_000_000,
        64_000,
        { input: 1.25, output: 10 },
        true,
        ["text", "image"],
    ),
    m("zenmux", "deepseek/deepseek-v3.2", "ZM · DeepSeek V3.2", 128_000, 64_000, { input: 0.28, output: 0.42 }, true),
];

// GitHub Copilot — proxied OpenAI/Anthropic models (subscription-billed)
const GITHUB_COPILOT: ModelInfo[] = [
    m("github-copilot", "gpt-5", "Copilot · GPT-5", 400_000, 128_000, { input: 0, output: 0 }, true, ["text", "image"]),
    m("github-copilot", "gpt-5-mini", "Copilot · GPT-5 Mini", 400_000, 128_000, { input: 0, output: 0 }, true, [
        "text",
        "image",
    ]),
    m(
        "github-copilot",
        "claude-opus-4-7",
        "Copilot · Claude Opus 4.7",
        200_000,
        64_000,
        { input: 0, output: 0 },
        true,
        ["text", "image"],
    ),
    m(
        "github-copilot",
        "claude-sonnet-4-6",
        "Copilot · Claude Sonnet 4.6",
        200_000,
        64_000,
        { input: 0, output: 0 },
        true,
        ["text", "image"],
    ),
    m(
        "github-copilot",
        "gemini-2.5-pro",
        "Copilot · Gemini 2.5 Pro",
        1_000_000,
        64_000,
        { input: 0, output: 0 },
        true,
        ["text", "image"],
    ),
];

// ChatGPT subscription via the Codex backend (subscription-billed, cost 0).
// Models are account-dependent; these are the current Codex-accessible ones
// (gpt-5.5 is the default for ChatGPT-authenticated sessions). The catalog is
// only a seed — pick whatever your subscription actually exposes.
// Note: ChatGPT-account auth rejects the API-key-only `*-codex` slugs ("model
// is not supported when using Codex with a ChatGPT account"), so we seed the
// base reasoning models. gpt-5.5 is the default for ChatGPT-authenticated
// sessions.
const OPENAI_CHATGPT: ModelInfo[] = [
    m("openai-chatgpt", "gpt-5.5", "ChatGPT · GPT-5.5", 1_000_000, 128_000, { input: 0, output: 0 }, true, [
        "text",
        "image",
    ]),
    m("openai-chatgpt", "gpt-5.4", "ChatGPT · GPT-5.4", 1_000_000, 128_000, { input: 0, output: 0 }, true, [
        "text",
        "image",
    ]),
];

export const FALLBACK_MODELS: ModelInfo[] = [
    ...XAI,
    ...ANTHROPIC,
    ...OPENAI,
    ...OPENAI_CHATGPT,
    ...GOOGLE,
    ...OPENROUTER,
    ...DEEPSEEK,
    ...MISTRAL,
    ...GLM,
    ...ZAI,
    ...GROQ,
    ...CEREBRAS,
    ...ZENMUX,
    ...GITHUB_COPILOT,
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
