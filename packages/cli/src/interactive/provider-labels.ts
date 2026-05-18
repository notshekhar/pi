export function providerLabel(id: string): string {
  switch (id) {
    case "xai":
      return "xAI (Grok) — OAuth subscription or API key";
    case "anthropic":
      return "Anthropic — API key";
    case "openai":
      return "OpenAI — API key";
    case "google":
      return "Google — API key";
    case "openrouter":
      return "OpenRouter — API key";
    case "github-copilot":
      return "GitHub Copilot — OAuth (device flow)";
    case "claude-agent":
      return "Claude Agent SDK — API key or Claude Pro/Max OAuth";
    case "cursor-agent":
      return "Cursor Agent SDK — API key (Pro pool billing)";
    default:
      return "";
  }
}
