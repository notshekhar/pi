import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, test } from "bun:test";
import { Session } from "../src/sessions";
import type { Entry } from "../src/types";

const dirs: string[] = [];
function mk(buffered: Entry[] = []) {
    const dir = mkdtempSync(join(tmpdir(), "loop-tree-"));
    dirs.push(dir);
    const info = { id: "s1", createdAt: 0, cwd: dir, provider: "anthropic" as const, model: "m0" };
    return new Session(info, join(dir, "s.jsonl"), buffered);
}
const user = (content: string, ts: number): Entry => ({ type: "message", role: "user", content, ts });
const asst = (content: string, ts: number, model?: string): Entry => ({
    type: "message",
    role: "assistant",
    content,
    ts,
    ...(model ? { model } : {}),
});

afterEach(() => {
    while (dirs.length) rmSync(dirs.pop()!, { recursive: true, force: true });
});

describe("Session tree: linear appends", () => {
    test("append advances the leaf; getBranch is root→leaf", async () => {
        const s = mk();
        const a = user("a", 1);
        const b = asst("b", 2);
        await s.append(a);
        await s.append(b);
        expect(s.getLeafId()).toBe(b.id!);
        expect(s.getBranch().map((e) => e.id)).toEqual([a.id, b.id]);
        expect(b.parentId).toBe(a.id!);
        expect(a.parentId).toBeNull();
    });

    test("appends persist and reload from disk identically", async () => {
        const s = mk();
        const a = user("hello", 1);
        const b = asst("world", 2);
        await s.append(a);
        await s.append(b);
        const reloaded = Session.load(s.path, s.info);
        expect(reloaded.getBranch().map((e) => e.id)).toEqual([a.id, b.id]);
        expect(reloaded.getLeafId()).toBe(b.id!);
    });
});

describe("Session tree: branching", () => {
    test("branch() forks a new path; the abandoned branch stays in entries() but leaves the model context", async () => {
        const s = mk();
        const a = user("q", 1);
        const b = asst("first answer", 2);
        await s.append(a);
        await s.append(b);
        s.branch(a.id!); // rewind to the user message
        const c = asst("alternate answer", 3);
        await s.append(c);

        // current branch is a → c; b is abandoned
        expect(s.getBranch().map((e) => e.id)).toEqual([a.id, c.id]);
        expect(s.entries().map((e) => e.id)).toEqual([a.id, b.id, c.id]);
        // messages() follows the live branch only
        expect(s.messages().map((m) => m.content)).toEqual(["q", "alternate answer"]);
    });

    test("getTree nests both children under the shared parent, oldest first", async () => {
        const s = mk();
        const a = user("q", 1);
        const b = asst("b", 2);
        await s.append(a);
        await s.append(b);
        s.branch(a.id!);
        const c = asst("c", 3);
        await s.append(c);

        const roots = s.getTree();
        expect(roots).toHaveLength(1);
        expect(roots[0].entry.id).toBe(a.id);
        expect(roots[0].children.map((n) => n.entry.id)).toEqual([b.id, c.id]);
    });

    test("resetLeaf() makes the next append a new root", async () => {
        const s = mk();
        const a = user("a", 1);
        await s.append(a);
        s.resetLeaf();
        const b = user("b", 2);
        await s.append(b);
        expect(b.parentId).toBeNull();
        expect(
            s
                .getTree()
                .map((r) => r.entry.id)
                .sort(),
        ).toEqual([a.id, b.id].sort());
    });

    test("branchWithSummary rewinds and records a branch-summary entry", async () => {
        const s = mk();
        const a = user("a", 1);
        const b = asst("b", 2);
        await s.append(a);
        await s.append(b);
        const sumId = await s.branchWithSummary(a.id!, "abandoned the first attempt");
        expect(s.getLeafId()).toBe(sumId);
        const leaf = s.getEntry(sumId)!;
        expect(leaf.type).toBe("branch-summary");
        expect(leaf.parentId).toBe(a.id!);
    });
});

describe("Session tree: orphans", () => {
    test("an entry whose parent is missing surfaces as an extra root", () => {
        const orphan: Entry = { type: "message", role: "user", content: "lost", ts: 2, id: "x2", parentId: "ghost" };
        const root: Entry = { type: "message", role: "user", content: "root", ts: 1, id: "x1", parentId: null };
        const s = mk([root, orphan]);
        const roots = s.getTree();
        expect(roots.map((r) => r.entry.id).sort()).toEqual(["x1", "x2"]);
    });
});

describe("Session tree: legacy migration", () => {
    test("flat entries with no id/parentId get a linear chain on construct", () => {
        const legacy: Entry[] = [
            { type: "message", role: "user", content: "a", ts: 1 },
            { type: "message", role: "assistant", content: "b", ts: 2 },
            { type: "message", role: "user", content: "c", ts: 3 },
        ];
        const s = mk(legacy);
        const branch = s.getBranch();
        expect(branch).toHaveLength(3);
        // every entry now has an id, and each links to its predecessor
        expect(branch[0].parentId).toBeNull();
        expect(branch[1].parentId).toBe(branch[0].id!);
        expect(branch[2].parentId).toBe(branch[1].id!);
    });
});

describe("Session tree: labels", () => {
    test("set, overwrite (latest wins), and clear a label", async () => {
        const s = mk();
        const a = user("a", 1);
        await s.append(a);
        await s.appendLabelChange(a.id!, "important");
        expect(s.getLabel(a.id!)).toBe("important");
        await s.appendLabelChange(a.id!, "revised");
        expect(s.getLabel(a.id!)).toBe("revised");
        await s.appendLabelChange(a.id!, undefined);
        expect(s.getLabel(a.id!)).toBeUndefined();
    });

    test("getTree carries the resolved label onto the node", async () => {
        const s = mk();
        const a = user("a", 1);
        await s.append(a);
        await s.appendLabelChange(a.id!, "tag");
        const node = s.getTree().find((r) => r.entry.id === a.id)!;
        expect(node.label).toBe("tag");
    });
});

describe("Session tree: name, model, compaction", () => {
    test("setName / getName — latest non-empty wins, empty clears", async () => {
        const s = mk();
        expect(s.getName()).toBeUndefined();
        await s.setName("Refactor");
        expect(s.getName()).toBe("Refactor");
        await s.setName("");
        expect(s.getName()).toBeUndefined();
    });

    test("lastModel prefers a model-change, then the last assistant stamp, then the session default", async () => {
        const s = mk();
        expect(s.lastModel()).toBe("m0"); // session default
        await s.append(asst("hi", 1, "m1"));
        expect(s.lastModel()).toBe("m1");
        await s.append({ type: "model-change", from: "m1", to: "m2", ts: 2 });
        expect(s.lastModel()).toBe("m2");
    });

    test("lastCompactCutAt returns the max cut on the current branch", async () => {
        const s = mk();
        await s.append({ type: "compact", summary: "s1", cutAt: 3, ts: 1, tokensBefore: 10, tokensAfter: 4 });
        await s.append({ type: "compact", summary: "s2", cutAt: 7, ts: 2, tokensBefore: 20, tokensAfter: 5 });
        await s.append({ type: "compact", summary: "s3", cutAt: 5, ts: 3, tokensBefore: 30, tokensAfter: 6 });
        expect(s.lastCompactCutAt()).toBe(7);
    });

    test("getUserMessagesForForking collects user messages from every branch", async () => {
        const s = mk();
        const a = user("first", 1);
        await s.append(a);
        await s.append(asst("ans", 2));
        s.branch(a.id!);
        await s.append(user("second", 3));
        const forks = s.getUserMessagesForForking().map((f) => f.text);
        expect(forks).toEqual(["first", "second"]);
    });
});
