export type BuiltinProviderId = "xai" | "anthropic" | "openai" | "google" | "openrouter" | "github-copilot" | "ollama";
export type ProviderId = BuiltinProviderId | (string & {});

export const BUILTIN_PROVIDER_IDS: BuiltinProviderId[] = [
    "xai",
    "anthropic",
    "openai",
    "google",
    "openrouter",
    "github-copilot",
    "ollama",
];
export const PROVIDER_IDS = BUILTIN_PROVIDER_IDS;

export type CustomProviderSdk = "openai" | "anthropic" | "google" | "openai-compatible";

export interface CustomProviderConfig {
    name: string;
    sdk: CustomProviderSdk;
    baseURL: string;
    apiKey: string;
    headers?: Record<string, string>;
    /** Model IDs the user wants to expose, with optional name + pricing overrides */
    models?: Array<{
        id: string;
        name?: string;
        contextWindow?: number;
        maxOutput?: number;
        cost?: { input?: number; output?: number; cacheRead?: number; cacheWrite?: number };
    }>;
}

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
    // ai-sdk v6 detail block — needed to bill cache writes (1.25x on Anthropic)
    inputTokenDetails?: {
        noCacheTokens?: number;
        cacheReadTokens?: number;
        cacheWriteTokens?: number;
    };
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

export interface GenericOAuthCredentials {
    refresh: string;
    access: string;
    expires: number;
    enterpriseUrl?: string;
    [key: string]: unknown;
}

export type AuthEntry =
    | { mode: "apikey"; provider: ProviderId; apiKey: string }
    | { mode: "oauth"; provider: "xai"; xai: XaiOAuthCredentials }
    | { mode: "oauth"; provider: ProviderId; creds: GenericOAuthCredentials };

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
