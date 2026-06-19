import { mkdirSync, readFileSync, appendFileSync, existsSync, writeFileSync } from "node:fs";
import { randomUUID } from "node:crypto";
import { join } from "node:path";
import lockfile from "proper-lockfile";
import type { Entry, SessionInfoData } from "../types";
import { adaptLoopEntry } from "./loop-adapter";

/** Generate a unique short ID (8 hex chars, collision-checked) — the reference parity. */
export function generateEntryId(has: (id: string) => boolean): string {
    for (let i = 0; i < 100; i++) {
        const id = randomUUID().slice(0, 8);
        if (!has(id)) return id;
    }
    return randomUUID();
}

/** Flatten message content (string or text-part array) to plain text. */
export function extractMessageText(content: unknown): string {
    if (typeof content === "string") return content;
    if (Array.isArray(content)) {
        return content
            .filter((c): c is { type: "text"; text: string } => !!c && typeof c === "object" && c.type === "text")
            .map((c) => c.text)
            .join("");
    }
    return "";
}

/** Tree node for getTree() — the reference SessionTreeNode equivalent. */
export interface SessionTreeNode {
    entry: Entry;
    children: SessionTreeNode[];
    /** Resolved label for this entry, if any. */
    label?: string;
    /** Timestamp (ms) of the latest label change for this entry, if any. */
    labelTimestamp?: number;
}

/**
 * A conversation session stored as an append-only tree in a JSONL file
 *.
 *
 * Every entry has an id and parentId. The "leaf" pointer tracks the current
 * position: append() creates a child of the leaf and advances it, branch()
 * moves the leaf to an earlier entry so the next append forks a new branch.
 * Existing entries are never modified or deleted.
 */
export class Session {
    readonly id: string;
    readonly info: SessionInfoData;
    readonly path: string;
    private buffered: Entry[] = [];
    private byId = new Map<string, Entry>();
    private labelsById = new Map<string, string>();
    private labelTimestampsById = new Map<string, number>();
    private leafId: string | null = null;
    private sessionName: string | undefined;

    constructor(info: SessionInfoData, path: string, buffered: Entry[]) {
        this.id = info.id;
        this.info = info;
        this.path = path;
        this.buffered = buffered;
        if (this.ensureTreeFields()) this.rewriteFile();
        this.rebuildIndex();
    }

    static load(path: string, info: SessionInfoData): Session {
        const raw = existsSync(path) ? readFileSync(path, "utf8") : "";
        const lines = raw.split("\n").filter(Boolean);
        const entries: Entry[] = [];
        for (const line of lines) {
            try {
                const parsed = JSON.parse(line);
                const adapted = adaptLoopEntry(parsed);
                if (adapted) entries.push(adapted);
            } catch {}
        }
        return new Session(info, path, entries);
    }

    /**
     * Migrate legacy flat entries (no id/parentId) to a linear chain —
     * Migrate legacy flat entries to the tree shape. Returns true if anything changed.
     */
    private ensureTreeFields(): boolean {
        const ids = new Set<string>();
        for (const e of this.buffered) if (e.id) ids.add(e.id);
        let changed = false;
        let prevId: string | null = null;
        for (const e of this.buffered) {
            if (!e.id) {
                e.id = generateEntryId((id) => ids.has(id));
                ids.add(e.id);
                e.parentId = prevId;
                changed = true;
            } else if (e.parentId === undefined) {
                e.parentId = prevId;
                changed = true;
            }
            prevId = e.id;
        }
        return changed;
    }

    private rewriteFile(): void {
        if (!existsSync(this.path)) return;
        writeFileSync(
            this.path,
            this.buffered.map((e) => JSON.stringify(e)).join("\n") + (this.buffered.length ? "\n" : ""),
        );
    }

    private rebuildIndex(): void {
        this.byId.clear();
        this.labelsById.clear();
        this.labelTimestampsById.clear();
        this.leafId = null;
        for (const e of this.buffered) {
            if (!e.id) continue;
            this.byId.set(e.id, e);
            this.leafId = e.id;
            if (e.type === "label") this.applyLabel(e.targetId, e.label, e.ts);
            if (e.type === "session-name") this.sessionName = e.name?.trim() || undefined;
        }
    }

    private applyLabel(targetId: string, label: string | undefined, ts: number): void {
        if (label) {
            this.labelsById.set(targetId, label);
            this.labelTimestampsById.set(targetId, ts);
        } else {
            this.labelsById.delete(targetId);
            this.labelTimestampsById.delete(targetId);
        }
    }

    /** All entries in file order (includes abandoned branches). */
    entries(): Entry[] {
        return [...this.buffered];
    }

    /**
     * Append as a child of the current leaf, then advance the leaf.
     * Entries arriving with an id/parentId already set (fork/import copies)
     * keep them so the tree structure survives the copy.
     */
    async append(entry: Entry): Promise<void> {
        if (!entry.id || this.byId.has(entry.id)) {
            entry.id = generateEntryId((id) => this.byId.has(id));
            entry.parentId = this.leafId;
        } else if (entry.parentId === undefined) {
            entry.parentId = this.leafId;
        }
        this.buffered.push(entry);
        this.byId.set(entry.id, entry);
        this.leafId = entry.id;
        if (entry.type === "label") this.applyLabel(entry.targetId, entry.label, entry.ts);
        if (entry.type === "session-name") this.sessionName = entry.name?.trim() || undefined;

        const dir = join(this.path, "..");
        mkdirSync(dir, { recursive: true });
        if (!existsSync(this.path)) writeFileSync(this.path, "");
        const release = await lockfile.lock(this.path, { retries: { retries: 5, minTimeout: 50, maxTimeout: 200 } });
        try {
            appendFileSync(this.path, JSON.stringify(entry) + "\n");
        } finally {
            await release();
        }
    }

    // =========================================================================
    // Tree traversal
    // =========================================================================

    getLeafId(): string | null {
        return this.leafId;
    }

    getEntry(id: string): Entry | undefined {
        return this.byId.get(id);
    }

    getLabel(id: string): string | undefined {
        return this.labelsById.get(id);
    }

    /** Move the leaf pointer to an earlier entry; the next append() branches there. */
    branch(branchFromId: string): void {
        if (!this.byId.has(branchFromId)) throw new Error(`Entry ${branchFromId} not found`);
        this.leafId = branchFromId;
    }

    /** Reset the leaf to before any entries; the next append() creates a new root. */
    resetLeaf(): void {
        this.leafId = null;
    }

    /** Walk from an entry (default: leaf) to the root. Returns root-first path. */
    getBranch(fromId?: string | null): Entry[] {
        const path: Entry[] = [];
        const startId = fromId === undefined ? this.leafId : fromId;
        let current = startId ? this.byId.get(startId) : undefined;
        while (current) {
            path.unshift(current);
            current = current.parentId ? this.byId.get(current.parentId) : undefined;
        }
        return path;
    }

    /**
     * Branch to an entry and record a summary of the abandoned path
     *. Pass null to branch before the root.
     */
    async branchWithSummary(branchFromId: string | null, summary: string): Promise<string> {
        if (branchFromId !== null && !this.byId.has(branchFromId)) {
            throw new Error(`Entry ${branchFromId} not found`);
        }
        this.leafId = branchFromId;
        const entry: Entry = {
            type: "branch-summary",
            ts: Date.now(),
            summary,
            fromId: branchFromId ?? "root",
            parentId: branchFromId,
        };
        await this.append(entry);
        return entry.id!;
    }

    /** User-set display name; latest session-name entry wins. */
    getName(): string | undefined {
        return this.sessionName;
    }

    /** Set (or clear, with empty string) the session display name — the reference appendSessionInfo. */
    async setName(name: string): Promise<void> {
        await this.append({ type: "session-name", ts: Date.now(), name: name.trim() });
    }

    /** Set or clear a user label on an entry. */
    async appendLabelChange(targetId: string, label: string | undefined): Promise<string> {
        if (!this.byId.has(targetId)) throw new Error(`Entry ${targetId} not found`);
        const entry: Entry = { type: "label", ts: Date.now(), targetId, label };
        await this.append(entry);
        return entry.id!;
    }

    /**
     * The session as a tree. A well-formed session has one root; orphaned
     * entries (broken parent chain) are returned as additional roots.
     */
    getTree(): SessionTreeNode[] {
        const nodeMap = new Map<string, SessionTreeNode>();
        const roots: SessionTreeNode[] = [];
        for (const e of this.buffered) {
            if (!e.id) continue;
            nodeMap.set(e.id, {
                entry: e,
                children: [],
                label: this.labelsById.get(e.id),
                labelTimestamp: this.labelTimestampsById.get(e.id),
            });
        }
        for (const e of this.buffered) {
            if (!e.id) continue;
            const node = nodeMap.get(e.id)!;
            const parent = e.parentId && e.parentId !== e.id ? nodeMap.get(e.parentId) : undefined;
            if (parent) parent.children.push(node);
            else roots.push(node);
        }
        // Children oldest-first; iterative to survive deep trees.
        const stack: SessionTreeNode[] = [...roots];
        while (stack.length > 0) {
            const node = stack.pop()!;
            node.children.sort((a, b) => a.entry.ts - b.entry.ts);
            stack.push(...node.children);
        }
        return roots;
    }

    /** All user messages (any branch) for the /fork selector — the reference parity. */
    getUserMessagesForForking(): Array<{ entryId: string; text: string }> {
        const out: Array<{ entryId: string; text: string }> = [];
        for (const e of this.buffered) {
            if (e.type !== "message" || e.role !== "user" || !e.id) continue;
            const text = extractMessageText(e.content);
            if (text) out.push({ entryId: e.id, text });
        }
        return out;
    }

    /**
     * Messages along the current branch path (leaf → root). Abandoned
     * branches no longer reach the model context after /tree navigation.
     */
    messages(): Array<{ role: "user" | "assistant" | "tool"; content: unknown }> {
        return this.getBranch()
            .filter((e): e is Extract<Entry, { type: "message" }> => e.type === "message")
            .map((e) => ({ role: e.role, content: e.content }));
    }

    lastCompactCutAt(): number {
        let cut = 0;
        for (const e of this.getBranch()) {
            if (e.type === "compact") cut = Math.max(cut, e.cutAt);
        }
        return cut;
    }
}
