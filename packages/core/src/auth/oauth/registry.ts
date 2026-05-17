import { anthropicOAuthProvider } from "./anthropic";
import { githubCopilotOAuthProvider } from "./github-copilot";
import type { OAuthProviderInterface } from "./types";

const REGISTRY: Record<string, OAuthProviderInterface> = {
  [anthropicOAuthProvider.id]: anthropicOAuthProvider,
  [githubCopilotOAuthProvider.id]: githubCopilotOAuthProvider,
  // claude-agent reuses the anthropic OAuth provider (same creds, different consumer)
  "claude-agent": anthropicOAuthProvider,
};

export function getOAuthProvider(id: string): OAuthProviderInterface | undefined {
  return REGISTRY[id];
}

export function listOAuthProviders(): OAuthProviderInterface[] {
  return Object.values(REGISTRY);
}

export { anthropicOAuthProvider, githubCopilotOAuthProvider };
