package catalog

// seed is loop's curated fallback catalog (packages/core/src/catalog/fallbacks.ts).
var seed = []Model{
	// xAI
	m("xai", "composer-2.5", "Composer 2.5", 256_000, 30_000, Cost{Input: 0.9, Output: 1.8, CacheRead: 0.2}, false),
	m("xai", "grok-build-0.1", "Grok Build 0.1", 256_000, 256_000, Cost{Input: 1, Output: 2, CacheRead: 0.2}, true),
	m("xai", "grok-4.3", "Grok 4.3", 1_000_000, 30_000, Cost{Input: 1.25, Output: 2.5, CacheRead: 0.2}, true),
	m("xai", "grok-4.20-0309-reasoning", "Grok 4.20 Reasoning", 2_000_000, 30_000, Cost{Input: 2, Output: 6, CacheRead: 0.2}, true),
	m("xai", "grok-4.20-0309-non-reasoning", "Grok 4.20 Non-Reasoning", 2_000_000, 30_000, Cost{Input: 2, Output: 6, CacheRead: 0.2}, false),
	m("xai", "grok-4.20-multi-agent-0309", "Grok 4.20 Multi-Agent", 2_000_000, 30_000, Cost{Input: 2, Output: 6, CacheRead: 0.2}, true),
	m("xai", "grok-4-fast", "Grok 4 Fast", 2_000_000, 30_000, Cost{Input: 0.2, Output: 0.5, CacheRead: 0.05}, true),
	m("xai", "grok-4", "Grok 4", 256_000, 30_000, Cost{Input: 3, Output: 15, CacheRead: 0.75}, true),
	m("xai", "grok-code-fast-1", "Grok Code Fast 1", 256_000, 30_000, Cost{Input: 0.2, Output: 1.5, CacheRead: 0.02}, true),
	m("xai", "grok-3", "Grok 3", 131_072, 8_192, Cost{Input: 3, Output: 15}, false),

	// Anthropic
	m("anthropic", "claude-opus-4-8", "Claude Opus 4.8", 1_000_000, 128_000, Cost{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25}, true),
	m("anthropic", "claude-sonnet-5", "Claude Sonnet 5", 1_000_000, 128_000, Cost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}, true),
	m("anthropic", "claude-opus-4-7", "Claude Opus 4.7", 1_000_000, 128_000, Cost{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25}, true),
	m("anthropic", "claude-sonnet-4-6", "Claude Sonnet 4.6", 1_000_000, 128_000, Cost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}, true),
	m("anthropic", "claude-haiku-4-5", "Claude Haiku 4.5", 200_000, 64_000, Cost{Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25}, true),
	m("anthropic", "claude-opus-4-5", "Claude Opus 4.5", 1_000_000, 128_000, Cost{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25}, true),
	m("anthropic", "claude-sonnet-4-5", "Claude Sonnet 4.5", 1_000_000, 128_000, Cost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}, true),

	// OpenAI
	m("openai", "gpt-5", "GPT-5", 400_000, 128_000, Cost{Input: 1.25, Output: 10, CacheRead: 0.125}, true),
	m("openai", "gpt-5-mini", "GPT-5 Mini", 400_000, 128_000, Cost{Input: 0.25, Output: 2, CacheRead: 0.025}, true),
	m("openai", "gpt-5-nano", "GPT-5 Nano", 400_000, 128_000, Cost{Input: 0.05, Output: 0.4, CacheRead: 0.005}, true),
	m("openai", "o3", "o3", 200_000, 100_000, Cost{Input: 2, Output: 8, CacheRead: 0.5}, true),
	m("openai", "o3-mini", "o3 Mini", 200_000, 100_000, Cost{Input: 1.1, Output: 4.4, CacheRead: 0.55}, true),
	m("openai", "gpt-4.1", "GPT-4.1", 1_000_000, 32_000, Cost{Input: 2, Output: 8, CacheRead: 0.5}, false),
	m("openai", "gpt-4.1-mini", "GPT-4.1 Mini", 1_000_000, 32_000, Cost{Input: 0.4, Output: 1.6, CacheRead: 0.1}, false),

	// Google
	m("google", "gemini-2.5-pro", "Gemini 2.5 Pro", 2_000_000, 64_000, Cost{Input: 1.25, Output: 10, CacheRead: 0.31}, true),
	m("google", "gemini-2.5-flash", "Gemini 2.5 Flash", 1_000_000, 64_000, Cost{Input: 0.3, Output: 2.5, CacheRead: 0.075}, true),
	m("google", "gemini-2.5-flash-lite", "Gemini 2.5 Flash Lite", 1_000_000, 64_000, Cost{Input: 0.1, Output: 0.4}, false),

	// OpenRouter
	m("openrouter", "anthropic/claude-opus-4-7", "OR · Claude Opus 4.7", 1_000_000, 128_000, Cost{Input: 5, Output: 25}, true),
	m("openrouter", "anthropic/claude-sonnet-4-6", "OR · Claude Sonnet 4.6", 1_000_000, 128_000, Cost{Input: 3, Output: 15}, true),
	m("openrouter", "openai/gpt-5", "OR · GPT-5", 400_000, 128_000, Cost{Input: 1.25, Output: 10}, true),
	m("openrouter", "google/gemini-2.5-pro", "OR · Gemini 2.5 Pro", 2_000_000, 64_000, Cost{Input: 1.25, Output: 10}, true),
	m("openrouter", "x-ai/grok-4", "OR · Grok 4", 256_000, 30_000, Cost{Input: 3, Output: 15}, true),
	m("openrouter", "meta-llama/llama-3.3-70b-instruct", "OR · Llama 3.3 70B", 131_072, 16_000, Cost{Input: 0.13, Output: 0.4}, false),
	m("openrouter", "nex-agi/nex-n2-pro:free", "OR · Nex-N2-Pro (free)", 262_144, 262_144, Cost{}, true),

	// DeepSeek
	m("deepseek", "deepseek-chat", "DeepSeek Chat", 128_000, 8_192, Cost{Input: 0.28, Output: 0.42, CacheRead: 0.028}, false),
	m("deepseek", "deepseek-reasoner", "DeepSeek Reasoner", 128_000, 64_000, Cost{Input: 0.28, Output: 0.42, CacheRead: 0.028}, true),

	// Mistral
	m("mistral", "mistral-large-latest", "Mistral Large", 131_072, 8_192, Cost{Input: 2, Output: 6}, false),
	m("mistral", "mistral-small-latest", "Mistral Small", 131_072, 8_192, Cost{Input: 0.1, Output: 0.3}, false),
	m("mistral", "magistral-medium-latest", "Magistral Medium", 40_960, 40_960, Cost{Input: 2, Output: 5}, true),
	m("mistral", "codestral-latest", "Codestral", 256_000, 8_192, Cost{Input: 0.3, Output: 0.9}, false),

	// GLM / z.ai
	m("glm", "glm-5.2", "GLM-5.2", 200_000, 128_000, Cost{Input: 0.6, Output: 2.2}, true),
	m("glm", "glm-4.7", "GLM-4.7", 200_000, 128_000, Cost{Input: 0.6, Output: 2.2}, true),
	m("glm", "glm-4.6", "GLM-4.6", 200_000, 128_000, Cost{Input: 0.6, Output: 2.2}, true),
	m("glm", "glm-4.5-air", "GLM-4.5 Air", 128_000, 96_000, Cost{Input: 0.2, Output: 1.1}, true),
	m("zai", "glm-5.2", "GLM-5.2 (z.ai)", 200_000, 128_000, Cost{Input: 0.6, Output: 2.2}, true),
	m("zai", "glm-4.7", "GLM-4.7 (z.ai)", 200_000, 128_000, Cost{Input: 0.6, Output: 2.2}, true),
	m("zai", "glm-4.6", "GLM-4.6 (z.ai)", 200_000, 128_000, Cost{Input: 0.6, Output: 2.2}, true),
	m("zai", "glm-4.5-air", "GLM-4.5 Air (z.ai)", 128_000, 96_000, Cost{Input: 0.2, Output: 1.1}, true),

	// Kimi platform (pay-per-token). Subscription keys swap to kimiCode.
	m("kimi", "kimi-k3", "Kimi K3", 1_048_576, 32_768, Cost{Input: 3, Output: 15, CacheRead: 0.3}, true),
	m("kimi", "kimi-k2.7-code", "Kimi K2.7 Code", 262_144, 32_768, Cost{Input: 0.95, Output: 4, CacheRead: 0.19}, true),
	m("kimi", "kimi-k2.6", "Kimi K2.6", 262_144, 32_768, Cost{Input: 0.95, Output: 4, CacheRead: 0.16}, true),

	// Groq
	m("groq", "openai/gpt-oss-120b", "GPT-OSS 120B (Groq)", 131_072, 32_768, Cost{Input: 0.15, Output: 0.75}, true),
	m("groq", "openai/gpt-oss-20b", "GPT-OSS 20B (Groq)", 131_072, 32_768, Cost{Input: 0.1, Output: 0.5}, true),
	m("groq", "moonshotai/kimi-k2-instruct", "Kimi K2 (Groq)", 131_072, 16_384, Cost{Input: 1, Output: 3}, false),
	m("groq", "llama-3.3-70b-versatile", "Llama 3.3 70B (Groq)", 131_072, 32_768, Cost{Input: 0.59, Output: 0.79}, false),

	// Cerebras
	m("cerebras", "gpt-oss-120b", "GPT-OSS 120B (Cerebras)", 131_072, 32_768, Cost{Input: 0.25, Output: 0.69}, true),
	m("cerebras", "qwen-3-235b-a22b-instruct-2507", "Qwen3 235B (Cerebras)", 131_072, 32_768, Cost{Input: 0.6, Output: 1.2}, false),
	m("cerebras", "llama-3.3-70b", "Llama 3.3 70B (Cerebras)", 131_072, 32_768, Cost{Input: 0.85, Output: 1.2}, false),

	// ZenMux
	m("zenmux", "z-ai/glm-5.2", "ZM · GLM-5.2", 200_000, 128_000, Cost{Input: 0.6, Output: 2.2}, true),
	m("zenmux", "anthropic/claude-opus-4.8", "ZM · Claude Opus 4.8", 200_000, 64_000, Cost{Input: 5, Output: 25}, true),
	m("zenmux", "anthropic/claude-sonnet-4.6", "ZM · Claude Sonnet 4.6", 200_000, 64_000, Cost{Input: 3, Output: 15}, true),
	m("zenmux", "openai/gpt-5.5", "ZM · GPT-5.5", 400_000, 128_000, Cost{Input: 1.25, Output: 10}, true),
	m("zenmux", "google/gemini-3.1-pro-preview", "ZM · Gemini 3.1 Pro", 1_000_000, 64_000, Cost{Input: 1.25, Output: 10}, true),
	m("zenmux", "deepseek/deepseek-v3.2", "ZM · DeepSeek V3.2", 128_000, 64_000, Cost{Input: 0.28, Output: 0.42}, true),

	// Vercel AI Gateway
	m("vercel", "anthropic/claude-opus-4.8", "VG · Claude Opus 4.8", 1_000_000, 128_000, Cost{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25}, true),
	m("vercel", "anthropic/claude-sonnet-4.6", "VG · Claude Sonnet 4.6", 1_000_000, 128_000, Cost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}, true),
	m("vercel", "openai/gpt-5.5", "VG · GPT-5.5", 1_000_000, 128_000, Cost{Input: 5, Output: 30, CacheRead: 0.5}, true),
	m("vercel", "google/gemini-3.1-pro-preview", "VG · Gemini 3.1 Pro", 1_000_000, 64_000, Cost{Input: 2, Output: 12, CacheRead: 0.2}, true),
	m("vercel", "zai/glm-5.2", "VG · GLM-5.2", 1_040_000, 128_000, Cost{Input: 1.4, Output: 4.4, CacheRead: 0.26}, true),
	m("vercel", "moonshotai/kimi-k3", "VG · Kimi K3", 1_000_000, 131_072, Cost{Input: 3, Output: 15, CacheRead: 0.3}, true),

	// Ollama — local pulls; these are the common coding defaults.
	m("ollama", "llama3.2", "Llama 3.2", 128_000, 8_192, Cost{}, false),
	m("ollama", "qwen2.5-coder", "Qwen2.5 Coder", 128_000, 8_192, Cost{}, false),

	// Bedrock — inference-profile ids; the live list is account-specific.
	m("bedrock", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", "Bedrock · Claude Sonnet 4.5", 1_000_000, 128_000, Cost{Input: 3, Output: 15, CacheRead: 0.3}, true),
	m("bedrock", "us.anthropic.claude-sonnet-4-6", "Bedrock · Claude Sonnet 4.6", 1_000_000, 128_000, Cost{Input: 3, Output: 15, CacheRead: 0.3}, true),
	m("bedrock", "us.anthropic.claude-opus-4-6", "Bedrock · Claude Opus 4.6", 1_000_000, 128_000, Cost{Input: 5, Output: 25, CacheRead: 0.5}, true),
}

// kimiCode is the Kimi Code subscription catalog (sk-kimi-… keys).
var kimiCode = []Model{
	m("kimi", "k3", "Kimi K3 (Code plan)", 1_048_576, 32_768, Cost{}, true),
	m("kimi", "kimi-for-coding", "Kimi K2.7 Coding", 262_144, 32_768, Cost{}, true),
	m("kimi", "kimi-for-coding-highspeed", "Kimi K2.7 Coding Highspeed", 262_144, 32_768, Cost{}, true),
}
