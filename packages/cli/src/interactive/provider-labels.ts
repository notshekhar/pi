export function providerLabel(id: string): string {
    switch (id) {
        case "xai":
            return "xAI (Grok) — OAuth subscription or API key";
        case "anthropic":
            return "Anthropic — API key";
        case "openai":
            return "OpenAI — ChatGPT subscription (OAuth) or API key";
        case "openai-chatgpt":
            return "ChatGPT (Codex) — OAuth subscription";
        case "google":
            return "Google — API key";
        case "openrouter":
            return "OpenRouter — API key";
        case "github-copilot":
            return "GitHub Copilot — OAuth (device flow)";
        case "deepseek":
            return "DeepSeek — API key";
        case "mistral":
            return "Mistral — API key";
        case "glm":
            return "Zhipu GLM (open.bigmodel.cn) — API key";
        case "zai":
            return "z.ai (GLM, international) — API key";
        case "groq":
            return "Groq — API key";
        case "cerebras":
            return "Cerebras — API key";
        case "zenmux":
            return "ZenMux (gateway, 200+ models) — API key";
        case "ollama":
            return "Ollama — local, no key (must be running)";
        default:
            return "";
    }
}
