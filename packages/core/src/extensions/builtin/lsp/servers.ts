/**
 * Turns a registry entry into a runnable LspServerSpec: detect a
 * project-local/PATH server first (uses the project's own toolchain), else
 * provision one into ~/.loop/servers. Everything is driven by the registry.
 */
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { join } from "node:path";
import type { LspServerSpec } from "./client";
import { ensureProvisioned } from "./provision";
import { findDef, type LanguageKey, type LanguageServerDef, languageIdFor } from "./registry";

export { type LanguageKey, languageKeyFor } from "./registry";

function resolveBinary(rootPath: string, names: string[]): string | null {
    const isWin = process.platform === "win32";
    for (const name of names) {
        const local = join(rootPath, "node_modules", ".bin", isWin ? `${name}.cmd` : name);
        if (existsSync(local)) return local;
    }
    for (const name of names) {
        const probe = spawnSync(isWin ? "where" : "which", [name], { stdio: "pipe" });
        if (probe.status === 0) return name;
    }
    return null;
}

/**
 * Build a launch spec. "node" servers (e.g. typescript-language-server) run with
 * loop's runtime: process.execPath is the loop binary, and BUN_BE_BUN=1 makes it
 * behave as the bun CLI — so no separate node/bun is needed. "native" servers
 * (e.g. gopls) run directly.
 */
function specFromExe(def: LanguageServerDef, exe: string): LspServerSpec {
    const languageId = (absPath: string) => languageIdFor(def, absPath);
    const args = def.args ?? [];
    if (def.runtime === "node") {
        return { name: def.key, command: process.execPath, args: [exe, ...args], env: { BUN_BE_BUN: "1" }, languageId };
    }
    return { name: def.key, command: exe, args: [...args], languageId };
}

export function resolveServer(key: LanguageKey, rootPath: string): LspServerSpec | null {
    const def = findDef(key);
    if (!def) return null;
    const exe = resolveBinary(rootPath, def.binNames);
    return exe ? specFromExe(def, exe) : null;
}

export async function resolveOrProvisionServer(key: LanguageKey, rootPath: string): Promise<LspServerSpec | null> {
    const local = resolveServer(key, rootPath);
    if (local) return local;
    const def = findDef(key);
    if (!def?.npm || !def.npmBin) return null;
    const exe = await ensureProvisioned(key, def.npm, def.npmBin);
    return exe ? specFromExe(def, exe) : null;
}
