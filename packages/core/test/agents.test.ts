import { describe, expect, test } from "bun:test";
import { isValidAgentName, parseAgentFile } from "../src/agent/agents";

describe("isValidAgentName", () => {
    test("accepts slash-command-safe names", () => {
        expect(isValidAgentName("reviewer")).toBe(true);
        expect(isValidAgentName("my-agent_2")).toBe(true);
        expect(isValidAgentName("A1")).toBe(true);
    });

    test("rejects unsafe names", () => {
        expect(isValidAgentName("")).toBe(false);
        expect(isValidAgentName("-leading-dash")).toBe(false);
        expect(isValidAgentName("has space")).toBe(false);
        expect(isValidAgentName("a".repeat(33))).toBe(false);
        expect(isValidAgentName("path/em")).toBe(false);
    });
});

describe("parseAgentFile", () => {
    test("plain body, no frontmatter", () => {
        expect(parseAgentFile("You are a reviewer.\n")).toEqual({ prompt: "You are a reviewer." });
    });

    test("frontmatter with tools subset", () => {
        const parsed = parseAgentFile("---\ntools: read, grep\n---\n\nReview only.\n");
        expect(parsed.prompt).toBe("Review only.");
        expect(parsed.tools).toEqual(["read", "grep"]);
    });

    test("unknown tools are dropped; empty result means all tools", () => {
        const parsed = parseAgentFile("---\ntools: hammer, saw\n---\n\nBody.");
        expect(parsed.tools).toBeUndefined();
    });

    test("full tool list (incl. task) normalizes to undefined (= all)", () => {
        const parsed = parseAgentFile("---\ntools: read, write, edit, bash, ls, grep, find, task\n---\n\nBody.");
        expect(parsed.tools).toBeUndefined();
    });

    test("file tools without task stays explicit (= no subagents)", () => {
        const parsed = parseAgentFile("---\ntools: read, write, edit, bash, ls, grep, find\n---\n\nBody.");
        expect(parsed.tools).toEqual(["read", "write", "edit", "bash", "ls", "grep", "find"]);
    });

    test("task is a valid agent tool", () => {
        const parsed = parseAgentFile("---\ntools: read, grep, task\n---\n\nBody.");
        expect(parsed.tools).toEqual(["read", "grep", "task"]);
    });

    test("subagent-tools cap parses, excludes task", () => {
        const parsed = parseAgentFile("---\ntools: read, task\nsubagent-tools: read, grep, task\n---\n\nBody.");
        expect(parsed.tools).toEqual(["read", "task"]);
        expect(parsed.subagentTools).toEqual(["read", "grep"]);
    });

    test("frontmatter without tools line keeps prompt", () => {
        const parsed = parseAgentFile("---\nname: x\n---\n\nThe prompt.");
        expect(parsed.prompt).toBe("The prompt.");
        expect(parsed.tools).toBeUndefined();
    });
});
