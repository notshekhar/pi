/**
 * Connects to a single MCP server and returns its tools, namespaced so they
 * never collide with loop's built-ins or another server's tools.
 */
import { createMCPClient } from "@ai-sdk/mcp";
import { isHttpServer, type McpServerConfig } from "./config";
import { buildTransport } from "./transport";
import { oauthClientOptions, LoopOAuthProvider } from "./oauth";

export type McpClient = Awaited<ReturnType<typeof createMCPClient>>;
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

/**
 * Hard ceiling on a single MCP tool call. A wedged stdio child or a dropped
 * HTTP connection can leave `callTool` pending forever — the agent loop then
 * awaits a result that never arrives and the whole UI appears frozen. Racing
 * each call against a timeout turns that hang into a normal tool error the
 * model can recover from. Overridable via LOOP_MCP_TOOL_TIMEOUT_MS.
 */
const MCP_TOOL_TIMEOUT_MS = Number(process.env.LOOP_MCP_TOOL_TIMEOUT_MS) || 120_000;

type ExecutableTool = { execute?: (input: unknown, options: unknown) => Promise<unknown> };

interface McpCallResult {
    content?: Array<{ type?: string; text?: string }>;
    structuredContent?: unknown;
    isError?: boolean;
}

function isMcpCallResult(value: unknown): value is McpCallResult {
    return typeof value === "object" && value !== null && ("content" in value || "structuredContent" in value);
}

/**
 * Preserve a tool's `structuredContent`. The AI SDK MCP client only maps a
 * result's `content` blocks into the model-facing output when the tool is
 * created with an explicit outputSchema; with the automatic schemas we use
 * (client.tools() with no args), `structuredContent` is dropped entirely. A
 * server that returns its real payload there with an empty `content` array
 * (e.g. codespec) then hands the model — and our persistence — nothing.
 *
 * So when a result carries structuredContent but no text block, surface it as a
 * text block. Generic: any structured-output MCP server benefits, no per-server
 * code. Servers that already return text content are left untouched.
 */
function preserveStructuredContent(result: unknown): unknown {
    if (!isMcpCallResult(result)) return result;
    if (result.structuredContent == null) return result;
    const content = Array.isArray(result.content) ? result.content : [];
    const hasText = content.some((part) => part?.type === "text" && part.text);
    if (hasText) return result;
    const asText = { type: "text", text: JSON.stringify(result.structuredContent, null, 2) };
    return { ...result, content: [...content, asText] };
}

/**
 * Wrap a tool's execute so a call that never settles rejects instead of
 * hanging, and so structuredContent-only results aren't silently lost.
 */
function withTimeout(name: string, tool: ExecutableTool): ExecutableTool {
    if (typeof tool.execute !== "function") return tool;
    const original = tool.execute.bind(tool);
    return {
        ...tool,
        execute: (input: unknown, options: unknown) => {
            let timer: ReturnType<typeof setTimeout>;
            const timeout = new Promise<never>((_, reject) => {
                timer = setTimeout(
                    () => reject(new Error(`MCP tool ${name} timed out after ${MCP_TOOL_TIMEOUT_MS}ms`)),
                    MCP_TOOL_TIMEOUT_MS,
                );
            });
            return Promise.race([original(input, options), timeout])
                .then(preserveStructuredContent)
                .finally(() => clearTimeout(timer));
        },
    };
}

function namespaceTools(server: string, tools: McpToolSet): McpToolSet {
    const namespaced: McpToolSet = {};
    for (const [toolName, tool] of Object.entries(tools)) {
        namespaced[namespacedToolName(server, toolName)] = withTimeout(
            namespacedToolName(server, toolName),
            tool as ExecutableTool,
        );
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
            ? new LoopOAuthProvider(name, oauthRefreshRedirectUri(), undefined, oauthClientOptions(cfg))
            : undefined;
    const transport = buildTransport(cfg, authProvider);
    const client = await createMCPClient({
        name: `loop-mcp-${name}`,
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
    const port = Number(process.env.LOOP_MCP_OAUTH_CALLBACK_PORT) || 8976;
    return `http://127.0.0.1:${port}/callback`;
}
