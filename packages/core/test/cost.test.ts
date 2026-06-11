import { describe, expect, test } from "bun:test";
import { CostTracker } from "../src/agent/cost";
import { Session } from "../src/sessions";
import type { Entry, UsageBlock } from "../src/types";

const usage = (input: number, output: number, total?: number): UsageBlock => ({
    inputTokens: input,
    outputTokens: output,
    totalTokens: total ?? input + output,
});

describe("CostTracker.seedFromEntries", () => {
    test("sums token usage and reports last turn ctx", () => {
        const t = new CostTracker();
        const { ctxTokens } = t.seedFromEntries("xai/grok-build-0.1", [usage(100, 20), usage(300, 50, 420)]);
        const s = t.sessionBreakdown();
        expect(s.inputTokens).toBe(400);
        expect(s.outputTokens).toBe(70);
        expect(ctxTokens).toBe(420);
    });

    test("empty transcript seeds zeros", () => {
        const t = new CostTracker();
        const { ctxTokens } = t.seedFromEntries("xai/grok-build-0.1", []);
        expect(ctxTokens).toBe(0);
        expect(t.sessionBreakdown().usd).toBe(0);
    });
});

describe("CostTracker.seedFromSession", () => {
    test("includes assistant turns and subagent runs, skips others", () => {
        const entries: Entry[] = [
            { type: "message", role: "user", content: "hi", ts: 0 },
            { type: "message", role: "assistant", content: "yo", ts: 0, usage: usage(10, 5) },
            { type: "subagent", ts: 0, agent: "plan", prompt: "p", result: "r", usage: usage(200, 30) },
            { type: "message", role: "assistant", content: "done", ts: 0, usage: usage(40, 8) },
        ];
        const session = new Session(
            { id: "t", createdAt: 0, cwd: "/tmp", provider: "xai", model: "xai/grok-build-0.1" },
            "/tmp/fake.jsonl",
            entries,
        );
        const t = new CostTracker();
        const { ctxTokens } = t.seedFromSession(session);
        const s = t.sessionBreakdown();
        expect(s.inputTokens).toBe(250);
        expect(s.outputTokens).toBe(43);
        expect(ctxTokens).toBe(48); // last assistant turn
    });
});
