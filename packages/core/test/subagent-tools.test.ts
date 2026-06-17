import { describe, expect, test } from "bun:test";
import { resolveSubagentTools } from "../src/agent/subagent";

const ALL = ["read", "write", "edit", "bash", "ls", "grep", "find"];
const READONLY = ["read", "ls", "grep", "find"];

describe("resolveSubagentTools — delegation never widens access (cap = parent's effective tools)", () => {
    test("read-only parent (plan) strips write/edit/bash even targeting an unrestricted agent", () => {
        const eff = resolveSubagentTools(ALL, undefined /* default = all */, READONLY /* plan's own tools */);
        expect(eff.sort()).toEqual([...READONLY].sort());
        expect(eff).not.toContain("write");
        expect(eff).not.toContain("edit");
        expect(eff).not.toContain("bash");
    });

    test("no cap, unrestricted target = all file tools", () => {
        expect(resolveSubagentTools(ALL, undefined, undefined).sort()).toEqual([...ALL].sort());
    });

    test("cap intersects with a restricted target (narrower wins)", () => {
        const eff = resolveSubagentTools(ALL, ["read", "grep"], ["read", "ls", "grep", "find"]);
        expect(eff.sort()).toEqual(["grep", "read"]);
    });

    test("task is always stripped — subagents never nest", () => {
        const eff = resolveSubagentTools(ALL, ["read", "task"], undefined);
        expect(eff).not.toContain("task");
        expect(eff).toEqual(["read"]);
    });

    test("cap wins even when target requests broader tools", () => {
        const eff = resolveSubagentTools(ALL, ["read", "write", "edit", "bash"], READONLY);
        expect(eff).toEqual(["read"]);
    });

    test("empty intersection yields no tools (fully sandboxed)", () => {
        expect(resolveSubagentTools(ALL, ["write"], READONLY)).toEqual([]);
    });
});

// MCP tool names join the universe (allTools) and the parent's cap when the
// parent turn exposed them. The same widen/narrow rule then governs whether a
// subagent inherits them — no MCP-specific branching.
describe("resolveSubagentTools — MCP inheritance follows the same cap rule", () => {
    const MCP = "mcp__search__query";
    const UNIVERSE = [...ALL, MCP];

    test("a fork inherits the parent's MCP tools (cap includes them)", () => {
        // Fork = no target restriction; parent (cap) had the MCP tool.
        const eff = resolveSubagentTools(UNIVERSE, undefined, [...ALL, MCP]);
        expect(eff).toContain(MCP);
    });

    test("a named agent that doesn't list the MCP tool drops it (narrow only)", () => {
        const eff = resolveSubagentTools(UNIVERSE, ["read", "grep"], [...ALL, MCP]);
        expect(eff).not.toContain(MCP);
        expect(eff.sort()).toEqual(["grep", "read"]);
    });

    test("a named agent cannot gain an MCP tool the parent never had (no widening)", () => {
        // Target lists the MCP tool, but the parent's cap doesn't include it.
        const eff = resolveSubagentTools(UNIVERSE, ["read", MCP], ALL);
        expect(eff).not.toContain(MCP);
        expect(eff).toEqual(["read"]);
    });
});
