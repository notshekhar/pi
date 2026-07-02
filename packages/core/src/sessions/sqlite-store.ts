import type { Database } from "bun:sqlite";
import type { Entry, ProviderId, SessionInfoData } from "../types";
import { getDb } from "./db";
import { normalizeUsage } from "./usage";

/**
 * Prepared-statement layer over the session tables. Sessions and entries keep
 * their public identities (session ulid, 8-hex entry id) in pub_id columns;
 * the integer PKs never leave this file. Entry `payload` is the full entry
 * JSON and is the single source of truth — the usage/model columns are
 * derived at insert for aggregate queries and are never read back into an
 * Entry.
 */

export interface SessionRecord {
    rowId: number;
    info: SessionInfoData;
    name?: string;
    updatedAt: number;
    /** Payload JSON of the first user message, when requested by list(). */
    firstUserPayload?: string;
}

interface SessionRow {
    id: number;
    pub_id: string;
    cwd: string;
    provider: string;
    model: string;
    name: string | null;
    parent_pub: string | null;
    created_at: number;
    updated_at: number;
    first_user_payload?: string | null;
}

function toRecord(r: SessionRow): SessionRecord {
    return {
        rowId: r.id,
        info: {
            id: r.pub_id,
            createdAt: r.created_at,
            cwd: r.cwd,
            provider: r.provider as ProviderId,
            model: r.model,
            ...(r.parent_pub ? { parentSession: r.parent_pub } : {}),
        },
        name: r.name ?? undefined,
        updatedAt: r.updated_at,
        firstUserPayload: r.first_user_payload ?? undefined,
    };
}

const FIRST_USER_PAYLOAD = `(
    SELECT e.payload FROM entries e
    WHERE e.session_id = s.id AND e.type = 'message' AND e.role = 'user'
    ORDER BY e.id LIMIT 1
) AS first_user_payload`;

export class SessionStore {
    constructor(private db: Database) {}

    /** Insert the session row if it doesn't exist yet; return the internal id. */
    ensureSession(info: SessionInfoData, updatedAt = Date.now()): number {
        this.db
            .query(
                `INSERT OR IGNORE INTO sessions (pub_id, cwd, provider, model, parent_pub, created_at, updated_at)
                 VALUES (?, ?, ?, ?, ?, ?, ?)`,
            )
            .run(info.id, info.cwd, info.provider, info.model, info.parentSession ?? null, info.createdAt, updatedAt);
        const row = this.db.query<{ id: number }, [string]>("SELECT id FROM sessions WHERE pub_id = ?").get(info.id);
        if (!row) throw new Error(`session row vanished for ${info.id}`);
        return row.id;
    }

    getSession(pubId: string): SessionRecord | null {
        const row = this.db.query<SessionRow, [string]>("SELECT * FROM sessions AS s WHERE pub_id = ?").get(pubId);
        return row ? toRecord(row) : null;
    }

    listSessions(cwd?: string): SessionRecord[] {
        const sql = `SELECT s.*, ${FIRST_USER_PAYLOAD} FROM sessions s
                     ${cwd !== undefined ? "WHERE s.cwd = ?" : ""}
                     ORDER BY s.updated_at DESC`;
        const rows =
            cwd !== undefined
                ? this.db.query<SessionRow, [string]>(sql).all(cwd)
                : this.db.query<SessionRow, []>(sql).all();
        return rows.map(toRecord);
    }

    /** Append one batch of entries in a single transaction (the appendAll contract). */
    appendEntries(sessionRowId: number, entries: Entry[]): void {
        if (entries.length === 0) return;
        const tx = this.db.transaction(() => {
            for (const e of entries) this.insertEntry(sessionRowId, e);
            this.db.query("UPDATE sessions SET updated_at = ? WHERE id = ?").run(Date.now(), sessionRowId);
            this.applyNameChanges(sessionRowId, entries);
        });
        tx();
    }

    loadEntries(sessionRowId: number): Entry[] {
        const rows = this.db
            .query<{ payload: string }, [number]>("SELECT payload FROM entries WHERE session_id = ? ORDER BY id")
            .all(sessionRowId);
        return rows.map((r) => JSON.parse(r.payload) as Entry);
    }

    /**
     * Rewrite a session's entries wholesale — the legacy-upgrade path
     * (ensureTreeFields assigned ids on load), replacing the old rewriteFile.
     */
    replaceEntries(sessionRowId: number, entries: Entry[]): void {
        const tx = this.db.transaction(() => {
            this.db.query("DELETE FROM entries WHERE session_id = ?").run(sessionRowId);
            for (const e of entries) this.insertEntry(sessionRowId, e);
        });
        tx();
    }

    /** Create a session and its entries atomically — the fork path. */
    insertSessionWithEntries(info: SessionInfoData, entries: Entry[]): number {
        const tx = this.db.transaction(() => {
            const rowId = this.ensureSession(info);
            for (const e of entries) this.insertEntry(rowId, e);
            this.applyNameChanges(rowId, entries);
            return rowId;
        });
        return tx() as number;
    }

    /**
     * The migration/import path: INSERT OR IGNORE end to end, so re-running
     * after a crash (or hitting duplicate pub_ids across files) skips whatever
     * already landed instead of failing. updated_at is the source file's mtime
     * so /resume ordering survives the move.
     */
    importSession(info: SessionInfoData, entries: Entry[], updatedAt = Date.now()): number {
        const tx = this.db.transaction(() => {
            const rowId = this.ensureSession(info, updatedAt);
            for (const e of entries) this.insertEntry(rowId, e, true);
            this.applyNameChanges(rowId, entries);
            return rowId;
        });
        return tx() as number;
    }

    /**
     * input+output tokens per local calendar day, across all sessions — the
     * /steak heatmap. date(…,'localtime') matches toLocaleDateString("sv").
     */
    dailyTokens(): Map<string, number> {
        const rows = this.db
            .query<{ day: string; toks: number }, []>(
                `SELECT date(ts / 1000, 'unixepoch', 'localtime') AS day,
                        SUM(COALESCE(usage_input, 0) + COALESCE(usage_output, 0)) AS toks
                 FROM entries
                 WHERE ((type = 'message' AND role = 'assistant') OR type = 'subagent')
                   AND COALESCE(usage_input, 0) + COALESCE(usage_output, 0) > 0
                 GROUP BY day`,
            )
            .all();
        return new Map(rows.map((r) => [r.day, r.toks]));
    }

    private insertEntry(sessionRowId: number, e: Entry, orIgnore = false): void {
        const usage = "usage" in e && e.usage ? normalizeUsage(e.usage) : null;
        this.db
            .query(
                `INSERT ${orIgnore ? "OR IGNORE " : ""}INTO entries (
                    session_id, pub_id, parent_pub_id, ts, type, role, payload,
                    usage_input, usage_output, usage_total, usage_no_cache,
                    usage_cache_read, usage_cache_write, usage_text,
                    usage_reasoning, usage_estimated, model
                 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
            )
            .run(
                sessionRowId,
                e.id!,
                e.parentId ?? null,
                e.ts,
                e.type,
                e.type === "message" ? e.role : null,
                JSON.stringify(e),
                usage?.input ?? null,
                usage?.output ?? null,
                usage?.total ?? null,
                usage?.noCache ?? null,
                usage?.cacheRead ?? null,
                usage?.cacheWrite ?? null,
                usage?.text ?? null,
                usage?.reasoning ?? null,
                usage ? (usage.estimated ? 1 : 0) : null,
                "model" in e ? (e.model ?? null) : null,
            );
    }

    /** Denormalize the latest session-name in a batch onto the session row. */
    private applyNameChanges(sessionRowId: number, entries: Entry[]): void {
        let seen = false;
        let name: string | null = null;
        for (const e of entries) {
            if (e.type !== "session-name") continue;
            seen = true;
            name = e.name?.trim() || null;
        }
        if (seen) this.db.query("UPDATE sessions SET name = ? WHERE id = ?").run(name, sessionRowId);
    }
}

let cached: { db: Database; store: SessionStore } | null = null;

/** The store bound to the current db singleton (re-bound when tests swap the path). */
export function getSessionStore(): SessionStore {
    const db = getDb();
    if (!cached || cached.db !== db) cached = { db, store: new SessionStore(db) };
    return cached.store;
}
