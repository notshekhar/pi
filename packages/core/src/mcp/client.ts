/**
 * Connects to a single MCP server and returns its tools, namespaced so they
 * never collide with pi's built-ins or another server's tools.
 */
import { experimental_createMCPClient } from "@ai-sdk/mcp";
import { isHttpServer, type McpServerConfig } from "./config";
import { buildTransport } from "./transport";
import { PiOAuthProvider } from "./oauth";

export type McpClient = Awaited<ReturnType<typeof experimental_createMCPClient>>;
export type McpToolSet = Record<string, unknown>;

export interface ConnectResult {
    client: McpClient;
    tools: McpToolSet;
    /** Number of tools the server exposed (after namespacing). */
    toolCount: number;
}

/** Tool/server name charset the AI SDK accepts in a tool key. */
const UNSAFE_NAME_CHARS = /[^a-zA-Z0-9_]/g;

function sanitize(name: string): string {
    return name.replace(UNSAFE_NAME_CHARS, "_");
}

/** Shared prefix for every tool from one server: `mcp__<server>__`. */
export function serverPrefix(server: string): string {
    return `mcp__${sanitize(server)}__`;
}

/** `mcp__<server>__<tool>` — the Claude Code convention. */
export function namespacedToolName(server: string, tool: string): string {
    return `${serverPrefix(server)}${sanitize(tool)}`;
}

function namespaceTools(server: string, tools: McpToolSet): McpToolSet {
    const namespaced: McpToolSet = {};
    for (const [toolName, tool] of Object.entries(tools)) {
        namespaced[namespacedToolName(server, toolName)] = tool;
    }
    return namespaced;
}

/**
 * Throws on connection failure — the manager catches per-server so one bad
 * server never takes down the rest. OAuth servers get a token-backed provider
 * (no browser opener) so the transport can refresh silently; if no tokens are
 * stored yet the provider throws McpAuthRequiredError, which the manager maps
 * to needs-auth.
 */
export async function connectServer(name: string, cfg: McpServerConfig): Promise<ConnectResult> {
    const authProvider =
        isHttpServer(cfg) && cfg.auth === "oauth"
            ? new PiOAuthProvider(name, oauthRefreshRedirectUri())
            : undefined;
    const transport = buildTransport(cfg, authProvider);
    const client = await experimental_createMCPClient({
        name: `pi-mcp-${name}`,
        transport,
        // Surface async transport errors instead of letting them crash the
        // process; the manager already tracks per-server status.
        onUncaughtError: () => {},
    });
    const rawTools = (await client.tools()) as McpToolSet;
    const tools = namespaceTools(name, rawTools);
    return { client, tools, toolCount: Object.keys(tools).length };
}

/**
 * Redirect URI used only for token refresh on background connects. Matches the
 * callback server's preferred port so a server that allow-lists the URI accepts
 * it; refresh itself never sends the redirect, so the exact port rarely matters.
 */
function oauthRefreshRedirectUri(): string {
    const port = Number(process.env.PI_MCP_OAUTH_CALLBACK_PORT) || 8976;
    return `http://127.0.0.1:${port}/callback`;
}
