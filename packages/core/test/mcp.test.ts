import { describe, expect, test, afterEach, beforeEach } from "bun:test";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { resolveSecrets } from "../src/mcp/config";
import { connectServer, namespacedToolName, serverPrefix } from "../src/mcp/client";
import { McpManager } from "../src/mcp/manager";
import { getSetting, setSetting } from "../src/settings";
import type { McpServerConfig } from "../src/mcp/config";

const here = dirname(fileURLToPath(import.meta.url));
const SERVER = join(here, "fixtures", "mock-mcp-server.mjs");
const stdioConfig: McpServerConfig = { command: process.execPath, args: [SERVER] };

describe("namespacing", () => {
    test("namespacedToolName follows mcp__server__tool", () => {
        expect(namespacedToolName("fs", "read_file")).toBe("mcp__fs__read_file");
        expect(serverPrefix("fs")).toBe("mcp__fs__");
    });

    test("sanitizes unsafe characters in names", () => {
        expect(namespacedToolName("my server", "do.thing")).toBe("mcp__my_server__do_thing");
    });
});

describe("resolveSecrets", () => {
    test("substitutes ${env:VAR} from process.env", () => {
        process.env.LOOP_TEST_TOKEN = "secret123";
        expect(resolveSecrets("Bearer ${env:LOOP_TEST_TOKEN}")).toBe("Bearer secret123");
        delete process.env.LOOP_TEST_TOKEN;
    });

    test("unknown vars resolve to empty string", () => {
        expect(resolveSecrets("x=${env:LOOP_DEFINITELY_UNSET}")).toBe("x=");
    });
});

describe("connectServer (stdio, real MCP handshake)", () => {
    test("connects and namespaces the server's tools", async () => {
        const { client, tools, toolCount } = await connectServer("mock", stdioConfig);
        try {
            expect(toolCount).toBe(2);
            expect(Object.keys(tools).sort()).toEqual(["mcp__mock__echo", "mcp__mock__structured"]);
        } finally {
            await client.close();
        }
    });

    // Regression: a server that returns its payload only via structuredContent
    // (empty content array, like codespec) must not be lost — the AI SDK's
    // automatic-schema path drops structuredContent, so we surface it as text.
    test("structuredContent-only result is preserved as text content", async () => {
        const { client, tools } = await connectServer("mock", stdioConfig);
        try {
            const tool = tools["mcp__mock__structured"] as {
                execute: (i: unknown, o: unknown) => Promise<unknown>;
            };
            const result = (await tool.execute({ text: "hi" }, {})) as {
                content: Array<{ type: string; text: string }>;
            };
            const textPart = result.content.find((p) => p.type === "text");
            expect(textPart).toBeDefined();
            expect(textPart!.text).toContain('"echo": "hi"');
            expect(textPart!.text).toContain('"items"');
        } finally {
            await client.close();
        }
    });
});

describe("McpManager", () => {
    let manager: McpManager | undefined;
    // init() merges global settings — null them out so tests see only the
    // project mcp.json they create (and never hit the user's real servers).
    let savedGlobal: unknown;
    beforeEach(() => {
        savedGlobal = getSetting("mcpServers");
        setSetting("mcpServers", {});
    });
    afterEach(async () => {
        await manager?.close();
        manager = undefined;
        setSetting("mcpServers", savedGlobal as Record<string, McpServerConfig> | undefined);
    });

    test("init connects servers and aggregates tools; close tears down", async () => {
        manager = new McpManager();
        // Drive connect directly via a synthetic settings-free path: write a
        // project mcp.json the loader reads.
        const cwd = makeProjectWith({ mock: stdioConfig });
        await manager.init(cwd);

        const servers = manager.listServers();
        expect(servers).toHaveLength(1);
        expect(servers[0]).toMatchObject({ name: "mock", status: "ready", toolCount: 2 });
        expect(Object.keys(manager.getTools()).sort()).toEqual(["mcp__mock__echo", "mcp__mock__structured"]);

        await manager.close();
        expect(manager.listServers()).toHaveLength(0);
        expect(manager.getTools()).toEqual({});
    });

    test("a failing server is isolated as error, not thrown", async () => {
        manager = new McpManager();
        const cwd = makeProjectWith({
            mock: stdioConfig,
            broken: { command: "this-command-does-not-exist-pi", args: [] },
        });
        await manager.init(cwd);

        const byName = Object.fromEntries(manager.listServers().map((s) => [s.name, s]));
        expect(byName.mock.status).toBe("ready");
        expect(byName.broken.status).toBe("error");
        // The good server's tools still load.
        expect(Object.keys(manager.getTools())).toContain("mcp__mock__echo");
    });
});

describe("OAuth provider", () => {
    test("persists tokens/client info and reports needs-auth via a thrown redirect", async () => {
        const { LoopOAuthProvider, hasStoredTokens, clearMcpAuth, McpAuthRequiredError } =
            await import("../src/mcp/oauth");
        const server = `test-oauth-${Date.now()}`;
        clearMcpAuth(server);
        expect(hasStoredTokens(server)).toBe(false);

        const provider = new LoopOAuthProvider(server, "http://127.0.0.1:8976/callback");
        provider.saveClientInformation({ client_id: "abc" });
        provider.saveTokens({ access_token: "tok", token_type: "bearer" });
        expect(provider.clientInformation()).toMatchObject({ client_id: "abc" });
        expect(hasStoredTokens(server)).toBe(true);

        // No onRedirect opener → a forced redirect surfaces as needs-auth.
        expect(() => provider.redirectToAuthorization(new URL("https://auth.example/authorize"))).toThrow(
            McpAuthRequiredError,
        );

        clearMcpAuth(server);
        expect(hasStoredTokens(server)).toBe(false);
    });
});

/** Write a throwaway project dir with .loop/mcp.json so loadMcpServers picks it up. */
function makeProjectWith(servers: Record<string, McpServerConfig>): string {
    const { mkdtempSync, mkdirSync, writeFileSync } = require("node:fs") as typeof import("node:fs");
    const { tmpdir } = require("node:os") as typeof import("node:os");
    const root = mkdtempSync(join(tmpdir(), "loop-mcp-test-"));
    mkdirSync(join(root, ".loop"), { recursive: true });
    writeFileSync(join(root, ".loop", "mcp.json"), JSON.stringify({ mcpServers: servers }));
    return root;
}
