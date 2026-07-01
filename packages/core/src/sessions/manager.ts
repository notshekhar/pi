import { mkdirSync, readdirSync, readFileSync, statSync, existsSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { ulid } from "ulid";
import { getLoopDir } from "../auth/storage";
import { debugLog } from "../debug";
import type { Entry, ProviderId, SessionInfoData, UsageBlock } from "../types";
import { Session, generateEntryId } from "./session";
import { stripSessionHookContext } from "./hook-context";

/**
 * peek() parses a whole transcript to build one picker row; with long
 * histories the /resume list would otherwise re-read megabytes on every open.
 * Keyed by slug+path, invalidated by mtime — an appended entry bumps the
 * mtime, so a stale hit is impossible.
 */
const peekCache = new Map<string, { mtime: number; info: SessionInfo }>();

function slugCwd(cwd: string): string {
    // slug convention: "--Users-notshekhar-Documents-foo--"
    const stripped = cwd.replace(/^\/+|\/+$/g, "").replace(/\//g, "-");
    return `--${stripped}--`;
}

function sessionsDir(): string {
    const dir = join(getLoopDir(), "agent", "sessions");
    mkdirSync(dir, { recursive: true });
    return dir;
}

export interface SessionInfo extends SessionInfoData {
    path: string;
    mtime: number;
    firstUserMessage?: string;
    /** User-set display name (/name), latest session-name entry wins. */
    name?: string;
}

export interface NewSessionOptions {
    cwd: string;
    provider: ProviderId;
    model: string;
}

export class SessionManager {
    list(cwd?: string): SessionInfo[] {
        const root = sessionsDir();
        if (!existsSync(root)) return [];
        const out: SessionInfo[] = [];
        const slugs = cwd ? [slugCwd(cwd)] : readdirSync(root);
        for (const slug of slugs) {
            const dir = join(root, slug);
            if (!existsSync(dir)) continue;
            const stat = statSync(dir);
            if (!stat.isDirectory()) continue;
            for (const file of readdirSync(dir)) {
                if (!file.endsWith(".jsonl")) continue;
                const path = join(dir, file);
                const info = this.peek(path, slug);
                if (info) out.push(info);
            }
        }
        return out.sort((a, b) => b.mtime - a.mtime);
    }

    /**
     * Sum input+output tokens per local calendar day across every stored
     * session transcript. Powers /steak's usage heatmap — read-only and full
     * history, independent of the cost store (which is USD-only and accrues
     * from "now" forward). Keyed YYYY-MM-DD in local time, matching cost.ts.
     */
    dailyTokens(): Map<string, number> {
        const out = new Map<string, number>();
        const root = sessionsDir();
        if (!existsSync(root)) return out;
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
                this.accumulateDailyTokens(join(dir, file), out);
            }
        }
        return out;
    }

    private accumulateDailyTokens(path: string, out: Map<string, number>): void {
        let raw: string;
        try {
            raw = readFileSync(path, "utf8");
        } catch {
            return;
        }
        for (const line of raw.split("\n")) {
            if (!line) continue;
            let e: { type?: string; role?: string; ts?: number; usage?: UsageBlock };
            try {
                e = JSON.parse(line);
            } catch {
                continue;
            }
            if (typeof e.ts !== "number" || !e.usage) continue;
            const isAssistant = e.type === "message" && e.role === "assistant";
            if (!isAssistant && e.type !== "subagent") continue;
            const toks = (e.usage.inputTokens ?? 0) + (e.usage.outputTokens ?? 0);
            if (toks <= 0) continue;
            const key = new Date(e.ts).toLocaleDateString("sv");
            out.set(key, (out.get(key) ?? 0) + toks);
        }
    }

    private peek(path: string, slug: string): SessionInfo | null {
        try {
            const stat = statSync(path);
            const cacheKey = `${slug}\0${path}`;
            const cached = peekCache.get(cacheKey);
            if (cached && cached.mtime === stat.mtimeMs) return cached.info;
            const raw = readFileSync(path, "utf8");
            const lines = raw.split("\n").filter(Boolean);
            let info: SessionInfoData | null = null;
            let firstUser: string | undefined;
            let name: string | undefined;
            for (const line of lines) {
                let parsed: { type?: string; name?: string };
                try {
                    parsed = JSON.parse(line) as { type?: string; name?: string };
                } catch {
                    // A torn line (crash mid-append) must skip that line, not
                    // hide the whole session from the picker — Session.load
                    // tolerates it the same way.
                    continue;
                }
                if (parsed.type === "session-info") {
                    info = parsed as unknown as SessionInfoData;
                }
                if (parsed.type === "session-name") {
                    name = parsed.name?.trim() || undefined;
                }
                if (parsed.type === "message") {
                    const m = parsed as { role?: string; content?: unknown };
                    if (m.role === "user" && !firstUser) {
                        // Hook-context wrapper is model-facing — previews show
                        // what the user actually typed.
                        firstUser =
                            typeof m.content === "string"
                                ? stripSessionHookContext(m.content)
                                : JSON.stringify(m.content).slice(0, 120);
                    }
                }
            }
            if (!info) {
                const id = path.split("/").pop()!.replace(".jsonl", "");
                info = { id, createdAt: stat.birthtimeMs, cwd: slug, provider: "xai", model: "" };
            }
            const result: SessionInfo = { ...info, path, mtime: stat.mtimeMs, firstUserMessage: firstUser, name };
            peekCache.set(cacheKey, { mtime: stat.mtimeMs, info: result });
            return result;
        } catch (err) {
            debugLog("session-peek", `unreadable session ${path}:`, err as Error);
            return null;
        }
    }

    async create(opts: NewSessionOptions): Promise<Session> {
        const id = ulid();
        const slug = slugCwd(opts.cwd);
        const dir = join(sessionsDir(), slug);
        mkdirSync(dir, { recursive: true });
        const path = join(dir, `${id}.jsonl`);
        const info: SessionInfoData = {
            id,
            createdAt: Date.now(),
            cwd: opts.cwd,
            provider: opts.provider,
            model: opts.model,
        };
        const session = new Session(info, path, []);
        await session.append({ type: "session-info", ts: Date.now(), ...info });
        return session;
    }

    async open(idOrPath: string): Promise<Session> {
        const path = idOrPath.endsWith(".jsonl") ? idOrPath : this.findById(idOrPath);
        if (!path) throw new Error(`Session not found: ${idOrPath}`);
        const peek = this.peek(path, "");
        if (!peek) throw new Error(`Cannot read session: ${path}`);
        return Session.load(path, peek);
    }

    private findById(id: string): string | null {
        const root = sessionsDir();
        for (const slug of readdirSync(root)) {
            const candidate = join(root, slug, `${id}.jsonl`);
            if (existsSync(candidate)) return candidate;
        }
        return null;
    }

    /**
     * Write a forked session file containing `entries` (tree fields preserved).
     * The new session-info root carries the new session ulid as both session
     * id and tree id (matching create()); entries whose parent isn't part of
     * the copy are rewired to it so the fork has a single-root tree.
     */
    private writeFork(source: Session, entries: Entry[]): Session {
        const newId = ulid();
        const dir = join(source.path, "..");
        const newPath = join(dir, `${newId}.jsonl`);
        const info: SessionInfoData = {
            ...source.info,
            id: newId,
            createdAt: Date.now(),
            parentSession: source.path,
        };

        const copiedIds = new Set(entries.map((e) => e.id!));
        const root: Entry = { type: "session-info", ts: Date.now(), ...info, parentId: null };
        const out: Entry[] = [root];
        for (const e of entries) {
            const copy: Entry = { ...e };
            if (!copy.parentId || !copiedIds.has(copy.parentId)) copy.parentId = newId;
            out.push(copy);
        }

        // Re-attach labels for copied entries, chained at the end (the reference
        // createBranchedSession recreates them from the resolved map).
        const usedIds = new Set([newId, ...copiedIds]);
        let parentId = out[out.length - 1].id!;
        for (const e of entries) {
            const label = source.getLabel(e.id!);
            if (!label) continue;
            const labelId = generateEntryId((id) => usedIds.has(id));
            usedIds.add(labelId);
            out.push({ type: "label", ts: Date.now(), targetId: e.id!, label, id: labelId, parentId });
            parentId = labelId;
        }

        writeFileSync(newPath, out.map((e) => JSON.stringify(e)).join("\n") + "\n");
        return new Session(info, newPath, out);
    }

    /**
     * Fork the path root → `leafId` into a new session file (the reference
     * createBranchedSession). Abandoned branches stay behind; the new
     * session's header records the source via parentSession.
     */
    forkAtEntry(source: Session, leafId: string): Session {
        const path = source.getBranch(leafId);
        if (path.length === 0) throw new Error(`Entry ${leafId} not found`);
        return this.writeFork(
            source,
            path.filter((e) => e.type !== "session-info" && e.type !== "label"),
        );
    }
}
