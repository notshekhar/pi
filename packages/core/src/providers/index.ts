import type { LanguageModel } from "ai";
import {
    getAccessToken,
    getApiKey,
    getCustomProvider,
    isCustomProvider,
    parseCustomProviderId,
    resolveAuthToken,
    resolveOAuthCreds,
} from "../auth";
import { COPILOT_HEADERS, getCopilotBaseUrl } from "../auth/oauth/github-copilot";
import { accountIdFromIdToken, CODEX_BASE_URL, OPENAI_CHATGPT_HEADERS } from "../auth/oauth/openai-chatgpt";
import type { CustomProviderConfig, ProviderId } from "../types";

type FetchInput = Parameters<typeof fetch>[0];
type FetchInit = Parameters<typeof fetch>[1];

function withPreconnect(fn: (input: FetchInput, init?: FetchInit) => Promise<Response>): typeof fetch {
    const wrapped = fn as typeof fetch & { preconnect: typeof fetch.preconnect };
    wrapped.preconnect = fetch.preconnect.bind(fetch);
    return wrapped;
}

function copilotAuthFetch(): typeof fetch {
    return withPreconnect(async (input, init) => {
        const token = await resolveAuthToken("github-copilot");
        if (!token) throw new Error("No GitHub Copilot credentials. Run: /login github-copilot");
        const headers = new Headers(init?.headers);
        headers.set("authorization", `Bearer ${token}`);
        for (const [k, v] of Object.entries(COPILOT_HEADERS)) headers.set(k, v);
        headers.set("X-Initiator", "user");
        headers.set("Openai-Intent", "conversation-edits");
        return fetch(input, { ...(init as RequestInit), headers });
    });
}

/**
 * Auth + request shaping for the ChatGPT/Codex backend. Beyond the bearer
 * token it needs the `chatgpt-account-id` header, and the backend only accepts
 * stateless Responses calls — so we force `store:false` and ask for encrypted
 * reasoning so multi-step turns keep their reasoning context. The system prompt
 * (loop's instructions) is left intact; set LOOP_CODEX_INSTRUCTIONS to override it
 * if the backend rejects a request for non-Codex instructions.
 */
/** Responses API message content is a string or an array of text parts. */
function messageContentToText(content: unknown): string {
    if (typeof content === "string") return content;
    if (Array.isArray(content)) {
        return content
            .map((p) => (p && typeof p === "object" && "text" in p ? String((p as { text: unknown }).text) : ""))
            .join("");
    }
    return "";
}

function openaiChatgptAuthFetch(): typeof fetch {
    const overrideInstructions = process.env.LOOP_CODEX_INSTRUCTIONS;
    return withPreconnect(async (input, init) => {
        const creds = await resolveOAuthCreds("openai-chatgpt");
        if (!creds) throw new Error("No ChatGPT credentials. Run: /login openai-chatgpt");
        const accountId = (creds.accountId as string | undefined) ?? accountIdFromIdToken(creds.idToken as string);

        const headers = new Headers(init?.headers);
        headers.delete("x-api-key");
        headers.set("authorization", `Bearer ${creds.access}`);
        if (accountId) headers.set("chatgpt-account-id", accountId);
        for (const [k, v] of Object.entries(OPENAI_CHATGPT_HEADERS)) headers.set(k, v);

        let body = init?.body;
        if (typeof body === "string") {
            try {
                const json = JSON.parse(body) as Record<string, unknown>;
                json.store = false; // backend only accepts stateless calls
                const include = new Set<string>(Array.isArray(json.include) ? (json.include as string[]) : []);
                include.add("reasoning.encrypted_content"); // required when store:false
                json.include = [...include];
                // The Codex backend rejects requests without a top-level
                // `instructions` ("Instructions are required"). The SDK encodes
                // loop's system prompt as a developer message in `input`, so lift
                // that out into `instructions` (removing the duplicate) unless an
                // override is supplied.
                if (!json.instructions) {
                    if (overrideInstructions) {
                        json.instructions = overrideInstructions;
                    } else if (Array.isArray(json.input)) {
                        const input = json.input as Array<{ role?: string; content?: unknown }>;
                        const idx = input.findIndex((m) => m?.role === "developer" || m?.role === "system");
                        if (idx >= 0) {
                            json.instructions = messageContentToText(input[idx].content);
                            input.splice(idx, 1);
                        }
                    }
                    if (!json.instructions) json.instructions = "You are a helpful coding assistant.";
                }
                body = JSON.stringify(json);
            } catch {
                // not JSON (shouldn't happen for the Responses API) — leave as-is
            }
        }
        return fetch(input, { ...(init as RequestInit), headers, body });
    });
}

/** Ollama host root (no /api suffix). Override with LOOP_OLLAMA_BASE_URL. */
export function ollamaBaseURL(): string {
    return (process.env.LOOP_OLLAMA_BASE_URL || "http://127.0.0.1:11434").replace(/\/+$/, "");
}

export interface OllamaModelTag {
    name: string;
    details?: { family?: string; parameter_size?: string };
}

/**
 * Lists models installed on the local Ollama daemon via GET /api/tags.
 * Returns null when the daemon is unreachable (not running), [] when running
 * with no models pulled.
 */
export async function listOllamaModels(): Promise<OllamaModelTag[] | null> {
    try {
        const res = await fetch(`${ollamaBaseURL()}/api/tags`, {
            signal: AbortSignal.timeout(3_000),
        });
        if (!res.ok) return null;
        const body = (await res.json()) as { models?: OllamaModelTag[] };
        return body.models ?? [];
    } catch {
        return null;
    }
}

export interface OllamaModelDetail {
    capabilities: string[];
    contextLength?: number;
}

/**
 * Inspects one installed model via POST /api/show. Returns its capability list
 * ("thinking", "vision", "tools", …) and trained context length. Null on error.
 */
export async function showOllamaModel(name: string): Promise<OllamaModelDetail | null> {
    try {
        const res = await fetch(`${ollamaBaseURL()}/api/show`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ model: name }),
            signal: AbortSignal.timeout(5_000),
        });
        if (!res.ok) return null;
        const body = (await res.json()) as {
            capabilities?: string[];
            model_info?: Record<string, unknown>;
        };
        const info = body.model_info ?? {};
        const ctxKey = Object.keys(info).find((k) => k.endsWith(".context_length"));
        const ctx = ctxKey ? Number(info[ctxKey]) : undefined;
        return {
            capabilities: body.capabilities ?? [],
            contextLength: Number.isFinite(ctx) ? ctx : undefined,
        };
    } catch {
        return null;
    }
}

export function parseModelId(full: string): { provider: ProviderId; model: string } {
    // custom provider ids look like "custom:bifrost/claude-opus-4-7"
    if (full.startsWith("custom:")) {
        const slash = full.indexOf("/", "custom:".length);
        if (slash < 0) throw new Error(`custom model id must be "custom:<name>/model": ${full}`);
        return { provider: full.slice(0, slash), model: full.slice(slash + 1) };
    }
    const idx = full.indexOf("/");
    if (idx < 0) throw new Error(`model id must be "provider/model": ${full}`);
    return { provider: full.slice(0, idx) as ProviderId, model: full.slice(idx + 1) };
}

function xaiAuthFetch(): typeof fetch {
    return withPreconnect(async (input, init) => {
        let token: string;
        try {
            token = await getAccessToken("xai");
        } catch {
            const key = getApiKey("xai");
            if (!key) throw new Error("No xAI credentials. Run: piagent login xai");
            token = key;
        }
        const headers = new Headers(init?.headers);
        headers.set("authorization", `Bearer ${token}`);
        const res = await fetch(input, { ...(init as RequestInit), headers });
        if (res.status === 401) {
            try {
                const fresh = await getAccessToken("xai", { forceRefresh: true });
                headers.set("authorization", `Bearer ${fresh}`);
                return fetch(input, { ...(init as RequestInit), headers });
            } catch {
                return res;
            }
        }
        return res;
    });
}

function customFetch(extraHeaders?: Record<string, string>): typeof fetch | undefined {
    if (!extraHeaders || Object.keys(extraHeaders).length === 0) return undefined;
    return withPreconnect(async (input, init) => {
        const headers = new Headers(init?.headers);
        for (const [k, v] of Object.entries(extraHeaders)) headers.set(k, v);
        return fetch(input, { ...(init as RequestInit), headers });
    });
}

function normalizeBaseURL(sdk: CustomProviderConfig["sdk"], baseURL: string): string {
    const trimmed = baseURL.replace(/\/+$/, "");
    // ai-sdk providers expect baseURL to already include the API version segment.
    // Auto-append the conventional path when missing so users don't have to know.
    const hasVersion = /\/v\d+(\b|\/)/.test(trimmed);
    if (hasVersion) return trimmed;
    switch (sdk) {
        case "anthropic":
            return `${trimmed}/v1`;
        case "openai":
        case "openai-compatible":
            return `${trimmed}/v1`;
        case "google":
            return `${trimmed}/v1beta`;
        default:
            return trimmed;
    }
}

// Provider SDKs are dynamic-imported so plain CLI commands (--version, sessions,
// login) never pay their module-eval cost — only the first model call does.
async function customModel(cfg: CustomProviderConfig, model: string): Promise<LanguageModel> {
    const fetchOverride = customFetch(cfg.headers);
    const baseURL = normalizeBaseURL(cfg.sdk, cfg.baseURL);
    switch (cfg.sdk) {
        case "openai":
        case "openai-compatible": {
            const { createOpenAI } = await import("@ai-sdk/openai");
            return createOpenAI({ apiKey: cfg.apiKey, baseURL, fetch: fetchOverride })(model);
        }
        case "anthropic": {
            const { createAnthropic } = await import("@ai-sdk/anthropic");
            return createAnthropic({ apiKey: cfg.apiKey, baseURL, fetch: fetchOverride })(model);
        }
        case "google": {
            const { createGoogleGenerativeAI } = await import("@ai-sdk/google");
            return createGoogleGenerativeAI({ apiKey: cfg.apiKey, baseURL, fetch: fetchOverride })(model);
        }
        default:
            throw new Error(`Unknown custom SDK: ${cfg.sdk}`);
    }
}

export interface DiscoveredModel {
    id: string;
    name?: string;
    contextWindow?: number;
    maxOutput?: number;
}

/**
 * Model discovery for custom providers (gateways like bifrost, litellm, or any
 * compatible endpoint). Hits the sdk-appropriate models listing with the
 * provider's auth + custom headers. Returns null when the endpoint doesn't
 * support listing (404/timeout/parse error) — caller falls back to asking the
 * user for model ids.
 */
export async function fetchCustomProviderModels(
    cfg: Pick<CustomProviderConfig, "sdk" | "baseURL" | "apiKey" | "headers">,
): Promise<DiscoveredModel[] | null> {
    const base = normalizeBaseURL(cfg.sdk, cfg.baseURL);
    const headers: Record<string, string> = { ...(cfg.headers ?? {}) };
    try {
        if (cfg.sdk === "anthropic") {
            headers["anthropic-version"] ??= "2023-06-01";
            if (cfg.apiKey) headers["x-api-key"] ??= cfg.apiKey;
            const res = await fetch(`${base}/models`, { headers, signal: AbortSignal.timeout(10_000) });
            if (!res.ok) return null;
            const body = (await res.json()) as {
                data?: Array<{ id: string; display_name?: string; max_input_tokens?: number; max_tokens?: number }>;
            };
            if (!body.data?.length) return null;
            return body.data.map((m) => ({
                id: m.id,
                name: m.display_name,
                contextWindow: m.max_input_tokens,
                maxOutput: m.max_tokens,
            }));
        }
        if (cfg.sdk === "google") {
            const res = await fetch(`${base}/models?key=${encodeURIComponent(cfg.apiKey)}`, {
                headers,
                signal: AbortSignal.timeout(10_000),
            });
            if (!res.ok) return null;
            const body = (await res.json()) as {
                models?: Array<{
                    name: string;
                    displayName?: string;
                    inputTokenLimit?: number;
                    outputTokenLimit?: number;
                }>;
            };
            if (!body.models?.length) return null;
            return body.models.map((m) => ({
                id: m.name.replace(/^models\//, ""),
                name: m.displayName,
                contextWindow: m.inputTokenLimit,
                maxOutput: m.outputTokenLimit,
            }));
        }
        // openai + openai-compatible
        if (cfg.apiKey) headers.Authorization ??= `Bearer ${cfg.apiKey}`;
        const res = await fetch(`${base}/models`, { headers, signal: AbortSignal.timeout(10_000) });
        if (!res.ok) return null;
        const body = (await res.json()) as { data?: Array<{ id: string }> };
        if (!body.data?.length) return null;
        return body.data.map((m) => ({ id: m.id }));
    } catch {
        return null;
    }
}

export async function getModel(fullId: string): Promise<LanguageModel> {
    const { provider, model } = parseModelId(fullId);
    if (isCustomProvider(provider)) {
        const name = parseCustomProviderId(provider)!;
        const cfg = getCustomProvider(name);
        if (!cfg) throw new Error(`Custom provider not configured: ${name}`);
        return customModel(cfg, model);
    }
    switch (provider) {
        case "xai": {
            const { createXai } = await import("@ai-sdk/xai");
            const xai = createXai({
                apiKey: getApiKey("xai") ?? "placeholder",
                baseURL: process.env.LOOP_XAI_BASE_URL || "https://api.x.ai/v1",
                fetch: xaiAuthFetch(),
            });
            return xai(model);
        }
        case "anthropic": {
            const key = getApiKey("anthropic");
            if (!key) throw new Error("No Anthropic API key. Run: piagent login anthropic");
            const { createAnthropic } = await import("@ai-sdk/anthropic");
            return createAnthropic({ apiKey: key })(model);
        }
        case "openai": {
            const key = getApiKey("openai");
            if (!key) throw new Error("No OpenAI API key. Run: piagent login openai");
            const { createOpenAI } = await import("@ai-sdk/openai");
            return createOpenAI({ apiKey: key })(model);
        }
        case "google": {
            const key = getApiKey("google");
            if (!key) throw new Error("No Google API key. Run: piagent login google");
            const { createGoogleGenerativeAI } = await import("@ai-sdk/google");
            return createGoogleGenerativeAI({ apiKey: key })(model);
        }
        case "openrouter": {
            const key = getApiKey("openrouter");
            if (!key) throw new Error("No OpenRouter API key. Run: piagent login openrouter");
            const { createOpenRouter } = await import("@openrouter/ai-sdk-provider");
            return createOpenRouter({ apiKey: key })(model);
        }
        case "deepseek": {
            const key = getApiKey("deepseek");
            if (!key) throw new Error("No DeepSeek API key. Run: piagent login deepseek");
            const { createDeepSeek } = await import("@ai-sdk/deepseek");
            return createDeepSeek({ apiKey: key })(model);
        }
        case "mistral": {
            const key = getApiKey("mistral");
            if (!key) throw new Error("No Mistral API key. Run: piagent login mistral");
            const { createMistral } = await import("@ai-sdk/mistral");
            return createMistral({ apiKey: key })(model);
        }
        case "glm": {
            // Zhipu GLM models via the BigModel endpoint (open.bigmodel.cn).
            const key = getApiKey("glm");
            if (!key) throw new Error("No GLM (Zhipu) API key. Run: piagent login glm");
            const { createZhipu } = await import("zhipu-ai-provider");
            return createZhipu({ apiKey: key })(model);
        }
        case "zai": {
            // Same Zhipu GLM models via the international z.ai endpoint.
            const key = getApiKey("zai");
            if (!key) throw new Error("No z.ai API key. Run: piagent login zai");
            const { createZhipu } = await import("zhipu-ai-provider");
            return createZhipu({ apiKey: key, baseURL: "https://api.z.ai/api/paas/v4" })(model);
        }
        case "groq": {
            const key = getApiKey("groq");
            if (!key) throw new Error("No Groq API key. Run: piagent login groq");
            const { createGroq } = await import("@ai-sdk/groq");
            return createGroq({ apiKey: key })(model);
        }
        case "zenmux": {
            // OpenAI-compatible gateway (https://zenmux.ai/api/v1), author/model ids.
            const key = getApiKey("zenmux");
            if (!key) throw new Error("No ZenMux API key. Run: piagent login zenmux");
            const { createOpenAI } = await import("@ai-sdk/openai");
            // .chat() forces /chat/completions — the gateway is chat-only, the
            // SDK's default model call would hit /responses and 403.
            return createOpenAI({ apiKey: key, baseURL: "https://zenmux.ai/api/v1" })(model);
        }
        case "cerebras": {
            const key = getApiKey("cerebras");
            if (!key) throw new Error("No Cerebras API key. Run: piagent login cerebras");
            const { createCerebras } = await import("@ai-sdk/cerebras");
            return createCerebras({ apiKey: key })(model);
        }
        case "github-copilot": {
            const token = await resolveAuthToken("github-copilot");
            if (!token) throw new Error("No GitHub Copilot credentials. Run: /login github-copilot");
            const baseURL = getCopilotBaseUrl(token);
            const { createOpenAI } = await import("@ai-sdk/openai");
            return createOpenAI({ apiKey: "placeholder", baseURL, fetch: copilotAuthFetch() })(model);
        }
        case "openai-chatgpt": {
            // ChatGPT subscription via the Codex backend. It speaks the Responses
            // API (POST <baseURL>/responses), so use the SDK's .responses() model.
            const creds = await resolveOAuthCreds("openai-chatgpt");
            if (!creds) throw new Error("No ChatGPT credentials. Run: /login openai-chatgpt");
            const { createOpenAI } = await import("@ai-sdk/openai");
            return createOpenAI({
                apiKey: "placeholder",
                baseURL: CODEX_BASE_URL,
                fetch: openaiChatgptAuthFetch(),
            }).responses(model);
        }
        case "ollama": {
            // Local daemon — no auth. createOllama wants the /api root.
            const { createOllama } = await import("ollama-ai-provider-v2");
            return createOllama({ baseURL: `${ollamaBaseURL()}/api` })(model);
        }
        default:
            throw new Error(`Unknown provider: ${provider}`);
    }
}
