import { describe, expect, test } from "bun:test";
import { stringifyResult } from "../src/interactive/components/chat-history";

/**
 * The live tool-result event carries the raw MCP CallToolResult shape, while the
 * persisted entry carries the AI SDK toModelOutput shape. Both must render to
 * the same text so runtime and resume agree (the "two different values" bug).
 */
describe("stringifyResult — live (raw) and persisted (toModelOutput) shapes agree", () => {
    const text = '{\n  "result": ["a", "b"]\n}';

    test("raw MCP result with text content renders the text", () => {
        const raw = { content: [{ type: "text", text }], structuredContent: { result: ["a", "b"] }, isError: false };
        expect(stringifyResult(raw)).toBe(text);
    });

    test("persisted toModelOutput content shape renders the same text", () => {
        const persisted = { type: "content", value: [{ type: "text", text }] };
        expect(stringifyResult(persisted)).toBe(text);
    });

    test("raw result with empty content falls back to structuredContent", () => {
        const raw = { content: [], structuredContent: { result: [] }, isError: false };
        expect(stringifyResult(raw)).toBe('{\n  "result": []\n}');
    });
});
