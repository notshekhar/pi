import { describe, expect, test } from "bun:test";
import { compactedContextEntries } from "../src/agent/compact";
import { Session } from "../src/sessions";
import type { Entry } from "../src/types";

function fakeSession(entries: Entry[]): Session {
    return new Session(
        { id: "t", createdAt: 0, cwd: "/tmp", provider: "xai", model: "xai/grok-build-0.1" },
        "/tmp/fake.jsonl",
        entries,
    );
}

const msg = (role: "user" | "assistant", content: string): Entry => ({ type: "message", role, content, ts: 0 });
const sub = (agent: string, result: string): Entry => ({ type: "subagent", ts: 0, agent, prompt: "p", result });

describe("compactedContextEntries", () => {
    test("no compact: messages and subagents interleave in order", () => {
        const out = compactedContextEntries(
            fakeSession([msg("user", "a"), sub("plan", "report"), msg("assistant", "b")]),
        );
        expect(out.map((e) => e.kind)).toEqual(["message", "subagent", "message"]);
    });

    test("compact cut drops earlier messages and their subagents", () => {
        const out = compactedContextEntries(
            fakeSession([
                msg("user", "old-1"),
                sub("plan", "old-report"),
                msg("assistant", "old-2"),
                { type: "compact", summary: "SUMMARY", cutAt: 2, ts: 0, tokensBefore: 0, tokensAfter: 0 },
                msg("user", "new-1"),
                sub("default", "new-report"),
            ]),
        );
        // summary message + surviving entries
        expect(out[0]).toMatchObject({ kind: "message", role: "user" });
        expect(String((out[0] as { content: unknown }).content)).toContain("SUMMARY");
        const rest = out.slice(1);
        expect(rest).toEqual([
            { kind: "message", role: "user", content: "new-1" },
            { kind: "subagent", agent: "default", result: "new-report" },
        ]);
    });
});
