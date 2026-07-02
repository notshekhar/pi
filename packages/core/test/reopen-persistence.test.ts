import { EventEmitter } from "node:events";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, mock, test } from "bun:test";
import { MockLanguageModelV3 } from "ai/test";
import { Session } from "../src/sessions";
import { useTempSessionDb } from "./helpers/temp-db";

useTempSessionDb();

const MODEL = "anthropic/claude-sonnet-4-6";

function streamOf(events: any[]) {
    let i = 0;
    return {
        stream: new ReadableStream({
            async pull(controller) {
                await new Promise((r) => setTimeout(r, 2));
                if (i < events.length) controller.enqueue(events[i++]);
                else controller.close();
            },
        }),
    };
}

function mkSession(dir: string) {
    const info = { id: "t", createdAt: 0, cwd: dir, provider: "anthropic" as const, model: MODEL };
    return { info, session: new Session(info, join(dir, "s.jsonl"), []) };
}

describe("turns survive being reopened from disk", () => {
    afterEach(() => mock.restore());

    test("a multi-step (tool-call) turn keeps its final answer after reload", async () => {
        const dir = mkdtempSync(join(tmpdir(), "loop-reopen-ms-"));
        const { info, session } = mkSession(dir);

        // Step 1 calls a tool; step 2 writes the final answer. Before the fix,
        // step 2's messages were sliced away and never persisted.
        const FINAL = "FINAL ANSWER: the detailed multi-paragraph explanation";
        let call = 0;
        const model = new MockLanguageModelV3({
            doStream: async () => {
                call++;
                if (call === 1) {
                    return streamOf([
                        { type: "tool-call", toolCallId: "c1", toolName: "ls", input: JSON.stringify({}) },
                        {
                            type: "finish",
                            finishReason: "tool-calls",
                            usage: { inputTokens: 10, outputTokens: 5, totalTokens: 15 },
                        },
                    ]);
                }
                return streamOf([
                    { type: "text-start", id: "t1" },
                    ...FINAL.split("").map((c) => ({ type: "text-delta", id: "t1", delta: c })),
                    { type: "text-end", id: "t1" },
                    {
                        type: "finish",
                        finishReason: "stop",
                        usage: { inputTokens: 20, outputTokens: 30, totalTokens: 50 },
                    },
                ]);
            },
        });
        const realProviders = await import("../src/providers");
        mock.module("../src/providers", () => ({ ...realProviders, getModel: async () => model }));
        const { runTurn, CostTracker } = await import("../src/agent");

        await runTurn({
            session,
            modelId: MODEL,
            userInput: "tell me about the project",
            cwd: dir,
            tracker: new CostTracker(),
            emitter: new EventEmitter() as any,
        });

        expect(call).toBe(2);

        // Reload purely from disk (the reopen path) and rebuild model context.
        const reloaded = Session.load(join(dir, "s.jsonl"), info);
        const { toModelMessages } = await import("../src/agent/model-messages");
        const ctx = JSON.stringify(toModelMessages(reloaded));
        // The final assistant answer must round-trip back into the model context.
        expect(ctx).toContain("FINAL ANSWER");
        // And both steps' usage must persist (resume cost-seeding must be whole).
        const usages = (reloaded.entries() as any[]).filter(
            (e) => e.type === "message" && e.role === "assistant" && e.usage,
        );
        expect(usages.length).toBe(2);
        // The model stamp survives the reload (correct re-pricing on resume).
        expect(usages.every((e) => e.model === MODEL)).toBe(true);
    });

    test("an interrupted turn keeps its flag and surfaces the note after reload", async () => {
        const dir = mkdtempSync(join(tmpdir(), "loop-reopen-int-"));
        const { info, session } = mkSession(dir);

        const text = "Roses are red and violets are blue";
        const model = new MockLanguageModelV3({
            doStream: async () =>
                streamOf([
                    { type: "text-start", id: "t0" },
                    ...text.split("").map((c) => ({ type: "text-delta", id: "t0", delta: c })),
                    { type: "text-end", id: "t0" },
                    {
                        type: "finish",
                        finishReason: "stop",
                        usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
                    },
                ]),
        });
        const realProviders = await import("../src/providers");
        mock.module("../src/providers", () => ({ ...realProviders, getModel: async () => model }));
        const { runTurn, CostTracker } = await import("../src/agent");

        const abort = new AbortController();
        const em = new EventEmitter() as any;
        let seen = "";
        em.on("text-delta", (t: string) => {
            seen += t;
            if (seen.length >= 8) abort.abort();
        });
        await runTurn({
            session,
            modelId: MODEL,
            userInput: "write a poem",
            cwd: dir,
            abortSignal: abort.signal,
            tracker: new CostTracker(),
            emitter: em,
        });

        // Reopen from disk: the interrupted flag must survive adaptLoopEntry.
        const reloaded = Session.load(join(dir, "s.jsonl"), info);
        const entry = (reloaded.entries() as any[]).filter((e) => e.type === "message" && e.role === "assistant").pop();
        expect(entry?.interrupted).toBe(true);

        // And the next turn's context carries the interruption note.
        const { toModelMessages } = await import("../src/agent/model-messages");
        const ctx = JSON.stringify(toModelMessages(reloaded));
        expect(ctx).toContain("interrupted this response");

        // Resume cost-seeding shows the estimate and keeps the `~` marker.
        const tracker = new CostTracker();
        tracker.seedFromSession(reloaded);
        expect(tracker.format().startsWith("~$")).toBe(true);
    });
});
