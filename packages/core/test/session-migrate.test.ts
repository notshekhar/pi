import { mkdirSync, mkdtempSync, rmSync, utimesSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { getDb, migrateLegacySessions, SessionManager } from "../src/sessions";
import { useTempSessionDb } from "./helpers/temp-db";

useTempSessionDb();

let root: string;
beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "loop-migrate-"));
});
afterEach(() => {
    rmSync(root, { recursive: true, force: true });
});

/** Write a fixture transcript under root/<slug>/<id>.jsonl with a fixed mtime. */
function writeTranscript(slug: string, id: string, lines: string[], mtimeMs = Date.now()): string {
    const dir = join(root, slug);
    mkdirSync(dir, { recursive: true });
    const path = join(dir, `${id}.jsonl`);
    writeFileSync(path, lines.join("\n") + (lines.length ? "\n" : ""));
    utimesSync(path, mtimeMs / 1000, mtimeMs / 1000);
    return path;
}

const sessionInfo = (id: string, cwd: string) =>
    JSON.stringify({
        type: "session-info",
        ts: 1,
        id,
        parentId: null,
        createdAt: 1,
        cwd,
        provider: "anthropic",
        model: "m0",
    });

describe("JSONL → SQLite migration", () => {
    test("a native transcript migrates whole: identity, branch, name, usage, mtime ordering", () => {
        const MTIME = Date.parse("2026-01-02T03:04:05Z");
        writeTranscript(
            "--proj--",
            "S1",
            [
                sessionInfo("S1", "/proj"),
                JSON.stringify({
                    type: "message",
                    role: "user",
                    content: "hello",
                    ts: 2,
                    id: "aaaaaaaa",
                    parentId: "S1",
                }),
                JSON.stringify({
                    type: "message",
                    role: "assistant",
                    content: "world",
                    ts: 3,
                    id: "bbbbbbbb",
                    parentId: "aaaaaaaa",
                    model: "m0",
                    usage: { inputTokens: 100, outputTokens: 40, totalTokens: 140 },
                }),
                JSON.stringify({ type: "session-name", name: "Migrated", ts: 4, id: "cccccccc", parentId: "bbbbbbbb" }),
            ],
            MTIME,
        );

        migrateLegacySessions(getDb(), root);

        const mgr = new SessionManager();
        const listed = mgr.list("/proj");
        expect(listed).toHaveLength(1);
        expect(listed[0].id).toBe("S1");
        expect(listed[0].name).toBe("Migrated");
        expect(listed[0].firstUserMessage).toBe("hello");
        expect(listed[0].mtime).toBe(MTIME);

        return mgr.open("S1").then((s) => {
            expect(s.getBranch().map((e) => e.id)).toEqual(["S1", "aaaaaaaa", "bbbbbbbb", "cccccccc"]);
            expect(s.getName()).toBe("Migrated");
            // Derived usage columns feed /steak.
            const day = new Date(3).toLocaleDateString("sv");
            expect(mgr.dailyTokens().get(day)).toBe(140);
        });
    });

    test("legacy flat entries (no ids) get a linear chain", async () => {
        writeTranscript("--old--", "FLAT", [
            JSON.stringify({ type: "message", role: "user", content: "a", ts: 1 }),
            JSON.stringify({ type: "message", role: "assistant", content: "b", ts: 2 }),
        ]);
        migrateLegacySessions(getDb(), root);
        const s = await new SessionManager().open("FLAT");
        const branch = s.getBranch();
        expect(branch.map((e: any) => e.content)).toEqual(["a", "b"]);
        expect(branch[0].parentId).toBeNull();
        expect(branch[1].parentId).toBe(branch[0].id!);
    });

    test("a torn tail line is skipped; the rest of the session survives", async () => {
        writeTranscript("--torn--", "TORN", [
            sessionInfo("TORN", "/torn"),
            JSON.stringify({ type: "message", role: "user", content: "kept", ts: 2, id: "aaaaaaaa", parentId: "TORN" }),
            '{"type":"message","ro', // crash mid-append
        ]);
        migrateLegacySessions(getDb(), root);
        const s = await new SessionManager().open("TORN");
        expect(s.entries().map((e: any) => e.content ?? e.type)).toEqual(["session-info", "kept"]);
    });

    test("an empty file becomes an empty session under its filename id", () => {
        writeTranscript("--empty--", "EMPTY", []);
        migrateLegacySessions(getDb(), root);
        const listed = new SessionManager().list();
        expect(listed.map((s) => s.id)).toEqual(["EMPTY"]);
    });

    test("duplicate session ids across files merge instead of failing", async () => {
        writeTranscript("--a--", "DUP", [
            sessionInfo("DUP", "/a"),
            JSON.stringify({ type: "message", role: "user", content: "one", ts: 2, id: "aaaaaaaa", parentId: "DUP" }),
        ]);
        writeTranscript("--b--", "DUP", [
            sessionInfo("DUP", "/a"),
            JSON.stringify({ type: "message", role: "user", content: "two", ts: 3, id: "bbbbbbbb", parentId: "DUP" }),
        ]);
        migrateLegacySessions(getDb(), root);
        const mgr = new SessionManager();
        expect(mgr.list().filter((s) => s.id === "DUP")).toHaveLength(1);
        const s = await mgr.open("DUP");
        expect(s.entries().filter((e) => e.type === "message")).toHaveLength(2);
    });

    test("re-running after a crash is idempotent (nothing double-imported)", async () => {
        writeTranscript("--proj--", "AGAIN", [
            sessionInfo("AGAIN", "/proj"),
            JSON.stringify({ type: "message", role: "user", content: "x", ts: 2, id: "aaaaaaaa", parentId: "AGAIN" }),
        ]);
        const db = getDb();
        migrateLegacySessions(db, root);
        // Simulate a crash before migrated_at landed, then a full re-run.
        db.run("DELETE FROM meta WHERE key = 'migrated_at'");
        migrateLegacySessions(db, root);
        const s = await new SessionManager().open("AGAIN");
        expect(s.entries()).toHaveLength(2);
        // And with migrated_at set, the walk itself is skipped outright.
        migrateLegacySessions(db, root);
        expect(s.entries()).toHaveLength(2);
    });

    test("migrated_at gates the walk: files added later are NOT auto-migrated…", () => {
        migrateLegacySessions(getDb(), root); // empty root — marks done
        writeTranscript("--late--", "LATE", [sessionInfo("LATE", "/late")]);
        migrateLegacySessions(getDb(), root);
        expect(new SessionManager().list()).toHaveLength(0);
    });

    test("…but import-on-open picks up a straggler transcript by direct path", async () => {
        migrateLegacySessions(getDb(), root);
        const path = writeTranscript("--late--", "LATE", [
            sessionInfo("LATE", "/late"),
            JSON.stringify({
                type: "message",
                role: "user",
                content: "restored",
                ts: 2,
                id: "aaaaaaaa",
                parentId: "LATE",
            }),
        ]);
        const s = await new SessionManager().open(path);
        expect(s.id).toBe("LATE");
        expect(s.getBranch().some((e: any) => e.content === "restored")).toBe(true);
        // Now in the DB: opening by id works without the file.
        rmSync(path);
        const again = await new SessionManager().open("LATE");
        expect(again.getBranch().some((e: any) => e.content === "restored")).toBe(true);
    });

    test("/export parity: an exported transcript re-imports byte-equal entries", async () => {
        const mgr = new SessionManager();
        const src = await mgr.create({ cwd: "/proj", provider: "anthropic", model: "m0" });
        await src.append({ type: "message", role: "user", content: "q", ts: 1 });
        await src.append({ type: "message", role: "assistant", content: "a", ts: 2 });
        // /export writes entries as JSONL (see cli exportSession).
        const exported = src.entries().map((e) => JSON.stringify(e));
        const path = writeTranscript("--x--", src.id, exported);

        // Fresh store (same file, different session db) — import via open(path).
        getDb().run("DELETE FROM sessions");
        const round = await mgr.open(path);
        expect(round.entries()).toEqual(src.entries());
    });
});
