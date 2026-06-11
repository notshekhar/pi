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
}
