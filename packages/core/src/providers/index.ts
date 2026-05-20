import { createAnthropic } from "@ai-sdk/anthropic";
import { createGoogleGenerativeAI } from "@ai-sdk/google";
import { createOpenAI } from "@ai-sdk/openai";
import { createXai } from "@ai-sdk/xai";
import { createOpenRouter } from "@openrouter/ai-sdk-provider";
import type { LanguageModel } from "ai";
import { createCursor } from "./cursor";
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

// Bun's globals.d.ts declares `typeof fetch` with a required `preconnect`
// method (Node 22+ runtime API). Our auth wrappers only implement the call
// signature; ai-sdk never invokes preconnect on the override. Wrap returns
// in this helper so TS sees a satisfying shape.
type FetchLike = typeof fetch;
function asFetchLike(
  fn: (input: Parameters<typeof fetch>[0], init?: Parameters<typeof fetch>[1]) => Promise<Response>,
): FetchLike {
  return Object.assign(fn, { preconnect: () => {} }) as FetchLike;
}

function copilotAuthFetch(): FetchLike {
  return asFetchLike(async (input, init) => {
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

function xaiAuthFetch(): FetchLike {
  return asFetchLike(async (input, init) => {
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

function customFetch(extraHeaders?: Record<string, string>): FetchLike | undefined {
  if (!extraHeaders || Object.keys(extraHeaders).length === 0) return undefined;
  return asFetchLike(async (input, init) => {
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

function customModel(cfg: CustomProviderConfig, model: string): LanguageModel {
  const fetchOverride = customFetch(cfg.headers);
  const baseURL = normalizeBaseURL(cfg.sdk, cfg.baseURL);
  switch (cfg.sdk) {
    case "openai":
    case "openai-compatible":
      return createOpenAI({ apiKey: cfg.apiKey, baseURL, fetch: fetchOverride })(model);
    case "anthropic":
      return createAnthropic({ apiKey: cfg.apiKey, baseURL, fetch: fetchOverride })(model);
    case "google":
      return createGoogleGenerativeAI({ apiKey: cfg.apiKey, baseURL, fetch: fetchOverride })(model);
    default:
      throw new Error(`Unknown custom SDK: ${cfg.sdk}`);
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
      return createAnthropic({ apiKey: key })(model);
    }
    case "openai": {
      const key = getApiKey("openai");
      if (!key) throw new Error("No OpenAI API key. Run: piagent login openai");
      return createOpenAI({ apiKey: key })(model);
    }
    case "google": {
      const key = getApiKey("google");
      if (!key) throw new Error("No Google API key. Run: piagent login google");
      return createGoogleGenerativeAI({ apiKey: key })(model);
    }
    case "openrouter": {
      const key = getApiKey("openrouter");
      if (!key) throw new Error("No OpenRouter API key. Run: piagent login openrouter");
      return createOpenRouter({ apiKey: key })(model);
    }
    case "github-copilot": {
      const token = await resolveAuthToken("github-copilot");
      if (!token) throw new Error("No GitHub Copilot credentials. Run: /login github-copilot");
      const baseURL = getCopilotBaseUrl(token);
      return createOpenAI({ apiKey: "placeholder", baseURL, fetch: copilotAuthFetch() })(model);
    }
    case "cursor": {
      const key = getApiKey("cursor") ?? process.env.CURSOR_API_KEY;
      if (!key) throw new Error("No Cursor API key. Run: pi login cursor");
      return createCursor({ apiKey: key })(model);
    }
    default:
      throw new Error(`Unknown provider: ${provider}`);
  }
}
