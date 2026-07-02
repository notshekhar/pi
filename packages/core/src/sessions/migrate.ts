import type { Database } from "bun:sqlite";
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { basename, join } from "node:path";
import { getLoopDir } from "../auth/storage";
import { debugLog } from "../debug";
import type { Entry, ProviderId, SessionInfoData } from "../types";
import { adaptLoopEntry } from "./loop-adapter";
import { ensureTreeFields } from "./session";
import { SessionStore } from "./sqlite-store";

/**
 * One-time move of JSONL transcripts into the session DB, plus the
 * import-on-open fallback for stragglers (a .jsonl restored from backup after
 * migration already ran). Originals are never deleted — they stay on disk as
 * the downgrade path and the corruption-recovery source.
 */

export interface ParsedSessionFile {
    info: SessionInfoData;
    entries: Entry[];
    /** Source file mtime — becomes updated_at so /resume ordering survives. */
    mtime: number;
}

export function legacySessionsRoot(): string {
    return join(getLoopDir(), "agent", "sessions");
}

/**
 * Read a JSONL transcript with the exact tolerance the old Session.load had:
 * corrupt (torn) lines are skipped, legacy entry shapes are adapted, and flat
 * entries get tree fields. The last session-info line wins as the session's
 * identity (matching the old peek()); files without one fall back to the
 * filename id, the file's birthtime, and `fallbackCwd`.
 */
export function parseSessionFile(path: string, fallbackCwd: string): ParsedSessionFile | null {
    let raw: string;
    let stat: ReturnType<typeof statSync>;
    try {
        raw = readFileSync(path, "utf8");
        stat = statSync(path);
    } catch (err) {
        debugLog("session-migrate", `unreadable session file ${path}:`, err as Error);
        return null;
    }
    const entries: Entry[] = [];
    for (const line of raw.split("\n")) {
        if (!line.trim()) continue;
        try {
            const adapted = adaptLoopEntry(JSON.parse(line));
            if (adapted) entries.push(adapted);
        } catch {
            debugLog("session-migrate", `skipped corrupt line in ${path}`);
        }
    }
    ensureTreeFields(entries);

    let info: SessionInfoData | null = null;
    for (const e of entries) {
        if (e.type === "session-info") {
            info = {
                id: e.id!,
                createdAt: e.createdAt,
                cwd: e.cwd,
                provider: e.provider,
                model: e.model,
                ...(e.parentSession ? { parentSession: e.parentSession } : {}),
            };
        }
    }
    if (!info) {
        info = {
            id: basename(path).replace(/\.jsonl$/, ""),
            createdAt: stat.birthtimeMs,
            cwd: fallbackCwd,
            provider: "xai" as ProviderId,
            model: "",
        };
    }
    return { info, entries, mtime: stat.mtimeMs };
}

/** Import one transcript file; returns the session's pub id, or null if unreadable. */
export function importSessionFile(store: SessionStore, path: string, fallbackCwd: string): string | null {
    const parsed = parseSessionFile(path, fallbackCwd);
    if (!parsed) return null;
    store.importSession(parsed.info, parsed.entries, parsed.mtime);
    return parsed.info.id;
}

/**
 * Walk the legacy sessions dir and import every transcript. Idempotent and
 * resumable: everything funnels through INSERT OR IGNORE, so a crash mid-way
 * re-runs cleanly and duplicate pub_ids across files merge instead of failing.
 * A single bad file is logged and skipped, never fatal.
 */
export function migrateLegacySessions(db: Database, root = legacySessionsRoot()): void {
    const done = db.query<{ value: string }, []>("SELECT value FROM meta WHERE key = 'migrated_at'").get();
    if (done) return;
    const store = new SessionStore(db);
    if (existsSync(root)) {
        for (const slug of readdirSync(root)) {
            const dir = join(root, slug);
            let dirStat;
            try {
                dirStat = statSync(dir);
            } catch {
                continue;
            }
            if (!dirStat.isDirectory()) continue;
            for (const file of readdirSync(dir)) {
                if (!file.endsWith(".jsonl")) continue;
                try {
                    importSessionFile(store, join(dir, file), slug);
                } catch (err) {
                    debugLog("session-migrate", `failed to migrate ${join(dir, file)}:`, err as Error);
                }
            }
        }
    }
    db.run("INSERT OR REPLACE INTO meta (key, value) VALUES ('migrated_at', ?)", [String(Date.now())]);
}
