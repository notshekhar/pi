/**
 * MCP server panel: /mcp opens an interactive list (status per server) where
 * each server can be authorized, reconnected, enabled/disabled, or deleted.
 * `/mcp reconnect [name]` keeps a scriptable, non-interactive shortcut.
 */
import type { SelectItem } from "@notshekhar/loop-tui";
import {
    getMcpManager,
    isGlobalServer,
    type CommandContext,
    type McpServerConfig,
    type ServerSnapshot,
} from "@notshekhar/loop-core";
import { openBrowser } from "../../open-browser";
import type { AppDeps } from "../deps";
import type { AppState } from "../state";

type McpHandlers = Pick<CommandContext, "manageMcp">;

// Sentinel list value for the "add server" row — a NUL prefix can't collide
// with a real server name.
const ADD_SERVER = "\0add-server";

const STATUS_LABEL: Record<ServerSnapshot["status"], string> = {
    ready: "ready",
    connecting: "connecting…",
    disabled: "disabled",
    error: "error",
    "needs-auth": "needs authorization",
};

export function createMcpHandlers(_state: AppState, deps: AppDeps): McpHandlers {
    const { tui, history, selectOnce, searchOnce, promptOnce } = deps;

    /** Prompt for a new server's config and connect it. Esc at any field aborts. */
    async function addServerFlow(): Promise<void> {
        const name = (await promptOnce("MCP server name (e.g. filesystem)")).trim();
        if (!name) return;

        const transport = await selectOnce(
            [
                { value: "stdio", label: "stdio", description: "local command over stdin/stdout" },
                { value: "http", label: "http", description: "remote streamable-HTTP server" },
                { value: "sse", label: "sse", description: "remote server-sent-events server" },
            ],
            "Transport",
        );
        if (!transport) return;

        const cfg = transport.value === "stdio" ? await promptStdioConfig() : await promptHttpConfig(transport.value);
        if (!cfg) return;

        history.addSystem(`adding ${name}…`);
        tui.requestRender();
        try {
            await getMcpManager().add(name, cfg);
            const added = getMcpManager().getServer(name);
            history.addSystem(`${name} → ${added ? STATUS_LABEL[added.status] : "added"}`);
        } catch (err) {
            history.addError(`failed to add ${name}: ${err instanceof Error ? err.message : String(err)}`);
        }
        tui.requestRender();
    }

    async function promptStdioConfig(): Promise<McpServerConfig | undefined> {
        const command = (await promptOnce("command (e.g. npx)")).trim();
        if (!command) return undefined;
        const argsRaw = (await promptOnce("args (space-separated, optional)")).trim();
        const args = argsRaw ? argsRaw.split(/\s+/) : undefined;
        // env/headers with secrets go in ~/.loop/settings.json as ${env:VAR}.
        return { type: "stdio", command, ...(args ? { args } : {}) };
    }

    async function promptHttpConfig(type: string): Promise<McpServerConfig | undefined> {
        const url = (await promptOnce("url (https://…)")).trim();
        if (!url) return undefined;
        const auth = await selectOnce(
            [
                { value: "none", label: "none", description: "no auth, or static headers set in settings.json" },
                { value: "oauth", label: "oauth", description: "browser login on first connect" },
            ],
            "Auth",
        );
        if (!auth) return undefined;
        return {
            type: type as "http" | "sse",
            url,
            ...(auth.value === "oauth" ? { auth: "oauth" as const } : {}),
        };
    }

    function detail(s: ServerSnapshot): string {
        if (s.status === "ready") return `${s.toolCount} tools`;
        if (s.status === "error" && s.error) return s.error;
        return STATUS_LABEL[s.status];
    }

    async function reconnect(name: string | undefined): Promise<void> {
        history.addSystem(name ? `reconnecting ${name}…` : "reconnecting all MCP servers…");
        tui.requestRender();
        await getMcpManager().reconnect(name || undefined);
    }

    async function authorize(name: string): Promise<void> {
        history.addSystem(`Opening browser to authorize ${name}… complete the login, then return here.`);
        tui.requestRender();
        try {
            await getMcpManager().authorize(name, (url) => openBrowser(url));
            history.addSystem(`${name} authorized and connected.`);
        } catch (err) {
            history.addError(`authorization failed for ${name}: ${err instanceof Error ? err.message : String(err)}`);
        }
        tui.requestRender();
    }

    /** Action submenu for one server. Returns to the list afterwards. */
    async function serverActions(s: ServerSnapshot): Promise<void> {
        const global = isGlobalServer(s.name);
        const items: SelectItem[] = [];
        if (s.status === "needs-auth" || s.status === "error") {
            items.push({ value: "authorize", label: "authorize", description: "run the OAuth browser login" });
        }
        items.push({ value: "reconnect", label: "reconnect", description: "retry the connection" });
        if (global) {
            items.push(
                s.status === "disabled"
                    ? { value: "enable", label: "enable", description: "turn this server on" }
                    : { value: "disable", label: "disable", description: "turn this server off" },
            );
            items.push({ value: "delete", label: "delete", description: "remove from ~/.loop/settings.json" });
        }

        const pick = await selectOnce(items, `${s.name} — ${STATUS_LABEL[s.status]}`);
        if (!pick) return;

        const manager = getMcpManager();
        if (pick.value === "authorize") return authorize(s.name);
        if (pick.value === "reconnect") return reconnect(s.name);
        if (pick.value === "enable" || pick.value === "disable") {
            await manager.setEnabled(s.name, pick.value === "enable");
            history.addSystem(`${s.name} ${pick.value}d`);
            tui.requestRender();
            return;
        }
        if (pick.value === "delete") {
            await manager.remove(s.name);
            history.addSystem(`${s.name} removed`);
            tui.requestRender();
        }
    }

    return {
        async manageMcp(args: string) {
            const manager = getMcpManager();
            const [sub, name] = args.split(/\s+/);

            // Non-interactive shortcut for scripts/muscle memory.
            if (sub === "reconnect") {
                await reconnect(name || undefined);
                const after = manager
                    .listServers()
                    .map((s) => `  ${s.name}: ${STATUS_LABEL[s.status]}`)
                    .join("\n");
                history.addSystem(after ? `MCP servers:\n${after}` : "No MCP servers configured.");
                tui.requestRender();
                return;
            }

            // Interactive panel: loop so action submenus return to the list.
            // "+ add server" is always offered, so an empty config isn't a
            // dead end.
            while (true) {
                const servers = manager.listServers();
                const items: SelectItem[] = [
                    { value: ADD_SERVER, label: "+ add server", description: "configure and connect a new MCP server" },
                    ...servers.map((s) => ({
                        value: s.name,
                        label: `${s.name} — ${STATUS_LABEL[s.status]}`,
                        description: detail(s),
                    })),
                ];
                const pick = await searchOnce(items, "MCP servers (type to filter, Esc to close)");
                if (!pick) return;
                if (pick.value === ADD_SERVER) {
                    await addServerFlow();
                    continue;
                }
                const server = manager.getServer(pick.value);
                if (server) await serverActions(server);
            }
        },
    };
}
