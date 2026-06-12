import Configstore from "configstore";
import { homedir } from "node:os";
import { join } from "node:path";

const PI_DIR = join(homedir(), ".pi");

export const authStore = new Configstore(
    "pi-agent-auth",
    { providers: {}, active: null },
    { configPath: join(PI_DIR, "auth.json") },
);

export const settingsStore = new Configstore(
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

export const costStore = new Configstore(
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
