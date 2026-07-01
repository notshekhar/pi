import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, test } from "bun:test";
import { Session, SessionManager } from "../src/sessions";
import type { Entry } from "../src/types";

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

        // New identity, recorded provenance, real file on disk.
        expect(forked.info.id).not.toBe(src.info.id);
        expect(forked.info.parentSession).toBe(src.path);
        expect(existsSync(forked.path)).toBe(true);

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
        const before = readFileSync(src.path, "utf8");

        const mgr = new SessionManager();
        mgr.forkAtEntry(src, src.getLeafId()!);

        expect(readFileSync(src.path, "utf8")).toBe(before);
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
