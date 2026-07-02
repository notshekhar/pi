import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, test } from "bun:test";
import { Session, SessionManager } from "../src/sessions";
import type { Entry } from "../src/types";
import { useTempSessionDb } from "./helpers/temp-db";

useTempSessionDb();

const dirs: string[] = [];
function mkSession() {
    const dir = mkdtempSync(join(tmpdir(), "loop-mgr-"));
    dirs.push(dir);
    const info = { id: "src", createdAt: 0, cwd: dir, provider: "anthropic" as const, model: "m0" };
    return new Session(info, join(dir, "src.jsonl"), []);
}
const user = (c: string, ts: number): Entry => ({ type: "message", role: "user", content: c, ts });
const asst = (c: string, ts: number): Entry => ({ type: "message", role: "assistant", content: c, ts });

afterEach(() => {
    while (dirs.length) rmSync(dirs.pop()!, { recursive: true, force: true });
});

describe("SessionManager.forkAtEntry", () => {
    test("forks the root→leaf path into a new single-root session file", async () => {
        const src = mkSession();
        const m1 = user("q1", 1);
        const m2 = asst("a1", 2);
        const m3 = user("q2", 3);
        const m4 = asst("a2", 4);
        for (const e of [m1, m2, m3, m4]) await src.append(e);

        const mgr = new SessionManager();
        const forked = mgr.forkAtEntry(src, m3.id!); // keep q1,a1,q2 — drop a2

        // New identity, recorded provenance, really persisted (reopens by id).
        expect(forked.info.id).not.toBe(src.info.id);
        expect(forked.info.parentSession).toBe(src.path);
        const reopened = await mgr.open(forked.info.id);
        expect(reopened.entries().length).toBe(forked.entries().length);

        // Single root (session-info) + the three kept messages, all reachable.
        const branch = forked.getBranch();
        expect(branch[0].type).toBe("session-info");
        expect(branch.filter((e) => e.type === "message").map((e: any) => e.content)).toEqual(["q1", "a1", "q2"]);
        // a2 did not come along.
        expect(forked.entries().some((e: any) => e.content === "a2")).toBe(false);
        // Every non-root entry chains back to a real parent (no dangling ids).
        const ids = new Set(forked.entries().map((e) => e.id));
        for (const e of forked.entries()) {
            if (e.parentId !== null) expect(ids.has(e.parentId!)).toBe(true);
        }
    });

    test("the source session is left completely untouched", async () => {
        const src = mkSession();
        for (const e of [user("q1", 1), asst("a1", 2), user("q2", 3)]) await src.append(e);
        const before = JSON.stringify(Session.load(src.path, src.info).entries());

        const mgr = new SessionManager();
        mgr.forkAtEntry(src, src.getLeafId()!);

        expect(JSON.stringify(Session.load(src.path, src.info).entries())).toBe(before);
    });

    test("labels on kept entries are reattached in the fork", async () => {
        const src = mkSession();
        const m1 = user("q1", 1);
        const m2 = asst("a1", 2);
        await src.append(m1);
        await src.append(m2);
        await src.appendLabelChange(m2.id!, "good answer");

        const mgr = new SessionManager();
        const forked = mgr.forkAtEntry(src, m2.id!);

        // The copied message keeps its id, so its label resolves in the fork.
        expect(forked.getLabel(m2.id!)).toBe("good answer");
    });

    test("throws when the leaf id is unknown", async () => {
        const src = mkSession();
        await src.append(user("q1", 1));
        const mgr = new SessionManager();
        expect(() => mgr.forkAtEntry(src, "no-such-id")).toThrow();
    });
});

describe("Session.appendAll", () => {
    test("batch entries chain leaf→child in order, one line each, and reload identically", async () => {
        const s = mkSession();
        await s.append(user("q1", 1));
        await s.appendAll([asst("a1", 2), asst("a2", 3), asst("a3", 4)]);

        // Chained under the previous leaf, in batch order.
        const branch = s.getBranch();
        expect(branch.map((e: any) => e.content)).toEqual(["q1", "a1", "a2", "a3"]);
        for (let i = 1; i < branch.length; i++) expect(branch[i].parentId).toBe(branch[i - 1].id!);

        // One row per entry in the store; a reload sees the same branch.
        const reloaded = Session.load(s.path, s.info);
        expect(reloaded.entries()).toHaveLength(4);
        expect(reloaded.getBranch().map((e: any) => e.content)).toEqual(["q1", "a1", "a2", "a3"]);
    });

    test("empty batch is a no-op (no store churn)", async () => {
        const s = mkSession();
        await s.append(user("q1", 1));
        const before = JSON.stringify(Session.load(s.path, s.info).entries());
        await s.appendAll([]);
        expect(JSON.stringify(Session.load(s.path, s.info).entries())).toBe(before);
    });
});

describe("SessionManager.open / list", () => {
    test("open() accepts an id or a transcript path and always sees the latest appends", async () => {
        const dir = mkdtempSync(join(tmpdir(), "loop-mgr-"));
        dirs.push(dir);
        const info = { id: "cachetest", createdAt: 0, cwd: dir, provider: "anthropic" as const, model: "m0" };
        const s = new Session(info, join(dir, "cachetest.jsonl"), []);
        await s.append({ type: "session-info", ts: 0, ...info } as any);
        await s.append(user("hello", 1));

        const mgr = new SessionManager();
        const first = await mgr.open("cachetest");
        expect(first.getBranch().some((e: any) => e.content === "hello")).toBe(true);

        // A subsequent append is visible on reopen — via the .jsonl-shaped
        // path form too (the picker hands paths back to open()).
        await first.append(user("world", 2));
        const again = await mgr.open(first.path);
        expect(again.getBranch().some((e: any) => e.content === "world")).toBe(true);
    });

    test("list() surfaces name, first user message, and cwd filtering", async () => {
        const mgr = new SessionManager();
        const dir = mkdtempSync(join(tmpdir(), "loop-mgr-"));
        dirs.push(dir);
        const a = await mgr.create({ cwd: dir, provider: "anthropic", model: "m0" });
        await a.append(user("first question", 1));
        await a.setName("My Session");
        const other = await mgr.create({ cwd: join(dir, "elsewhere"), provider: "anthropic", model: "m0" });
        await other.append(user("unrelated", 1));

        const listed = mgr.list(dir);
        expect(listed).toHaveLength(1);
        expect(listed[0].id).toBe(a.id);
        expect(listed[0].name).toBe("My Session");
        expect(listed[0].firstUserMessage).toBe("first question");
        // Unfiltered list sees both, newest activity first.
        expect(
            mgr
                .list()
                .map((s) => s.id)
                .sort(),
        ).toEqual([a.id, other.id].sort());
    });
});
