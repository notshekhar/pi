import { beforeAll, describe, expect, test } from "bun:test";
import type { SessionTreeNode } from "@notshekhar/loop-core";
import {
    buildToolCallMap,
    getEntryDisplayText,
    getSearchableText,
    type TreeRenderContext,
} from "../src/interactive/ui/tree/entry-display";
import { initTheme } from "../src/interactive/ui/theme";

// getEntryDisplayText applies theme colors, which need the theme initialized.
beforeAll(() => initTheme("dark"));

const CWD = "/repo";
const strip = (s: string) => s.replace(/\x1b\[[0-9;]*m/g, "");

function node(entry: Record<string, unknown>, children: SessionTreeNode[] = []): SessionTreeNode {
    return { entry, children } as unknown as SessionTreeNode;
}

const asstToolCall = node({
    type: "message",
    role: "assistant",
    id: "a1",
    parentId: null,
    ts: 1,
    content: [{ type: "tool-call", toolCallId: "c1", toolName: "read", input: { path: "/repo/src/foo.ts" } }],
});
const toolResult = node({
    type: "message",
    role: "tool",
    id: "t1",
    parentId: "a1",
    ts: 2,
    content: [{ type: "tool-result", toolCallId: "c1", toolName: "read", output: "…file…" }],
});

function ctxFor(roots: SessionTreeNode[]): TreeRenderContext {
    return { toolCalls: buildToolCallMap(roots), cwd: CWD };
}

describe("buildToolCallMap", () => {
    test("indexes assistant tool-call blocks by toolCallId across the whole tree", () => {
        const map = buildToolCallMap([node({ ...asstToolCall.entry }, [toolResult])]);
        expect(map.get("c1")).toEqual({ toolName: "read", input: { path: "/repo/src/foo.ts" } });
    });

    test("ignores non-tool content and messages", () => {
        const map = buildToolCallMap([
            node({ type: "message", role: "assistant", content: [{ type: "text", text: "hi" }] }),
        ]);
        expect(map.size).toBe(0);
    });
});

describe("getEntryDisplayText — tool rows", () => {
    test("a tool-result row resolves to the call's name + args (not an empty [tool])", () => {
        const ctx = ctxFor([asstToolCall, toolResult]);
        expect(strip(getEntryDisplayText(toolResult, false, ctx))).toBe("read src/foo.ts");
    });

    test("a pure tool-call assistant turn shows the call, not '(no content)'", () => {
        const ctx = ctxFor([asstToolCall]);
        expect(strip(getEntryDisplayText(asstToolCall, false, ctx))).toBe("assistant: read src/foo.ts");
    });

    test("multiple tool results are comma-joined", () => {
        const asst = node({
            type: "message",
            role: "assistant",
            id: "a2",
            content: [
                { type: "tool-call", toolCallId: "x", toolName: "bash", input: { command: "git status" } },
                { type: "tool-call", toolCallId: "y", toolName: "ls", input: { path: "/repo/src" } },
            ],
        });
        const res = node({
            type: "message",
            role: "tool",
            id: "r2",
            content: [
                { type: "tool-result", toolCallId: "x" },
                { type: "tool-result", toolCallId: "y" },
            ],
        });
        const ctx = ctxFor([asst, res]);
        expect(strip(getEntryDisplayText(res, false, ctx))).toBe("bash git status, ls src");
    });

    test("falls back to the block's own toolName when the call isn't in the map", () => {
        const orphan = node({
            type: "message",
            role: "tool",
            id: "o1",
            content: [{ type: "tool-result", toolCallId: "missing", toolName: "grep" }],
        });
        const ctx = ctxFor([orphan]);
        expect(strip(getEntryDisplayText(orphan, false, ctx))).toBe("grep");
    });

    test("without a context, tool rows degrade gracefully (no crash)", () => {
        expect(() => getEntryDisplayText(toolResult, false)).not.toThrow();
    });
});

describe("getSearchableText — tool rows", () => {
    test("includes the resolved tool name + args so search matches them", () => {
        const ctx = ctxFor([asstToolCall, toolResult]);
        const text = getSearchableText(toolResult, ctx);
        expect(text).toContain("read");
        expect(text).toContain("src/foo.ts");
    });
});
