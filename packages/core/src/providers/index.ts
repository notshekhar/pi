import { createAnthropic } from "@ai-sdk/anthropic";
import { createGoogleGenerativeAI } from "@ai-sdk/google";
import { createOpenAI } from "@ai-sdk/openai";
import { createXai } from "@ai-sdk/xai";
import { createOpenRouter } from "@openrouter/ai-sdk-provider";
import type { LanguageModel } from "ai";
import { getAccessToken, getApiKey } from "../auth";
import type { ProviderId } from "../types";

export function parseModelId(full: string): { provider: ProviderId; model: string } {
  const idx = full.indexOf("/");
  if (idx < 0) throw new Error(`model id must be "provider/model": ${full}`);
  const provider = full.slice(0, idx) as ProviderId;
  const model = full.slice(idx + 1);
  return { provider, model };
}

function xaiAuthFetch(): typeof fetch {
  return async (input, init) => {
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
  };
}

export async function getModel(fullId: string): Promise<LanguageModel> {
  const { provider, model } = parseModelId(fullId);
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
    default:
      throw new Error(`Unknown provider: ${provider}`);
  }
}
