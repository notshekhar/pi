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
    maxSteps: 32,
    autoCompactThreshold: 0.8,
    piCompatMode: "fork",
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
