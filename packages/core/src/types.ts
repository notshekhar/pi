export type BuiltinProviderId =
    | "xai"
    | "anthropic"
    | "openai"
    | "openai-chatgpt"
    | "google"
    | "openrouter"
    | "github-copilot"
    | "deepseek"
    | "mistral"
    | "glm"
    | "zai"
    | "groq"
    | "cerebras"
    | "ollama";
export type ProviderId = BuiltinProviderId | (string & {});

// Note: "openai-chatgpt" is intentionally NOT listed — it's not a standalone
// entry in the /login picker. The single "openai" entry asks API-key vs ChatGPT
// subscription, and the subscription path stores creds under "openai-chatgpt"
// (used for model routing + catalog), mirroring how xAI offers OAuth-or-key.
export const BUILTIN_PROVIDER_IDS: BuiltinProviderId[] = [
    "xai",
    "anthropic",
    "openai",
    "google",
    "openrouter",
    "github-copilot",
    "deepseek",
    "mistral",
    "glm",
    "zai",
    "groq",
    "cerebras",
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
    /** Path of the session file this one was forked from. */
    parentSession?: string;
}

/** One step of a subagent's run, in stream order. Structured (not a flat
 * string) so renderers can style text / reasoning / tool lines differently. */
export type SubagentActivityPart =
    | { type: "text"; text: string }
    | { type: "reasoning"; text: string }
    | { type: "tool"; name: string; summary: string };

/**
 * Tree structure: every entry is a node with an id and a parent
 * pointer. Optional on read (legacy flat sessions get ids assigned and a
 * linear chain on load); always set on write.
 */
export interface EntryTreeFields {
    id?: string;
    parentId?: string | null;
}

export type Entry = EntryTreeFields &
    (
        | ({ type: "session-info"; ts: number } & SessionInfoData)
        | {
              type: "message";
              role: "user" | "assistant" | "tool";
              content: unknown;
              ts: number;
              usage?: UsageBlock;
              /** Model that produced this message — pins cost to the right pricing
               * even after a mid-session model switch. */
              model?: string;
          }
        | {
              type: "subagent";
              ts: number;
              agent: string;
              prompt: string;
              result: string;
              /** Ordered run log (text/reasoning/tool parts, stream order). */
              activity?: SubagentActivityPart[];
              usage?: UsageBlock;
              /** Model that ran the subagent — pins its cost to the right pricing. */
              model?: string;
          }
        | { type: "model-change"; from: string; to: string; ts: number }
        | { type: "compact"; summary: string; cutAt: number; ts: number; tokensBefore: number; tokensAfter: number }
        | { type: "branch-summary"; summary: string; ts: number; fromId?: string }
        | { type: "label"; targetId: string; label?: string; ts: number }
        // User-set session display name:
        // latest wins, empty/absent name clears.
        | { type: "session-name"; name?: string; ts: number }
        | { type: "custom"; payload: unknown; ts: number }
    );
