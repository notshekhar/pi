import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, test } from "bun:test";
import {
    addProjectServer,
    getProjectServers,
    projectServersPath,
    removeProjectServer,
    setProjectServerEnabled,
    type McpServerConfig,
} from "../src/mcp";

// Project scope writes <cwd>/.loop/mcp.json, so a temp cwd is fully hermetic —
// no HOME / getLoopDir involved.
const dirs: string[] = [];
function cwd() {
    const d = mkdtempSync(join(tmpdir(), "loop-mcpcfg-"));
    dirs.push(d);
    return d;
}
afterEach(() => {
    while (dirs.length) rmSync(dirs.pop()!, { recursive: true, force: true });
});

const http: McpServerConfig = { type: "http", url: "https://mcp.example.com/mcp" };
const stdio: McpServerConfig = { command: "npx", args: ["-y", "pkg"] };

describe("project-scope MCP config", () => {
    test("add writes canonical { mcpServers } JSON and reads back", () => {
        const dir = cwd();
        addProjectServer(dir, "figma", http);
        expect(existsSync(projectServersPath(dir))).toBe(true);
        const onDisk = JSON.parse(readFileSync(projectServersPath(dir), "utf8"));
        expect(onDisk).toEqual({ mcpServers: { figma: http } });
        expect(getProjectServers(dir).figma).toEqual(http);
    });

    test("add merges rather than clobbering existing servers", () => {
        const dir = cwd();
        addProjectServer(dir, "a", http);
        addProjectServer(dir, "b", stdio);
        expect(Object.keys(getProjectServers(dir)).sort()).toEqual(["a", "b"]);
    });

    test("add replaces a server of the same name", () => {
        const dir = cwd();
        addProjectServer(dir, "a", http);
        addProjectServer(dir, "a", stdio);
        expect(getProjectServers(dir).a).toEqual(stdio);
    });

    test("setEnabled flips the flag; returns false for unknown", () => {
        const dir = cwd();
        addProjectServer(dir, "a", http);
        expect(setProjectServerEnabled(dir, "a", false)).toBe(true);
        expect(getProjectServers(dir).a.enabled).toBe(false);
        expect(setProjectServerEnabled(dir, "ghost", false)).toBe(false);
    });

    test("remove deletes the entry; returns false for unknown", () => {
        const dir = cwd();
        addProjectServer(dir, "a", http);
        expect(removeProjectServer(dir, "a")).toBe(true);
        expect(getProjectServers(dir).a).toBeUndefined();
        expect(removeProjectServer(dir, "a")).toBe(false);
    });

    test("getProjectServers accepts a bare map (no mcpServers wrapper) too", () => {
        const dir = cwd();
        // Simulate a hand-written bare-map file.
        addProjectServer(dir, "a", http); // creates .loop dir + file
        writeFileSync(projectServersPath(dir), JSON.stringify({ a: http }));
        expect(getProjectServers(dir).a).toEqual(http);
    });

    test("a malformed file is ignored rather than throwing", () => {
        const dir = cwd();
        addProjectServer(dir, "a", http);
        writeFileSync(projectServersPath(dir), "{ not json");
        expect(getProjectServers(dir)).toEqual({});
    });
});
