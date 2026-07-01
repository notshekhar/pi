import { describe, expect, test } from "bun:test";
import { adaptLoopEntry } from "../src/sessions/loop-adapter";

describe("adaptLoopEntry", () => {
    test("returns null for non-objects", () => {
        expect(adaptLoopEntry(null)).toBeNull();
        expect(adaptLoopEntry("nope")).toBeNull();
        expect(adaptLoopEntry(42)).toBeNull();
    });

    test("session-info: fills defaults and keeps id as both session and tree id", () => {
        const e = adaptLoopEntry({ type: "session-info", id: "sid", cwd: "/x", model: "m", ts: 5 }) as any;
        expect(e.type).toBe("session-info");
        expect(e.id).toBe("sid");
        expect(e.provider).toBe("xai"); // default
        expect(e.createdAt).toBe(5); // falls back to ts
    });

    test("nested reference message shape is flattened; toolResult role maps to tool", () => {
        const e = adaptLoopEntry({
            type: "message",
            id: "m1",
            parentId: "p0",
            message: { role: "toolResult", content: "out" },
            ts: 1,
        }) as any;
        expect(e.type).toBe("message");
        expect(e.role).toBe("tool");
        expect(e.content).toBe("out");
        expect(e.id).toBe("m1");
        expect(e.parentId).toBe("p0");
    });

    test("assistant message keeps its per-message model + interrupted flags", () => {
        const e = adaptLoopEntry({
            type: "message",
            role: "assistant",
            content: "hi",
            model: "m9",
            interrupted: true,
        }) as any;
        expect(e.role).toBe("assistant");
        expect(e.model).toBe("m9");
        expect(e.interrupted).toBe(true);
    });

    test("unknown roles default to user; absent model/interrupted are omitted", () => {
        const e = adaptLoopEntry({ type: "message", role: "system", content: "c" }) as any;
        expect(e.role).toBe("user");
        expect("model" in e).toBe(false);
        expect("interrupted" in e).toBe(false);
    });

    test("subagent: string activity is upgraded to a text part", () => {
        const e = adaptLoopEntry({
            type: "subagent",
            agent: "explore",
            prompt: "p",
            result: "r",
            activity: "did stuff",
        }) as any;
        expect(e.activity).toEqual([{ type: "text", text: "did stuff" }]);
    });

    test("subagent: malformed activity parts are dropped", () => {
        const e = adaptLoopEntry({
            type: "subagent",
            activity: [{ type: "text", text: "ok" }, { type: "text" }, { junk: true }],
        }) as any;
        expect(e.activity).toEqual([{ type: "text", text: "ok" }]);
    });

    test("legacy model_change maps to model-change with modelId as `to`", () => {
        const e = adaptLoopEntry({ type: "model_change", modelId: "grok-4" }) as any;
        expect(e.type).toBe("model-change");
        expect(e.to).toBe("grok-4");
    });

    test("legacy branch_summary and session_info map to canonical types", () => {
        expect((adaptLoopEntry({ type: "branch_summary", summary: "s" }) as any).type).toBe("branch-summary");
        expect(adaptLoopEntry({ type: "session_info", name: "My chat" }) as any).toMatchObject({
            type: "session-name",
            name: "My chat",
        });
    });

    test("legacy flat shapes: user-prompt / assistant-message / role-only", () => {
        expect(adaptLoopEntry({ type: "user-prompt", text: "hey" }) as any).toMatchObject({
            type: "message",
            role: "user",
            content: "hey",
        });
        expect(adaptLoopEntry({ role: "assistant", content: "yo" }) as any).toMatchObject({
            type: "message",
            role: "assistant",
            content: "yo",
        });
    });

    test("timestamp field is honored when ts is absent", () => {
        const e = adaptLoopEntry({ type: "message", role: "user", content: "x", timestamp: 777 }) as any;
        expect(e.ts).toBe(777);
    });

    test("truly unknown shapes fall back to a custom entry carrying the payload", () => {
        const raw = { type: "mystery", foo: 1 };
        const e = adaptLoopEntry(raw) as any;
        expect(e.type).toBe("custom");
        expect(e.payload).toEqual(raw);
    });
});
