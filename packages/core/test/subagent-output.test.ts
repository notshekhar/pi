import { describe, expect, test } from "bun:test";
import { taskToolModelOutput, type SubagentOutput } from "../src/agent/subagent";
import { sumUsage } from "../src/agent/cost";
import type { UsageBlock } from "../src/types";

const out = (report: string): SubagentOutput => ({ history: [{ type: "text", text: report }], report });

describe("taskToolModelOutput — what the parent model reads", () => {
    test("SDK options-object call shape delivers the report (regression)", () => {
        // The AI SDK invokes toModelOutput({ toolCallId, input, output }) — the
        // report must be read from options.output, not from the wrapper.
        const r = taskToolModelOutput({
            toolCallId: "task-1",
            input: { prompt: "explore" },
            output: out("## Report\neverything works"),
        } as { output: unknown });
        expect(r).toEqual({ type: "text", value: "## Report\neverything works" });
    });

    test("empty report falls back to a non-empty placeholder", () => {
        const r = taskToolModelOutput({ output: out("") });
        expect(r.value).toBe("(subagent finished without a final response)");
    });

    test("oversized report is bounded with a truncation note", () => {
        const r = taskToolModelOutput({ output: out("x".repeat(30_000)) });
        expect(r.value.length).toBeLessThan(30_000);
        expect(r.value).toContain("truncated");
    });

    test("hook feedback on the output object stays visible to the parent", () => {
        const o = { ...out("done"), hook_feedback: "BLOCKED: nope" };
        const r = taskToolModelOutput({ output: o });
        expect(r.value).toContain("done");
        expect(r.value).toContain("BLOCKED: nope");
    });
});

describe("sumUsage — abort-safe per-step accumulation", () => {
    const u = (i: number, o: number, cr?: number): UsageBlock => ({
        inputTokens: i,
        outputTokens: o,
        totalTokens: i + o,
        ...(cr !== undefined ? { inputTokenDetails: { cacheReadTokens: cr } } : {}),
    });

    test("undefined seed copies the block", () => {
        expect(sumUsage(undefined, u(100, 10))).toMatchObject({ inputTokens: 100, outputTokens: 10 });
    });

    test("sums tokens and cache details across steps", () => {
        const total = sumUsage(sumUsage(undefined, u(100, 10, 80)), u(200, 20, 150));
        expect(total.inputTokens).toBe(300);
        expect(total.outputTokens).toBe(30);
        expect(total.totalTokens).toBe(330);
        expect(total.inputTokenDetails?.cacheReadTokens).toBe(230);
    });

    test("absent fields stay undefined rather than becoming 0", () => {
        const total = sumUsage(sumUsage(undefined, u(1, 1)), u(2, 2));
        expect(total.reasoningTokens).toBeUndefined();
        expect(total.cost).toBeUndefined();
    });
});
