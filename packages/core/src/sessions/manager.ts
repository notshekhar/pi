import { existsSync, readdirSync } from "node:fs";
import { basename, dirname, join } from "node:path";
import { ulid } from "ulid";
import { getLoopDir } from "../auth/storage";
import { debugLog } from "../debug";
import type { Entry, ProviderId, SessionInfoData } from "../types";
import { Session, generateEntryId } from "./session";
import { stripSessionHookContext } from "./hook-context";
import { importSessionFile, legacySessionsRoot } from "./migrate";
import { getSessionStore, type SessionRecord } from "./sqlite-store";

function slugCwd(cwd: string): string {
    // slug convention: "--Users-notshekhar-Documents-foo--"
    const stripped = cwd.replace(/^\/+|\/+$/g, "").replace(/\//g, "-");
    return `--${stripped}--`;
}

/**
 * The canonical transcript address a session is known by (picker values,
 * hooks' transcript_path). Entries live in the session DB; this path is the
 * session's public name, kept in the historical JSONL shape.
 */
function transcriptPath(cwd: string, id: string): string {
    return join(getLoopDir(), "agent", "sessions", slugCwd(cwd), `${id}.jsonl`);
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

function toSessionInfo(record: SessionRecord): SessionInfo {
    let firstUser: string | undefined;
    if (record.firstUserPayload) {
        try {
            const m = JSON.parse(record.firstUserPayload) as { content?: unknown };
            // Hook-context wrapper is model-facing — previews show what the
            // user actually typed.
            firstUser =
                typeof m.content === "string"
                    ? stripSessionHookContext(m.content)
                    : JSON.stringify(m.content).slice(0, 120);
        } catch (err) {
            debugLog("session-list", `bad first-user payload for ${record.info.id}:`, err as Error);
        }
    }
    return {
        ...record.info,
        path: transcriptPath(record.info.cwd, record.info.id),
        mtime: record.updatedAt,
        firstUserMessage: firstUser,
        name: record.name,
    };
}

export class SessionManager {
    list(cwd?: string): SessionInfo[] {
        return getSessionStore().listSessions(cwd).map(toSessionInfo);
    }

    /**
     * Sum input+output tokens per local calendar day across every stored
     * session. Powers /steak's usage heatmap — one GROUP BY over the derived
     * usage columns, independent of the cost store (which is USD-only).
     * Keyed YYYY-MM-DD in local time, matching cost.ts.
     */
    dailyTokens(): Map<string, number> {
        return getSessionStore().dailyTokens();
    }

    async create(opts: NewSessionOptions): Promise<Session> {
        const id = ulid();
        const info: SessionInfoData = {
            id,
            createdAt: Date.now(),
            cwd: opts.cwd,
            provider: opts.provider,
            model: opts.model,
        };
        const session = new Session(info, transcriptPath(opts.cwd, id), []);
        await session.append({ type: "session-info", ts: Date.now(), ...info });
        return session;
    }

    async open(idOrPath: string): Promise<Session> {
        const isPath = idOrPath.endsWith(".jsonl");
        const id = isPath ? basename(idOrPath).replace(/\.jsonl$/, "") : idOrPath;
        let record = getSessionStore().getSession(id);
        if (!record) {
            // Straggler transcript (restored from backup, or a direct path to
            // a file that was never migrated): import it, then open normally.
            const file = isPath ? idOrPath : this.findLegacyFile(id);
            if (file && existsSync(file)) {
                const importedId = importSessionFile(getSessionStore(), file, basename(dirname(file)));
                if (importedId) record = getSessionStore().getSession(importedId);
            }
        }
        if (!record) throw new Error(`Session not found: ${idOrPath}`);
        return Session.load(transcriptPath(record.info.cwd, record.info.id), record.info);
    }

    /** The old findById dir scan — kept only as the import-on-open fallback. */
    private findLegacyFile(id: string): string | null {
        const root = legacySessionsRoot();
        if (!existsSync(root)) return null;
        for (const slug of readdirSync(root)) {
            const candidate = join(root, slug, `${id}.jsonl`);
            if (existsSync(candidate)) return candidate;
        }
        return null;
    }

    /**
     * Persist a forked session containing `entries` (tree fields preserved).
     * The new session-info root carries the new session ulid as both session
     * id and tree id (matching create()); entries whose parent isn't part of
     * the copy are rewired to it so the fork has a single-root tree.
     */
    private writeFork(source: Session, entries: Entry[]): Session {
        const newId = ulid();
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

        getSessionStore().insertSessionWithEntries(info, out);
        return new Session(info, transcriptPath(info.cwd, newId), out);
    }

    /**
     * Fork the path root → `leafId` into a new session (the reference
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
