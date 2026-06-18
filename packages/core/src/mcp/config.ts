/**
 * MCP server configuration. Servers are declared in ~/.loop/settings.json under
 * `mcpServers`, and optionally overridden per-project in <cwd>/.loop/mcp.json.
 * Two sources only (global + project) — project entries win on name collision.
 */
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { getSetting, setSetting } from "../settings";

/** A local server launched as a subprocess; tools speak over stdio. */
export interface StdioServerConfig {
    type?: "stdio";
    command: string;
    args?: string[];
    env?: Record<string, string>;
    /** Default true. Set false to keep the entry but skip connecting. */
    enabled?: boolean;
}

/** A remote server reached over streamable HTTP (with SSE fallback). */
export interface HttpServerConfig {
    type: "http" | "sse";
    url: string;
    headers?: Record<string, string>;
    /** "oauth" → run the browser login flow; omit for static-header auth. */
    auth?: "oauth";
    enabled?: boolean;
}

export type McpServerConfig = StdioServerConfig | HttpServerConfig;

export function isHttpServer(cfg: McpServerConfig): cfg is HttpServerConfig {
    return cfg.type === "http" || cfg.type === "sse";
}

/**
 * Master switch for MCP. Default ON — only an explicit `mcp: false` disables it.
 * Callers that spawn/connect servers additionally gate on project trust; this
 * helper is just the setting so the toggle, command visibility, and agent loop
 * all agree on one rule.
 */
export function isMcpEnabled(): boolean {
    return getSetting("mcp") !== false;
}

export function isServerEnabled(cfg: McpServerConfig): boolean {
    return cfg.enabled !== false;
}

/**
 * Substitute `${env:VAR}` placeholders from process.env so tokens live in the
 * environment, not in plaintext config. Unknown vars resolve to an empty
 * string (the request then simply fails auth, which surfaces clearly).
 */
const ENV_PLACEHOLDER = /\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}/g;

export function resolveSecrets(value: string): string {
    return value.replace(ENV_PLACEHOLDER, (_, name: string) => process.env[name] ?? "");
}

export function resolveSecretMap(map: Record<string, string> | undefined): Record<string, string> | undefined {
    if (!map) return undefined;
    const resolved: Record<string, string> = {};
    for (const [key, value] of Object.entries(map)) resolved[key] = resolveSecrets(value);
    return resolved;
}

/**
 * Merge global (settings.json) and project (<cwd>/.loop/mcp.json) server maps.
 * A malformed project file is ignored rather than crashing startup.
 */
export function loadMcpServers(cwd: string): Record<string, McpServerConfig> {
    const global = getSetting("mcpServers") ?? {};
    const project = loadProjectServers(cwd);
    return { ...global, ...project };
}

/** Servers declared in global settings (the surface /mcp can edit). */
export function getGlobalServers(): Record<string, McpServerConfig> {
    return getSetting("mcpServers") ?? {};
}

export function isGlobalServer(name: string): boolean {
    return name in getGlobalServers();
}

/** Add or replace a global server in ~/.loop/settings.json. */
export function addServer(name: string, cfg: McpServerConfig): void {
    setSetting("mcpServers", { ...getGlobalServers(), [name]: cfg });
}

/** Flip a global server's enabled flag. Returns false if not a global server. */
export function setServerEnabled(name: string, enabled: boolean): boolean {
    const servers = getGlobalServers();
    if (!servers[name]) return false;
    setSetting("mcpServers", { ...servers, [name]: { ...servers[name], enabled } });
    return true;
}

/** Delete a global server. Returns false if not a global server. */
export function removeServer(name: string): boolean {
    const servers = getGlobalServers();
    if (!servers[name]) return false;
    const next = { ...servers };
    delete next[name];
    setSetting("mcpServers", next);
    return true;
}

function loadProjectServers(cwd: string): Record<string, McpServerConfig> {
    const path = join(cwd, ".loop", "mcp.json");
    if (!existsSync(path)) return {};
    try {
        const parsed = JSON.parse(readFileSync(path, "utf8"));
        // Accept both `{ mcpServers: {...} }` and a bare `{...}` map.
        const servers = parsed?.mcpServers ?? parsed;
        return servers && typeof servers === "object" ? servers : {};
    } catch {
        return {};
    }
}
