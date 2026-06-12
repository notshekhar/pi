import { describe, expect, test } from "bun:test";
import type { ModelMessage } from "ai";
import { anthropicCachedSystem, moveAnthropicCacheTail, withAnthropicCaching } from "../src/agent/model-messages";

const hasAnchor = (m: ModelMessage) =>
    Boolean((m.providerOptions as { anthropic?: { cacheControl?: unknown } } | undefined)?.anthropic?.cacheControl);

describe("withAnthropicCaching", () => {
    test("anchors system and last message", () => {
        const out = withAnthropicCaching("sys", [{ role: "user", content: "hi" }, { role: "user", content: "again" }]);
        expect(out[0].role).toBe("system");
        expect(hasAnchor(out[0])).toBe(true);
        expect(hasAnchor(out[out.length - 1])).toBe(true);
        expect(hasAnchor(out[1])).toBe(false);
    });
});

describe("moveAnthropicCacheTail", () => {
    test("moves the tail anchor as the loop grows — never accumulates breakpoints", () => {
        // Step 1: system + anchored user prompt (what the agent starts with).
        let messages: ModelMessage[] = [
            anthropicCachedSystem("sys"),
            { role: "user", content: "do the task", providerOptions: { anthropic: { cacheControl: { type: "ephemeral" } } } },
        ];
        // Simulate 10 loop steps, each appending an assistant + tool message.
        for (let step = 0; step < 10; step++) {
            messages = moveAnthropicCacheTail(messages);
            const anchored = messages.filter(hasAnchor);
            // Exactly 2 anchors per request: system + current last message.
            expect(anchored.length).toBe(2);
            expect(hasAnchor(messages[0])).toBe(true);
            expect(hasAnchor(messages[messages.length - 1])).toBe(true);
            messages = [
                ...messages,
                { role: "assistant", content: [{ type: "tool-call", toolCallId: `c${step}`, toolName: "read", input: {} }] },
                { role: "tool", content: [{ type: "tool-result", toolCallId: `c${step}`, toolName: "read", output: { type: "text", value: "x" } }] },
            ];
        }
    });

    test("keeps non-cache providerOptions on stripped messages", () => {
        const messages: ModelMessage[] = [
            anthropicCachedSystem("sys"),
            {
                role: "user",
                content: "hi",
                providerOptions: { anthropic: { cacheControl: { type: "ephemeral" } }, openrouter: { foo: 1 } },
            },
            { role: "user", content: "tail" },
        ];
        const out = moveAnthropicCacheTail(messages);
        expect(hasAnchor(out[1])).toBe(false);
        expect((out[1].providerOptions as { openrouter?: unknown }).openrouter).toEqual({ foo: 1 });
        expect(hasAnchor(out[2])).toBe(true);
    });

    test("never anchors a trailing system message and handles empty input", () => {
        expect(moveAnthropicCacheTail([])).toEqual([]);
        const out = moveAnthropicCacheTail([anthropicCachedSystem("sys")]);
        expect(out.length).toBe(1);
        expect(hasAnchor(out[0])).toBe(true); // system anchor untouched, no tail added
    });
});
