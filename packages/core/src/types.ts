export type ProviderId = "xai" | "anthropic" | "openai" | "google" | "openrouter";

export const PROVIDER_IDS: ProviderId[] = ["xai", "anthropic", "openai", "google", "openrouter"];

export interface ModelInfo {
  id: string;
  provider: ProviderId;
  name: string;
  contextWindow: number;
  maxOutput: number;
  cost: { input: number; output: number; cacheRead: number; cacheWrite: number };
  reasoning: boolean;
  modalities: string[];
  available: boolean;
}

export interface UsageBlock {
  inputTokens?: number;
  outputTokens?: number;
  totalTokens?: number;
  cachedInputTokens?: number;
  reasoningTokens?: number;
  cost?: number;
}

export interface CostBreakdown {
  inputTokens: number;
  outputTokens: number;
  cachedInputTokens: number;
  usd: number;
}

export interface XaiOAuthCredentials {
  refresh: string;
  access: string;
  expires: number;
  tokenEndpoint?: string;
  discovery?: { authorization_endpoint: string; token_endpoint: string };
  idToken?: string;
  tokenType?: string;
  baseUrl?: string;
}

export type AuthEntry =
  | { mode: "apikey"; provider: ProviderId; apiKey: string }
  | { mode: "oauth"; provider: "xai"; xai: XaiOAuthCredentials };

export interface SessionInfoData {
  id: string;
  createdAt: number;
  cwd: string;
  provider: ProviderId;
  model: string;
}

export type Entry =
  | ({ type: "session-info"; ts: number } & SessionInfoData)
  | { type: "message"; role: "user" | "assistant" | "tool"; content: unknown; ts: number; usage?: UsageBlock }
  | { type: "model-change"; from: string; to: string; ts: number }
  | { type: "compact"; summary: string; cutAt: number; ts: number; tokensBefore: number; tokensAfter: number }
  | { type: "branch-summary"; summary: string; ts: number }
  | { type: "custom"; payload: unknown; ts: number };
