import Configstore from "configstore";
import { homedir } from "node:os";
import { join } from "node:path";

const PI_DIR = join(homedir(), ".pi");

type StoreData = Record<string, unknown>;

/**
 * configstore reads + JSON-parses the whole file from disk on EVERY `.get`/`.all`
 * and rewrites it on every `.set` — it has no in-memory cache (verified against
 * configstore@7 source: `get all()` calls `readFileSync` each time). In our hot
 * paths that synchronous read blocks the event loop: the agent loop reads ~8
 * settings per turn (plus per subagent), and the footer ticker reads several
 * every second. That blocking I/O is a prime suspect for the UI freezing.
 *
 * CachedStore keeps the parsed object in memory and only touches the disk on a
 * real write. Reads come from the cache; writes go through to configstore (for
 * persistence) and refresh the cache. `refresh()` drops the cache so an external
 * edit to the file is picked up on the next read.
 */
export class CachedStore {
    private readonly store: Configstore;
    private cache: StoreData | null = null;

    constructor(id: string, defaults: StoreData, options: { configPath: string }) {
        this.store = new Configstore(id, defaults, options);
    }

    get all(): StoreData {
        if (this.cache === null) this.cache = this.store.all as StoreData;
        return this.cache;
    }

    set all(value: StoreData) {
        this.store.all = value;
        this.cache = value;
    }

    get(key: string): unknown {
        return this.all[key];
    }

    set(key: string, value: unknown): void {
        this.store.set(key, value);
        // Keep the cache coherent without a re-read. If nothing is cached yet,
        // leave it null so the next read pulls the merged-with-defaults file.
        if (this.cache !== null) this.cache[key] = value;
    }

    /** Force a re-read on next access — call after an external write to the file. */
    refresh(): void {
        this.cache = null;
    }
}

export const authStore = new CachedStore(
    "pi-agent-auth",
    { providers: {}, active: null },
    { configPath: join(PI_DIR, "auth.json") },
);

export const settingsStore = new CachedStore(
    "pi-agent-settings",
    {
        defaultModel: null,
        theme: "dark",
        maxSteps: 0, // 0 = unlimited; loop ends when the model stops calling tools
        autoCompactThreshold: 0.8,
        piCompatMode: "direct",
        workspaceContext: true,
    },
    { configPath: join(PI_DIR, "settings.json") },
);

export const costStore = new CachedStore(
    "pi-agent-cost",
    { lifetime: { usd: 0, byProvider: {} } },
    { configPath: join(PI_DIR, "cost.json") },
);

export function getPiDir(): string {
    return PI_DIR;
}

/**
 * Per-project model memory: the last model picked while working in a folder
 * is restored next time pi starts there. Global defaultModel stays the
 * fallback for folders never seen before.
 */
export function getProjectModel(cwd: string): string | undefined {
    const m = settingsStore.get("projectModels") as Record<string, string> | undefined;
    const id = m?.[cwd];
    return typeof id === "string" && id ? id : undefined;
}

export function setProjectModel(cwd: string, modelId: string): void {
    const m = (settingsStore.get("projectModels") as Record<string, string> | undefined) ?? {};
    settingsStore.set("projectModels", { ...m, [cwd]: modelId });
    // Also remember the pick per provider, so switching providers and back
    // restores the model used last with that provider in this folder.
    const provider = providerOfModelId(modelId);
    if (!provider) return;
    const pm =
        (settingsStore.get("projectProviderModels") as Record<string, Record<string, string>> | undefined) ?? {};
    settingsStore.set("projectProviderModels", { ...pm, [cwd]: { ...pm[cwd], [provider]: modelId } });
}

/**
 * Last model used with a given provider in this folder. Lets /provider
 * restore the model you had with that provider instead of its first model.
 */
export function getProjectProviderModel(cwd: string, provider: string): string | undefined {
    const pm = settingsStore.get("projectProviderModels") as Record<string, Record<string, string>> | undefined;
    const id = pm?.[cwd]?.[provider];
    return typeof id === "string" && id ? id : undefined;
}

// Mirrors providers/parseModelId; inlined (and non-throwing) to avoid an
// auth ↔ providers import cycle.
function providerOfModelId(modelId: string): string | undefined {
    const start = modelId.startsWith("custom:") ? "custom:".length : 0;
    const idx = modelId.indexOf("/", start);
    return idx > 0 ? modelId.slice(0, idx) : undefined;
}
