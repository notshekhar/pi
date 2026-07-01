/**
 * `loop mcp …` — manage MCP servers from the command line, in the style of
 * `claude mcp add` / `codex mcp add`. The bulk of the interesting logic (arg →
 * config) lives in ./mcp-add-parse so it can be tested in isolation.
 */
import {
    addProjectServer,
    addServer,
    authorizeServer,
    clearMcpAuth,
    getGlobalServers,
    getProjectServers,
    isHttpServer,
    loadMcpServers,
    projectServersPath,
    removeProjectServer,
    removeServer,
    setProjectServerEnabled,
    setServerEnabled,
    type McpServerConfig,
} from "@notshekhar/loop-core";
import { buildAddConfig, McpUsageError, type McpScope } from "./mcp-add-parse";
import { openBrowser } from "./open-browser";

/** One-line target summary for list/get output. */
function describeTarget(cfg: McpServerConfig): string {
    if (isHttpServer(cfg)) {
        const auth = cfg.auth === "oauth" ? " (oauth)" : cfg.headers ? " (header auth)" : "";
        return `${cfg.type} ${cfg.url}${auth}`;
    }
    const args = cfg.args?.length ? " " + cfg.args.join(" ") : "";
    return `stdio ${cfg.command}${args}`;
}

/** Every configured server across both scopes, with its scope tag. */
function allServers(cwd: string): Array<{ name: string; cfg: McpServerConfig; scope: McpScope }> {
    const out: Array<{ name: string; cfg: McpServerConfig; scope: McpScope }> = [];
    for (const [name, cfg] of Object.entries(getGlobalServers())) out.push({ name, cfg, scope: "user" });
    for (const [name, cfg] of Object.entries(getProjectServers(cwd))) out.push({ name, cfg, scope: "project" });
    return out;
}

function firstPositional(args: string[]): string | undefined {
    return args.find((a) => !a.startsWith("-"));
}

/** Parse an optional `--scope user|project` out of a simple subcommand's args. */
function scopeFlag(args: string[]): McpScope | undefined {
    const i = args.findIndex((a) => a === "--scope" || a === "-s");
    if (i >= 0 && args[i + 1]) return args[i + 1] === "project" || args[i + 1] === "local" ? "project" : "user";
    const eq = args.find((a) => a.startsWith("--scope="));
    if (eq) return eq.slice("--scope=".length) === "project" ? "project" : "user";
    return undefined;
}

function cmdAdd(args: string[]): void {
    const { name, cfg, scope } = buildAddConfig(args);
    const cwd = process.cwd();
    const merged = loadMcpServers(cwd);
    if (merged[name]) {
        console.log(`Note: replacing existing MCP server "${name}".`);
    }
    if (scope === "project") addProjectServer(cwd, name, cfg);
    else addServer(name, cfg);

    const where = scope === "project" ? projectServersPath(cwd) : "~/.loop/settings.json";
    console.log(`✓ Added MCP server "${name}" — ${describeTarget(cfg)}  [scope: ${scope}]`);
    console.log(`  written to ${where}`);
    if (isHttpServer(cfg) && cfg.auth === "oauth") {
        console.log(`\n  This server uses OAuth. Sign in with:\n    loop mcp login ${name}`);
    }
}

function cmdList(cwd: string): void {
    const servers = allServers(cwd);
    if (servers.length === 0) {
        console.log("No MCP servers configured.\nAdd one with:  loop mcp add --transport http <name> <url>");
        return;
    }
    for (const { name, cfg, scope } of servers) {
        const disabled = cfg.enabled === false ? "  (disabled)" : "";
        console.log(`${name}  —  ${describeTarget(cfg)}  [${scope}]${disabled}`);
    }
}

function cmdGet(args: string[]): void {
    const name = firstPositional(args);
    if (!name) throw new McpUsageError("usage: loop mcp get <name>");
    const found = allServers(process.cwd()).find((s) => s.name === name);
    if (!found) throw new McpUsageError(`no MCP server named "${name}"`);
    console.log(`${name}  [scope: ${found.scope}]`);
    console.log(describeTarget(found.cfg));
    console.log(JSON.stringify(found.cfg, null, 2));
}

function cmdRemove(args: string[]): void {
    const name = firstPositional(args);
    if (!name) throw new McpUsageError("usage: loop mcp remove <name> [--scope user|project]");
    const cwd = process.cwd();
    const wanted = scopeFlag(args);
    let removed: McpScope | undefined;
    if ((!wanted || wanted === "user") && removeServer(name)) removed = "user";
    else if ((!wanted || wanted === "project") && removeProjectServer(cwd, name)) removed = "project";
    if (!removed) {
        throw new McpUsageError(`no MCP server named "${name}"${wanted ? ` in scope ${wanted}` : ""}`);
    }
    clearMcpAuth(name); // forget any stored OAuth session
    console.log(`✓ Removed MCP server "${name}" [scope: ${removed}]`);
}

function cmdSetEnabled(args: string[], enabled: boolean): void {
    const name = firstPositional(args);
    if (!name) throw new McpUsageError(`usage: loop mcp ${enabled ? "enable" : "disable"} <name>`);
    const cwd = process.cwd();
    const wanted = scopeFlag(args);
    const ok =
        (!wanted || wanted === "user" ? setServerEnabled(name, enabled) : false) ||
        (!wanted || wanted === "project" ? setProjectServerEnabled(cwd, name, enabled) : false);
    if (!ok) throw new McpUsageError(`no MCP server named "${name}"`);
    console.log(`✓ ${enabled ? "Enabled" : "Disabled"} MCP server "${name}"`);
}

async function cmdLogin(args: string[]): Promise<void> {
    const name = firstPositional(args);
    if (!name) throw new McpUsageError("usage: loop mcp login <name>");
    const cfg = loadMcpServers(process.cwd())[name];
    if (!cfg) throw new McpUsageError(`no MCP server named "${name}"`);
    if (!isHttpServer(cfg)) throw new McpUsageError(`"${name}" is a stdio server — OAuth only applies to http/sse`);
    console.log(`Authorizing "${name}"…`);
    await authorizeServer(name, cfg, (url) => {
        console.log(`\nOpening your browser to:\n  ${url}\n`);
        openBrowser(url);
    });
    console.log(`✓ Authorized "${name}". It will connect on next launch.`);
}

function cmdAddJson(args: string[]): void {
    const positional = args.filter((a) => !a.startsWith("-"));
    const name = positional[0];
    const json = positional[1];
    if (!name || !json) throw new McpUsageError(`usage: loop mcp add-json <name> '<json>' [--scope user|project]`);
    let cfg: McpServerConfig;
    try {
        cfg = JSON.parse(json) as McpServerConfig;
    } catch (err) {
        throw new McpUsageError(`invalid JSON: ${(err as Error).message}`);
    }
    if (!cfg || typeof cfg !== "object") throw new McpUsageError("JSON must be a server config object");
    const looksHttp = "url" in cfg;
    const looksStdio = "command" in cfg;
    if (!looksHttp && !looksStdio) throw new McpUsageError('config needs either "url" (http/sse) or "command" (stdio)');
    const scope = scopeFlag(args) ?? "user";
    if (scope === "project") addProjectServer(process.cwd(), name, cfg);
    else addServer(name, cfg);
    console.log(`✓ Added MCP server "${name}" — ${describeTarget(cfg)}  [scope: ${scope}]`);
}

function printHelp(): void {
    console.log(`loop mcp — manage MCP (Model Context Protocol) servers

Usage:
  loop mcp add [options] <name> <url>            add an http/sse server
  loop mcp add [options] <name> -- <cmd> [args]  add a stdio (local) server
  loop mcp add-json <name> '<json>'              add from a raw JSON config
  loop mcp list                                  list configured servers
  loop mcp get <name>                            show one server's config
  loop mcp remove <name>                         remove a server
  loop mcp enable|disable <name>                 toggle a server
  loop mcp login <name>                          run the OAuth browser sign-in

Options for add:
  --transport, -t <stdio|http|sse>   transport (default: stdio)
  --scope, -s <user|project>         where to store it (default: user)
  --header, -H "Name: Value"         static auth header (repeatable, http/sse)
  --env, -e KEY=VALUE                environment variable (repeatable, stdio)
  --oauth                            use OAuth sign-in (http/sse)
  --client-id <id>                   pre-registered OAuth client id
  --client-secret <secret>           OAuth client secret (supports \${env:VAR})
  --oauth-scopes a,b,c               OAuth scopes to request

Examples:
  loop mcp add --transport http docs https://code.claude.com/docs/mcp
  loop mcp add --transport http figma https://mcp.figma.com/mcp --oauth
  loop mcp add --transport sse linear https://mcp.linear.app/sse \\
    --header "Authorization: Bearer \${env:LINEAR_TOKEN}"
  loop mcp add fs -- npx -y @modelcontextprotocol/server-filesystem ~/code

Tokens: headers and \${env:VAR} placeholders resolve from your environment at
connect time, so secrets stay out of the config file.`);
}

/** Entry point wired from cli.ts; `argv` is everything after `mcp`. */
export async function cmdMcp(argv: string[]): Promise<void> {
    const [sub, ...rest] = argv;
    try {
        switch (sub) {
            case "add":
                cmdAdd(rest);
                return;
            case "add-json":
                cmdAddJson(rest);
                return;
            case "list":
            case "ls":
                cmdList(process.cwd());
                return;
            case "get":
                cmdGet(rest);
                return;
            case "remove":
            case "rm":
            case "delete":
                cmdRemove(rest);
                return;
            case "enable":
                cmdSetEnabled(rest, true);
                return;
            case "disable":
                cmdSetEnabled(rest, false);
                return;
            case "login":
            case "authorize":
                await cmdLogin(rest);
                return;
            case undefined:
            case "help":
            case "--help":
            case "-h":
                printHelp();
                return;
            default:
                console.error(`unknown mcp subcommand: "${sub}"\n`);
                printHelp();
                process.exitCode = 1;
        }
    } catch (err) {
        if (err instanceof McpUsageError) {
            console.error(`error: ${err.message}`);
            process.exitCode = 1;
            return;
        }
        throw err;
    }
}
