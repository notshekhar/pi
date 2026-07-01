import { describe, expect, test } from "bun:test";
import { buildAddConfig, McpUsageError } from "../src/mcp-add-parse";

describe("buildAddConfig — http/sse", () => {
    test("http server with a URL (the claude-code-docs example)", () => {
        const spec = buildAddConfig(["--transport", "http", "docs", "https://code.claude.com/docs/mcp"]);
        expect(spec).toEqual({
            name: "docs",
            scope: "user",
            cfg: { type: "http", url: "https://code.claude.com/docs/mcp" },
        });
    });

    test("sse server via short -t flag", () => {
        const spec = buildAddConfig(["-t", "sse", "linear", "https://mcp.linear.app/sse"]);
        expect(spec.cfg).toEqual({ type: "sse", url: "https://mcp.linear.app/sse" });
    });

    test("--oauth sets auth and passes through client credentials + scopes", () => {
        const spec = buildAddConfig([
            "--transport",
            "http",
            "figma",
            "https://mcp.figma.com/mcp",
            "--oauth",
            "--client-id",
            "abc",
            "--client-secret",
            "${env:FIGMA_SECRET}",
            "--oauth-scopes",
            "read,write",
        ]);
        expect(spec.cfg).toEqual({
            type: "http",
            url: "https://mcp.figma.com/mcp",
            auth: "oauth",
            clientId: "abc",
            clientSecret: "${env:FIGMA_SECRET}",
            scopes: ["read", "write"],
        });
    });

    test("repeatable --header / -H become a headers map", () => {
        const spec = buildAddConfig([
            "-t",
            "http",
            "svc",
            "https://x/mcp",
            "--header",
            "Authorization: Bearer ${env:TOK}",
            "-H",
            "X-Team: acme",
        ]);
        expect(spec.cfg).toMatchObject({
            headers: { Authorization: "Bearer ${env:TOK}", "X-Team": "acme" },
        });
    });

    test("--transport=http (equals form) is accepted", () => {
        const spec = buildAddConfig(["--transport=http", "svc", "https://x/mcp"]);
        expect(spec.cfg).toMatchObject({ type: "http", url: "https://x/mcp" });
    });

    test("--scope project is honored", () => {
        expect(buildAddConfig(["-t", "http", "svc", "https://x/mcp", "--scope", "project"]).scope).toBe("project");
    });

    test("rejects a missing URL", () => {
        expect(() => buildAddConfig(["-t", "http", "svc"])).toThrow(McpUsageError);
    });

    test("rejects a non-http URL", () => {
        expect(() => buildAddConfig(["-t", "http", "svc", "ftp://x"])).toThrow(/must start with http/);
    });

    test("rejects a malformed --header", () => {
        expect(() => buildAddConfig(["-t", "http", "svc", "https://x", "-H", "no-colon"])).toThrow(/invalid --header/);
    });

    test("rejects --env on http transport", () => {
        expect(() => buildAddConfig(["-t", "http", "svc", "https://x", "-e", "A=1"])).toThrow(/only applies to stdio/);
    });
});

describe("buildAddConfig — stdio", () => {
    test("stdio is the default; command + args after `--`", () => {
        const spec = buildAddConfig(["fs", "--", "npx", "-y", "@modelcontextprotocol/server-filesystem", "~/code"]);
        expect(spec).toEqual({
            name: "fs",
            scope: "user",
            cfg: { command: "npx", args: ["-y", "@modelcontextprotocol/server-filesystem", "~/code"] },
        });
    });

    test("command + args without `--` (no leading dashes)", () => {
        const spec = buildAddConfig(["mem", "my-server", "start"]);
        expect(spec.cfg).toEqual({ command: "my-server", args: ["start"] });
    });

    test("a bare command with no args omits the args field", () => {
        const spec = buildAddConfig(["x", "--", "some-binary"]);
        expect(spec.cfg).toEqual({ command: "some-binary" });
    });

    test("flags before `--` still apply; --env builds the env map", () => {
        const spec = buildAddConfig(["db", "-e", "PGHOST=localhost", "--env", "PGPORT=5432", "--", "pg-mcp"]);
        expect(spec.cfg).toEqual({ command: "pg-mcp", env: { PGHOST: "localhost", PGPORT: "5432" } });
    });

    test("`--` args with dashes are preserved verbatim", () => {
        const spec = buildAddConfig(["s", "--", "uvx", "server", "--port", "9000"]);
        expect(spec.cfg).toEqual({ command: "uvx", args: ["server", "--port", "9000"] });
    });

    test("rejects a missing command", () => {
        expect(() => buildAddConfig(["s"])).toThrow(/command is required/);
    });

    test("rejects --header on stdio transport", () => {
        expect(() => buildAddConfig(["s", "-H", "A: b", "--", "cmd"])).toThrow(/only applies to http/);
    });

    test("rejects --oauth on stdio transport", () => {
        expect(() => buildAddConfig(["s", "--oauth", "--", "cmd"])).toThrow(/only applies to http/);
    });
});

describe("buildAddConfig — general validation", () => {
    test("rejects a missing name", () => {
        expect(() => buildAddConfig(["-t", "http"])).toThrow(/server name is required/);
    });

    test("rejects an invalid name", () => {
        expect(() => buildAddConfig(["bad name", "--", "cmd"])).toThrow(/invalid server name/);
    });

    test("rejects an unknown flag", () => {
        expect(() => buildAddConfig(["--wat", "x", "--", "cmd"])).toThrow(/unknown flag/);
    });

    test("rejects an unknown transport", () => {
        expect(() => buildAddConfig(["-t", "carrier-pigeon", "x", "y"])).toThrow(/unknown transport/);
    });

    test("rejects a flag missing its value", () => {
        expect(() => buildAddConfig(["x", "--transport"])).toThrow(/missing value/);
    });

    test("rejects an invalid --scope", () => {
        expect(() => buildAddConfig(["-t", "http", "x", "https://y", "-s", "galaxy"])).toThrow(/invalid --scope/);
    });
});
