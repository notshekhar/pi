import Configstore from "configstore";
import { join } from "node:path";
import { existsSync, readFileSync } from "node:fs";
import { GENERATED_MODELS } from "./generated/models";
import { FALLBACK_MODELS, XAI_FALLBACK_MODELS, fallbackModelsForSdk } from "./fallbacks";
import { getPiDir } from "../auth/storage";
import { getApiKey, getAccessToken, listAuthorizedProviders, listCustomProviders } from "../auth";
import { listOllamaModels, showOllamaModel } from "../providers";
import type { ModelInfo, ProviderId } from "../types";

const cacheStore = new Configstore(
  "pi-agent-catalog",
  { availability: {}, ts: 0 },
  { configPath: join(getPiDir(), "catalog.json") },
);

const TTL_MS = 60 * 60 * 1000; // 1h

let mergedCache: Record<string, ModelInfo> | null = null;

export function bustCatalogCache(): void {
  mergedCache = null;
}

async function fetchAvailability(provider: ProviderId): Promise<Set<string> | null> {
  try {
    if (provider === "xai") {
      const token = (await getAccessToken("xai").catch(() => null)) ?? getApiKey("xai");
      if (!token) return null;
      const res = await fetch("https://api.x.ai/v1/models", {
        headers: { Authorization: `Bearer ${token}` },
        signal: AbortSignal.timeout(10_000),
      });
      if (!res.ok) return null;
      const body = (await res.json()) as { data?: { id: string }[] };
      return new Set((body.data ?? []).map((m) => `xai/${m.id}`));
    }
    if (provider === "openai") {
      const key = getApiKey("openai");
      if (!key) return null;
      const res = await fetch("https://api.openai.com/v1/models", {
        headers: { Authorization: `Bearer ${key}` },
        signal: AbortSignal.timeout(10_000),
      });
      if (!res.ok) return null;
      const body = (await res.json()) as { data?: { id: string }[] };
      return new Set((body.data ?? []).map((m) => `openai/${m.id}`));
    }
    if (provider === "anthropic") {
      const key = getApiKey("anthropic");
      if (!key) return null;
      const res = await fetch("https://api.anthropic.com/v1/models", {
        headers: { "x-api-key": key, "anthropic-version": "2023-06-01" },
        signal: AbortSignal.timeout(10_000),
      });
      if (!res.ok) return null;
      const body = (await res.json()) as { data?: { id: string }[] };
      return new Set((body.data ?? []).map((m) => `anthropic/${m.id}`));
    }
    if (provider === "openrouter") {
      const res = await fetch("https://openrouter.ai/api/v1/models", {
        signal: AbortSignal.timeout(10_000),
      });
      if (!res.ok) return null;
      const body = (await res.json()) as { data?: { id: string }[] };
      return new Set((body.data ?? []).map((m) => `openrouter/${m.id}`));
    }
    return null;
  } catch {
    return null;
  }
}

async function fetchOllamaCatalog(): Promise<ModelInfo[]> {
  const models = await listOllamaModels();
  if (!models) return [];
  // Query each model's real capabilities (thinking/vision) + context length
  // via /api/show, rather than guessing from the name. Runs in parallel.
  const details = await Promise.all(models.map((m) => showOllamaModel(m.name)));
  return models.map((m, i) => {
    const caps = details[i]?.capabilities ?? [];
    return {
      id: `ollama/${m.name}`,
      provider: "ollama" as ProviderId,
      name: m.name,
      contextWindow: details[i]?.contextLength ?? 8192,
      maxOutput: 4096,
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      reasoning: caps.includes("thinking"),
      modalities: caps.includes("vision") ? ["text", "image"] : ["text"],
      available: true,
    };
  });
}

function readUserOverrides(): Record<string, Partial<ModelInfo>> {
  const path = join(getPiDir(), "models.json");
  if (!existsSync(path)) return {};
  try {
    return JSON.parse(readFileSync(path, "utf8")) as Record<string, Partial<ModelInfo>>;
  } catch {
    return {};
  }
}

let refreshInFlight: Promise<Record<string, string[]>> | null = null;

async function refreshAvailability(): Promise<Record<string, string[]>> {
  if (refreshInFlight) return refreshInFlight;
  refreshInFlight = (async () => {
    const providers: ProviderId[] = ["xai", "anthropic", "openai", "openrouter"];
    const results = await Promise.all(providers.map((p) => fetchAvailability(p)));
    const availability: Record<string, string[]> = {};
    for (let i = 0; i < providers.length; i++) {
      const set = results[i];
      if (set) availability[providers[i]] = [...set];
    }
    cacheStore.set("availability", availability);
    cacheStore.set("ts", Date.now());
    return availability;
  })();
  try {
    return await refreshInFlight;
  } finally {
    refreshInFlight = null;
  }
}

export async function getCatalog(opts: { refresh?: boolean } = {}): Promise<Record<string, ModelInfo>> {
  if (mergedCache && !opts.refresh) return mergedCache;

  const ts = cacheStore.get("ts") as number;
  const stored = (cacheStore.get("availability") as Record<string, string[]>) ?? {};
  let availability: Record<string, string[]> = stored;

  if (opts.refresh) {
    availability = await refreshAvailability();
  } else if (Date.now() - ts > TTL_MS) {
    // Stale-while-revalidate: serve the stored availability immediately and
    // refresh in the background. Blocking here stalled the first prompt of a
    // session by up to 10s (4 provider /models fetches behind one await).
    void refreshAvailability()
      .then(() => {
        mergedCache = null; // next getCatalog() rebuilds with fresh availability
      })
      .catch(() => {});
  }

  const out: Record<string, ModelInfo> = {};
  // curated fallbacks win id collisions over generated catalog
  for (const m of FALLBACK_MODELS) {
    out[m.id] = { ...m };
  }
  for (const [id, m] of Object.entries(GENERATED_MODELS)) {
    if (out[id]) continue;
    const provAvail = availability[m.provider];
    const available = provAvail ? provAvail.includes(id) : true;
    out[id] = { ...m, available };
  }
  // Gate non-public providers by auth presence.
  const authed = new Set(listAuthorizedProviders());
  const hasCopilot = authed.has("github-copilot");
  for (const m of Object.values(out)) {
    if (m.provider === "github-copilot") m.available = hasCopilot;
  }

  // Note: we do NOT downmark fallback models based on /v1/models availability.
  // Subscription-only / preview models (e.g. grok-build, grok-4.20-*) are not
  // returned by xai's public /v1/models endpoint but ARE callable with an
  // OAuth bearer token. So fallback entries stay available unconditionally.
  void XAI_FALLBACK_MODELS;

  // custom providers' declared models — falls back to sdk defaults if empty
  for (const cfg of listCustomProviders()) {
    const provId = `custom:${cfg.name}`;
    const userModels = cfg.models ?? [];
    let entries = userModels;
    if (entries.length === 0) {
      entries = fallbackModelsForSdk(cfg.sdk).map((m) => {
        const localId = m.id.split("/").slice(1).join("/");
        return {
          id: localId,
          name: m.name,
          contextWindow: m.contextWindow,
          maxOutput: m.maxOutput,
          cost: m.cost,
        };
      });
    }
    for (const m of entries) {
      const fullId = `${provId}/${m.id}`;
      out[fullId] = {
        id: fullId,
        provider: provId,
        name: m.name ?? m.id,
        contextWindow: m.contextWindow ?? 200_000,
        maxOutput: m.maxOutput ?? 16_000,
        cost: {
          input: m.cost?.input ?? 0,
          output: m.cost?.output ?? 0,
          cacheRead: m.cost?.cacheRead ?? 0,
          cacheWrite: m.cost?.cacheWrite ?? 0,
        },
        reasoning: false,
        modalities: ["text"],
        available: true,
      };
    }
  }

  // Ollama: dynamic, machine-local. Only the models actually installed are
  // listed (GET /api/tags). Gated on auth so we don't hit localhost otherwise.
  if (authed.has("ollama")) {
    const tags = await fetchOllamaCatalog();
    for (const m of tags) out[m.id] = m;
  }

  const overrides = readUserOverrides();
  for (const [id, patch] of Object.entries(overrides)) {
    out[id] = { ...(out[id] ?? ({} as ModelInfo)), ...patch, id } as ModelInfo;
  }

  mergedCache = out;
  return out;
}

export async function getModel(id: string): Promise<ModelInfo | undefined> {
  const cat = await getCatalog();
  return cat[id];
}

let FALLBACK_BY_ID: Record<string, ModelInfo> | null = null;
export function getModelSync(id: string): ModelInfo | undefined {
  if (mergedCache?.[id]) return mergedCache[id];
  if (GENERATED_MODELS[id]) return GENERATED_MODELS[id];
  if (!FALLBACK_BY_ID) {
    FALLBACK_BY_ID = {};
    for (const m of FALLBACK_MODELS) FALLBACK_BY_ID[m.id] = m;
  }
  return FALLBACK_BY_ID[id];
}

export { GENERATED_MODELS };
export { fallbackModelsForSdk, FALLBACK_MODELS } from "./fallbacks";
