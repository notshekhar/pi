/**
 * The set of language servers this extension knows about. Built-ins live here in
 * code; users add or override entries in ~/.loop/servers/servers.json — no
 * recompile needed. Each entry says how to FIND the server (binNames, looked up
 * in the project's node_modules/.bin then PATH) and, optionally, how to INSTALL
 * it when absent (npm deps provisioned into ~/.loop/servers/<key>/).
 */
import { existsSync, readFileSync } from "node:fs";
import { extname, join } from "node:path";
import { homedir } from "node:os";

export type LanguageKey = string;

export interface LanguageServerDef {
    key: LanguageKey;
    extensions: string[];
    languageId: string | ((absPath: string) => string);
    runtime?: "node" | "native";
    binNames: string[];
    args?: string[];
    npm?: Record<string, string>;
    npmBin?: string;
}

function tsLanguageId(absPath: string): string {
    const ext = extname(absPath).toLowerCase();
    if (ext === ".tsx") return "typescriptreact";
    if (ext === ".jsx") return "javascriptreact";
    if (ext === ".js" || ext === ".mjs" || ext === ".cjs") return "javascript";
    return "typescript";
}

const BUILTINS: LanguageServerDef[] = [
    {
        key: "typescript",
        extensions: [".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"],
        languageId: tsLanguageId,
        runtime: "node",
        binNames: ["typescript-language-server"],
        args: ["--stdio"],
        npm: { "typescript-language-server": "^5.3.0", typescript: "^6.0.0" },
        npmBin: join("node_modules", ".bin", "typescript-language-server"),
    },
];

const MANIFEST_PATH = join(homedir(), ".loop", "servers", "servers.json");

let cache: LanguageServerDef[] | null = null;

export function getServerDefs(): LanguageServerDef[] {
    if (cache) return cache;
    const byKey = new Map<string, LanguageServerDef>();
    for (const def of BUILTINS) byKey.set(def.key, def);
    for (const def of loadManifest()) byKey.set(def.key, def);
    cache = [...byKey.values()];
    return cache;
}

export function reloadServerDefs(): void {
    cache = null;
}

export function findDef(key: LanguageKey): LanguageServerDef | undefined {
    return getServerDefs().find((d) => d.key === key);
}

export function languageKeyFor(absPath: string): LanguageKey | null {
    const ext = extname(absPath).toLowerCase();
    for (const def of getServerDefs()) {
        if (def.extensions.includes(ext)) return def.key;
    }
    return null;
}

export function languageIdFor(def: LanguageServerDef, absPath: string): string {
    return typeof def.languageId === "function" ? def.languageId(absPath) : def.languageId;
}

function loadManifest(): LanguageServerDef[] {
    if (!existsSync(MANIFEST_PATH)) return [];
    try {
        const raw = JSON.parse(readFileSync(MANIFEST_PATH, "utf-8")) as Record<string, unknown>;
        const defs: LanguageServerDef[] = [];
        for (const [key, value] of Object.entries(raw)) {
            const v = value as Partial<LanguageServerDef> & { extensions?: unknown };
            if (!Array.isArray(v.extensions) || typeof v.languageId !== "string" || !Array.isArray(v.binNames)) {
                continue;
            }
            defs.push({
                key,
                extensions: v.extensions.map((e) => String(e).toLowerCase()),
                languageId: v.languageId,
                runtime: v.runtime === "node" ? "node" : "native",
                binNames: v.binNames.map(String),
                args: Array.isArray(v.args) ? v.args.map(String) : [],
                npm: v.npm && typeof v.npm === "object" ? (v.npm as Record<string, string>) : undefined,
                npmBin: typeof v.npmBin === "string" ? v.npmBin : undefined,
            });
        }
        return defs;
    } catch {
        return [];
    }
}
