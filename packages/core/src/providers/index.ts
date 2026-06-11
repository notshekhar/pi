import type { LanguageModel } from "ai";
import {
  getAccessToken,
  getApiKey,
  getCustomProvider,
  isCustomProvider,
  parseCustomProviderId,
  resolveAuthToken,
} from "../auth";
import { COPILOT_HEADERS, getCopilotBaseUrl } from "../auth/oauth/github-copilot";
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

/** Ollama host root (no /api suffix). Override with PI_OLLAMA_BASE_URL. */
export function ollamaBaseURL(): string {
  return (process.env.PI_OLLAMA_BASE_URL || "http://127.0.0.1:11434").replace(/\/+$/, "");
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
        models?: Array<{ name: string; displayName?: string; inputTokenLimit?: number; outputTokenLimit?: number }>;
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
        baseURL: process.env.PI_XAI_BASE_URL || "https://api.x.ai/v1",
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
    case "github-copilot": {
      const token = await resolveAuthToken("github-copilot");
      if (!token) throw new Error("No GitHub Copilot credentials. Run: /login github-copilot");
      const baseURL = getCopilotBaseUrl(token);
      const { createOpenAI } = await import("@ai-sdk/openai");
      return createOpenAI({ apiKey: "placeholder", baseURL, fetch: copilotAuthFetch() })(model);
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
