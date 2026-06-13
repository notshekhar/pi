/**
 * Long-lived MCP connections. Clients hold subprocesses/sockets, so they are
 * created once at startup (not per turn) and reused across the session. The
 * agent loop reads aggregated tools via the module singleton; the /mcp panel
 * reads status snapshots and drives authorize/enable/remove.
 */
import { UnauthorizedError } from "@ai-sdk/mcp";
import { connectServer, serverPrefix, type McpClient, type McpToolSet } from "./client";
import { isServerEnabled, loadMcpServers, removeServer, setServerEnabled, type McpServerConfig } from "./config";
import { authorizeServer } from "./authorize";
import { clearMcpAuth, McpAuthRequiredError } from "./oauth";

export type ServerStatus = "disabled" | "connecting" | "ready" | "error" | "needs-auth";

export interface ServerState {
    name: string;
    status: ServerStatus;
    toolCount: number;
    error?: string;
    config: McpServerConfig;
    client?: McpClient;
}

/** Status snapshot for the /mcp panel — no live client handle leaked. */
export type ServerSnapshot = Omit<ServerState, "client">;

export class McpManager {
    private servers = new Map<string, ServerState>();
    private tools: McpToolSet = {};
    private initialized = false;
    private cwd = process.cwd();

    /** Connect every enabled server in parallel. Safe to call once per session. */
    async init(cwd: string): Promise<void> {
        if (this.initialized) return;
        this.initialized = true;
        this.cwd = cwd;
        const configs = loadMcpServers(cwd);
        await Promise.allSettled(Object.entries(configs).map(([name, cfg]) => this.connectOne(name, cfg)));
    }

    private async connectOne(name: string, cfg: McpServerConfig): Promise<void> {
        if (!isServerEnabled(cfg)) {
            this.servers.set(name, { name, status: "disabled", toolCount: 0, config: cfg });
            return;
        }
        this.servers.set(name, { name, status: "connecting", toolCount: 0, config: cfg });
        try {
            const { client, tools, toolCount } = await connectServer(name, cfg);
            // Tool keys are already namespaced with this server's prefix by
            // connectServer, so a plain merge can't clobber another server.
            Object.assign(this.tools, tools);
            this.servers.set(name, { name, status: "ready", toolCount, config: cfg, client });
        } catch (err) {
            this.setFailed(name, cfg, err);
        }
    }

    /** OAuth servers that aren't logged in get a distinct, actionable status. */
    private setFailed(name: string, cfg: McpServerConfig, err: unknown): void {
        const needsAuth = err instanceof McpAuthRequiredError || err instanceof UnauthorizedError;
        this.servers.set(name, {
            name,
            status: needsAuth ? "needs-auth" : "error",
            toolCount: 0,
            config: cfg,
            error: needsAuth ? undefined : err instanceof Error ? err.message : String(err),
        });
    }

    /** Aggregated, namespaced tool set for the agent loop. Empty until init. */
    getTools(): McpToolSet {
        return this.tools;
    }

    listServers(): ServerSnapshot[] {
        return [...this.servers.values()].map(({ client: _client, ...rest }) => rest);
    }

    getServer(name: string): ServerSnapshot | undefined {
        const s = this.servers.get(name);
        if (!s) return undefined;
        const { client: _client, ...rest } = s;
        return rest;
    }

    hasServers(): boolean {
        return this.servers.size > 0;
    }

    /** Reconnect one server (or all). Used by /mcp reconnect. */
    async reconnect(name?: string): Promise<void> {
        const targets = name
            ? [this.servers.get(name)].filter((s): s is ServerState => s !== undefined)
            : [...this.servers.values()];
        for (const server of targets) {
            await this.closeOne(server);
            await this.connectOne(server.name, server.config);
        }
    }

    /** Run the browser OAuth login for a server, then connect it. */
    async authorize(name: string, openUrl: (url: string) => void): Promise<void> {
        const server = this.servers.get(name);
        if (!server) throw new Error(`unknown MCP server: ${name}`);
        await authorizeServer(name, server.config, openUrl);
        await this.closeOne(server);
        await this.connectOne(name, server.config);
    }

    /** Toggle a global server on/off, persisting the choice and (dis)connecting. */
    async setEnabled(name: string, enabled: boolean): Promise<boolean> {
        const server = this.servers.get(name);
        if (!server) return false;
        if (!setServerEnabled(name, enabled)) return false;
        const cfg: McpServerConfig = { ...server.config, enabled };
        await this.closeOne(server);
        if (enabled) {
            await this.connectOne(name, cfg);
        } else {
            this.servers.set(name, { name, status: "disabled", toolCount: 0, config: cfg });
        }
        return true;
    }

    /** Delete a global server: disconnect, forget its OAuth session, drop config. */
    async remove(name: string): Promise<boolean> {
        const server = this.servers.get(name);
        if (server) await this.closeOne(server);
        clearMcpAuth(name);
        this.servers.delete(name);
        return removeServer(name);
    }

    async close(): Promise<void> {
        await Promise.allSettled([...this.servers.values()].map((s) => this.closeOne(s)));
        this.tools = {};
        this.servers.clear();
        this.initialized = false;
    }

    private async closeOne(server: ServerState): Promise<void> {
        try {
            await server.client?.close();
        } catch {
            // Best-effort teardown — a wedged transport shouldn't block exit.
        }
        this.dropTools(server.name);
    }

    private dropTools(name: string): void {
        const prefix = serverPrefix(name);
        for (const key of Object.keys(this.tools)) {
            if (key.startsWith(prefix)) delete this.tools[key];
        }
    }
}

let singleton: McpManager | undefined;

export function getMcpManager(): McpManager {
    if (!singleton) singleton = new McpManager();
    return singleton;
}
