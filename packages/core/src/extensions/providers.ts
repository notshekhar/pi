/**
 * Pure helpers for turning extension-provider declarations into catalog
 * `ModelInfo`s. Kept free of I/O and of any provider/catalog import so it can be
 * unit-tested in isolation and reused by both the host and the catalog without
 * creating an import cycle (Dependency Inversion: callers depend on these pure
 * functions, not on each other).
 */
import type { ModelInfo } from "../types";
import type { ProviderModelSpec, ProviderPlugin } from "./api";

/** Map one provider model spec to a full catalog ModelInfo, filling defaults. */
export function providerModelToInfo(providerId: string, spec: ProviderModelSpec): ModelInfo {
    return {
        id: `${providerId}/${spec.id}`,
        provider: providerId,
        name: spec.name ?? spec.id,
        contextWindow: spec.contextWindow ?? 128_000,
        maxOutput: spec.maxOutput ?? 8_192,
        cost: {
            input: spec.cost?.input ?? 0,
            output: spec.cost?.output ?? 0,
            cacheRead: spec.cost?.cacheRead ?? 0,
            cacheWrite: spec.cost?.cacheWrite ?? 0,
        },
        reasoning: spec.reasoning ?? false,
        modalities: spec.modalities ?? ["text"],
        available: true,
    };
}

/**
 * Collect catalog entries from registered provider plugins' declarative
 * `models`, plus any directly-added `ModelInfo`s (api.models.add). Later entries
 * win id collisions, so direct adds override declared models, which override
 * earlier providers — a deterministic last-writer-wins.
 */
export function collectProviderModelInfos(plugins: Iterable<ProviderPlugin>, extra: ModelInfo[]): ModelInfo[] {
    const byId = new Map<string, ModelInfo>();
    for (const plugin of plugins) {
        for (const spec of plugin.models ?? []) {
            const info = providerModelToInfo(plugin.id, spec);
            byId.set(info.id, info);
        }
    }
    for (const info of extra) byId.set(info.id, info);
    return [...byId.values()];
}
