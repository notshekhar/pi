import { describe, expect, test } from "bun:test";
import { toModelMessages } from "../src/agent/model-messages";
import { Session } from "../src/sessions";
import type { Entry } from "../src/types";
import { useTempSessionDb } from "./helpers/temp-db";

useTempSessionDb();

/**
 * Build a session from entries that have been through a JSON serialize/parse
 * cycle — the same lossy boundary a real session crosses when it's written to
 * its JSONL file and reloaded. This is what catches "the tool result was in
 * context the turn it ran, but gone the next turn": the next turn rebuilds
 * context purely from the reloaded transcript.
 */
function reloadedSession(entries: Entry[]): Session {
    const roundTripped = entries.map((e) => JSON.parse(JSON.stringify(e)) as Entry);
    return new Session(
        { id: "t", createdAt: 0, cwd: "/tmp", provider: "anthropic", model: "anthropic/claude" },
        "/tmp/fake.jsonl",
        roundTripped,
    );
}

const ts = 0;

describe("toModelMessages — tool results survive a session round-trip", () => {
    test("a tool-call + tool-result pair re-enters context intact", () => {
        const toolCallId = "call-1";
        const entries: Entry[] = [
            { type: "message", role: "user", content: "use the tool", ts },
            {
                type: "message",
                role: "assistant",
                content: [{ type: "tool-call", toolCallId, toolName: "mcp__mock__echo", input: { text: "hi" } }],
                ts,
            },
            {
                type: "message",
                role: "tool",
                // MCP tool output is structured content (an array) — the exact
                // shape the previous implementation risked dropping on replay.
                content: [
                    {
                        type: "tool-result",
                        toolCallId,
                        toolName: "mcp__mock__echo",
                        output: { type: "content", value: [{ type: "text", text: "echo: hi" }] },
                    },
                ],
                ts,
            },
            { type: "message", role: "user", content: "thanks", ts },
        ];

        const msgs = toModelMessages(reloadedSession(entries));

        const toolMsg = msgs.find((m) => m.role === "tool");
        expect(toolMsg).toBeDefined();
        expect(JSON.stringify(toolMsg)).toContain("echo: hi");
        // The tool result is not silently dropped, and ordering is preserved so
        // the result still pairs with its call.
        expect(msgs.map((m) => m.role)).toEqual(["user", "assistant", "tool", "user"]);
    });
});
