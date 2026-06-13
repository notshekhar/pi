/**
 * MCP server panel: /mcp opens an interactive list (status per server) where
 * each server can be authorized, reconnected, enabled/disabled, or deleted.
 * `/mcp reconnect [name]` keeps a scriptable, non-interactive shortcut.
 */
import type { SelectItem } from "@notshekhar/pi-tui";
import { getMcpManager, isGlobalServer, type CommandContext, type ServerSnapshot } from "@notshekhar/pi-core";
import { openBrowser } from "../../open-browser";
import type { AppDeps } from "../deps";
import type { AppState } from "../state";

type McpHandlers = Pick<CommandContext, "manageMcp">;

const STATUS_LABEL: Record<ServerSnapshot["status"], string> = {
    ready: "ready",
    connecting: "connecting…",
    disabled: "disabled",
    error: "error",
    "needs-auth": "needs authorization",
};

export function createMcpHandlers(_state: AppState, deps: AppDeps): McpHandlers {
    const { tui, history, selectOnce, searchOnce } = deps;

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
            items.push({ value: "delete", label: "delete", description: "remove from ~/.pi/settings.json" });
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
            while (true) {
                const servers = manager.listServers();
                if (servers.length === 0) {
                    history.addSystem(
                        "No MCP servers connected. Add them under mcpServers in ~/.pi/settings.json " +
                            "(MCP must be enabled in /settings and the project trusted).",
                    );
                    tui.requestRender();
                    return;
                }
                const items: SelectItem[] = servers.map((s) => ({
                    value: s.name,
                    label: `${s.name} — ${STATUS_LABEL[s.status]}`,
                    description: detail(s),
                }));
                const pick = await searchOnce(items, "MCP servers (type to filter, Esc to close)");
                if (!pick) return;
                const server = manager.getServer(pick.value);
                if (server) await serverActions(server);
            }
        },
    };
}
