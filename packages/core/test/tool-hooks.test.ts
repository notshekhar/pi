import { describe, expect, test } from "bun:test";
import { attachHookFeedback } from "../src/agent/tool-hooks";

describe("attachHookFeedback — preserves any tool output shape", () => {
    test("array output (e.g. MCP content blocks) is not corrupted by a spread", () => {
        const output = [
            { type: "text", text: "a" },
            { type: "text", text: "b" },
        ];
        const result = attachHookFeedback(output, "note") as { result: unknown; hook_feedback: string };
        // The array must survive intact under `result`, not be spread into
        // {0:…,1:…} which would wreck it for persistence + replay.
        expect(result.result).toEqual(output);
        expect(Array.isArray(result.result)).toBe(true);
        expect(result.hook_feedback).toBe("note");
    });

    test("plain object output gets the feedback field merged in", () => {
        const result = attachHookFeedback({ ok: true }, "note") as Record<string, unknown>;
        expect(result).toEqual({ ok: true, hook_feedback: "note" });
    });

    test("primitive output is nested under result", () => {
        const result = attachHookFeedback("done", "note");
        expect(result).toEqual({ result: "done", hook_feedback: "note" });
    });
});
