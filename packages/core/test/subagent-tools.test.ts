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
