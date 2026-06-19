import { EventEmitter } from "node:events";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, mock, test } from "bun:test";
import { MockLanguageModelV3 } from "ai/test";
import { Session } from "../src/sessions";

// Streams reasoning deltas, then text deltas, slowly — like a reasoning model
// that "thinks" before writing the answer. A turn can be interrupted in either
// phase to check what survives.
function reasoningThenText(reasoning: string, text: string) {
    const events: any[] = [
        { type: "reasoning-start", id: "r0" },
        ...reasoning.split("").map((c) => ({ type: "reasoning-delta", id: "r0", delta: c })),
        { type: "reasoning-end", id: "r0" },
        { type: "text-start", id: "t0" },
        ...text.split("").map((c) => ({ type: "text-delta", id: "t0", delta: c })),
        { type: "text-end", id: "t0" },
        { type: "finish", finishReason: "stop", usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } },
    ];
    let i = 0;
    const stream = new ReadableStream({
        async pull(controller) {
            await new Promise((r) => setTimeout(r, 4));
            if (i < events.length) controller.enqueue(events[i++]);
            else controller.close();
        },
    });
    return { stream };
}

function mkSession(dir: string, modelId: string) {
    return new Session({ id: "t", createdAt: 0, cwd: dir, provider: "xai", model: modelId }, join(dir, "s.jsonl"), []);
}

const MODEL = "xai/grok-build-0.1";

describe("partial output is persisted when a turn is aborted mid-stream", () => {
    let dir: string;
    beforeEach(() => {
        dir = mkdtempSync(join(tmpdir(), "loop-abort-"));
    });
    afterEach(() => mock.restore());

    async function runUntilAborted(opts: {
        reasoning: string;
        text: string;
        abortOn: "reasoning" | "text";
        afterChars: number;
    }) {
        const model = new MockLanguageModelV3({
            doStream: async () => reasoningThenText(opts.reasoning, opts.text),
        });
        const realProviders = await import("../src/providers");
        mock.module("../src/providers", () => ({ ...realProviders, getModel: async () => model }));
        const { runTurn, CostTracker } = await import("../src/agent");

        const session = mkSession(dir, MODEL);
        const abort = new AbortController();
        const em = new EventEmitter() as any;
        let seen = "";
        const evt = opts.abortOn === "reasoning" ? "reasoning-delta" : "text-delta";
        em.on(evt, (t: string) => {
            seen += t;
            if (seen.length >= opts.afterChars) abort.abort();
        });
        await runTurn({
            session,
            modelId: MODEL,
            userInput: "write me a 1000 line poem",
            cwd: dir,
            abortSignal: abort.signal,
            tracker: new CostTracker(),
            emitter: em,
        });
        const assistant = session.entries().find((e: any) => e.type === "message" && e.role === "assistant") as any;
        return { assistant, seen };
    }

    test("aborting during the thinking phase persists the partial reasoning", async () => {
        const { assistant, seen } = await runUntilAborted({
            reasoning: "Let me think about the poem's structure carefully",
            text: "Roses are red",
            abortOn: "reasoning",
            afterChars: 6,
        });
        expect(assistant).toBeDefined();
        const parts = assistant.content as Array<{ type: string; text: string }>;
        const reasoning = parts.find((p) => p.type === "reasoning");
        expect(reasoning).toBeDefined();
        expect(reasoning!.text).toBe(seen);
        // No text streamed yet, so there's no text part.
        expect(parts.some((p) => p.type === "text")).toBe(false);
    });

    test("aborting during the answer keeps both the reasoning and the partial text", async () => {
        const { assistant } = await runUntilAborted({
            reasoning: "Short think",
            text: "Roses are red and violets are blue",
            abortOn: "text",
            afterChars: 8,
        });
        expect(assistant).toBeDefined();
        const parts = assistant.content as Array<{ type: string; text: string }>;
        const reasoning = parts.find((p) => p.type === "reasoning");
        const text = parts.find((p) => p.type === "text");
        expect(reasoning?.text).toBe("Short think");
        expect(text).toBeDefined();
        expect(text!.text.length).toBeGreaterThan(0);
        expect("Roses are red and violets are blue".startsWith(text!.text)).toBe(true);
    });
});
