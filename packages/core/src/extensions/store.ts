/**
 * The installed-extensions registry: ~/.loop/extensions.json. One record per
 * installed extension — its install source, resolved name/version, enabled
 * flag, and (for `loop link`) the linked source path. The extension code itself
 * lives in ~/.loop/extensions/<name>/; this file is just the index of what's
 * installed and which are on.
 */
import { join } from "node:path";
import { CachedStore, getLoopDir } from "../auth/storage";
import type { SourceKind } from "./sources";

export interface ExtensionRecord {
    name: string;
    version?: string;
    /** Original `loop install` argument, for reinstall/repair. */
    source: string;
    sourceKind: SourceKind;
    enabled: boolean;
    /** Absolute path for `loop link`ed extensions (loaded in place). */
    linkPath?: string;
    installedAt: number;
}

type Records = Record<string, ExtensionRecord>;

const store = new CachedStore(
    "loop-agent-extensions",
    { extensions: {} },
    {
        configPath: join(getLoopDir(), "extensions.json"),
    },
);

/** Root dir holding each extension's own directory. */
export function extensionsDir(): string {
    return join(getLoopDir(), "extensions");
}

export function extensionDir(name: string): string {
    return join(extensionsDir(), name);
}

export function listRecords(): ExtensionRecord[] {
    const recs = (store.get("extensions") as Records) ?? {};
    return Object.values(recs);
}

export function getRecord(name: string): ExtensionRecord | undefined {
    const recs = (store.get("extensions") as Records) ?? {};
    return recs[name];
}

export function putRecord(rec: ExtensionRecord): void {
    // Re-read before mutating: another loop process (a second session, a shell
    // `loop install`) may have written since our cache was filled.
    store.refresh();
    const recs = { ...((store.get("extensions") as Records) ?? {}) };
    recs[rec.name] = rec;
    store.set("extensions", recs);
}

export function deleteRecord(name: string): boolean {
    store.refresh();
    const recs = { ...((store.get("extensions") as Records) ?? {}) };
    if (!(name in recs)) return false;
    delete recs[name];
    store.set("extensions", recs);
    return true;
}

export function setRecordEnabled(name: string, enabled: boolean): boolean {
    const rec = getRecord(name);
    if (!rec) return false;
    putRecord({ ...rec, enabled });
    return true;
}

// ---- built-in extension enable state (kept separate from install records) ----

type Builtins = Record<string, boolean>;

/** Enabled state for a built-in, falling back to its default when unset. */
export function getBuiltinEnabled(name: string, fallback: boolean): boolean {
    const b = (store.get("builtins") as Builtins) ?? {};
    return name in b ? b[name] : fallback;
}

export function setBuiltinEnabled(name: string, enabled: boolean): void {
    store.refresh();
    const b = { ...((store.get("builtins") as Builtins) ?? {}) };
    b[name] = enabled;
    store.set("builtins", b);
}
