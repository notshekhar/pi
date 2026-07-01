/**
 * Pure argument → config parsing for `loop mcp add`, kept separate from the
 * command handler so it can be unit-tested without loading core. Mirrors the
 * Claude Code / Codex CLI ergonomics:
 *
 *   loop mcp add --transport http  <name> <url>
 *   loop mcp add --transport sse   <name> <url>  --header "Authorization: Bearer x"
 *   loop mcp add                   <name> -- <command> [args...]
 *   loop mcp add                   <name> <command> [args...]
 */
import type { HttpServerConfig, McpServerConfig, StdioServerConfig } from "@notshekhar/loop-core";

export type McpScope = "user" | "project";

export interface AddSpec {
    name: string;
    cfg: McpServerConfig;
    scope: McpScope;
}

/** Thrown for any bad invocation; the handler prints `.message` without a stack. */
export class McpUsageError extends Error {}

const NAME_RE = /^[a-zA-Z0-9_-]+$/;

function splitHeader(raw: string): [string, string] {
    const idx = raw.indexOf(":");
    if (idx < 0) throw new McpUsageError(`invalid --header "${raw}" (expected "Name: Value")`);
    const name = raw.slice(0, idx).trim();
    const value = raw.slice(idx + 1).trim();
    if (!name) throw new McpUsageError(`invalid --header "${raw}" (empty header name)`);
    return [name, value];
}

function splitEnv(raw: string): [string, string] {
    const idx = raw.indexOf("=");
    if (idx < 0) throw new McpUsageError(`invalid --env "${raw}" (expected "KEY=VALUE")`);
    const key = raw.slice(0, idx).trim();
    if (!key) throw new McpUsageError(`invalid --env "${raw}" (empty key)`);
    return [key, raw.slice(idx + 1)];
}

function asScope(raw: string): McpScope {
    if (raw === "user" || raw === "global") return "user";
    if (raw === "project" || raw === "local") return "project";
    throw new McpUsageError(`invalid --scope "${raw}" (use "user" or "project")`);
}

/** Expand `--key=value` into `["--key","value"]` so the walker only handles one form. */
function normalize(head: string[]): string[] {
    const out: string[] = [];
    for (const a of head) {
        if (a.startsWith("--") && a.includes("=")) {
            const eq = a.indexOf("=");
            out.push(a.slice(0, eq), a.slice(eq + 1));
        } else {
            out.push(a);
        }
    }
    return out;
}

/**
 * Parse the arguments after `mcp add` into a concrete server config + scope.
 * `args` excludes the `add` subcommand itself.
 */
export function buildAddConfig(args: string[]): AddSpec {
    // Everything after a literal `--` is the stdio command + its args, verbatim.
    const ddi = args.indexOf("--");
    const head = normalize(ddi >= 0 ? args.slice(0, ddi) : args);
    const tail = ddi >= 0 ? args.slice(ddi + 1) : [];

    let transport: string | undefined;
    let scope: McpScope = "user";
    let oauth = false;
    let clientId: string | undefined;
    let clientSecret: string | undefined;
    let oauthScopes: string[] | undefined;
    const headers: Record<string, string> = {};
    const env: Record<string, string> = {};
    const positional: string[] = [];

    for (let i = 0; i < head.length; i++) {
        const a = head[i];
        const take = () => {
            const v = head[++i];
            if (v === undefined) throw new McpUsageError(`missing value for ${a}`);
            return v;
        };
        switch (a) {
            case "--header":
            case "-H": {
                const [k, v] = splitHeader(take());
                headers[k] = v;
                break;
            }
            case "--env":
            case "-e": {
                const [k, v] = splitEnv(take());
                env[k] = v;
                break;
            }
            case "--transport":
            case "-t":
                transport = take().toLowerCase();
                break;
            case "--scope":
            case "-s":
                scope = asScope(take());
                break;
            case "--oauth":
                oauth = true;
                break;
            case "--client-id":
                clientId = take();
                break;
            case "--client-secret":
                clientSecret = take();
                break;
            case "--oauth-scopes":
                oauthScopes = take()
                    .split(",")
                    .map((s) => s.trim())
                    .filter(Boolean);
                break;
            default:
                if (a.startsWith("-")) throw new McpUsageError(`unknown flag: ${a}`);
                positional.push(a);
        }
    }

    const name = positional[0];
    if (!name) throw new McpUsageError("a server name is required");
    if (!NAME_RE.test(name)) {
        throw new McpUsageError(`invalid server name "${name}" (use letters, digits, "-" or "_")`);
    }

    const t = transport ?? "stdio";
    if (t === "http" || t === "sse") {
        const url = positional[1];
        if (!url) throw new McpUsageError(`a URL is required for ${t} transport`);
        if (!/^https?:\/\//i.test(url))
            throw new McpUsageError(`invalid URL "${url}" (must start with http:// or https://)`);
        if (tail.length) throw new McpUsageError("`--` command arguments only apply to stdio transport");
        if (Object.keys(env).length) throw new McpUsageError("--env only applies to stdio transport");
        const cfg: HttpServerConfig = { type: t, url };
        if (Object.keys(headers).length) cfg.headers = headers;
        if (oauth) cfg.auth = "oauth";
        if (clientId) cfg.clientId = clientId;
        if (clientSecret) cfg.clientSecret = clientSecret;
        if (oauthScopes?.length) cfg.scopes = oauthScopes;
        return { name, cfg, scope };
    }

    if (t === "stdio") {
        const command = tail.length ? tail[0] : positional[1];
        const cmdArgs = tail.length ? tail.slice(1) : positional.slice(2);
        if (!command) {
            throw new McpUsageError(
                "a command is required for stdio transport (e.g. `loop mcp add my-server -- npx -y @scope/pkg`)",
            );
        }
        if (Object.keys(headers).length) throw new McpUsageError("--header only applies to http/sse transport");
        if (oauth) throw new McpUsageError("--oauth only applies to http/sse transport");
        const cfg: StdioServerConfig = { command };
        if (cmdArgs.length) cfg.args = cmdArgs;
        if (Object.keys(env).length) cfg.env = env;
        return { name, cfg, scope };
    }

    throw new McpUsageError(`unknown transport "${transport}" (use "stdio", "http" or "sse")`);
}
