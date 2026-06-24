/**
 * Settings & reload: /settings, /reload.
 */
import { existsSync, readdirSync } from "node:fs";
import { join } from "node:path";
import type { SelectItem } from "@notshekhar/loop-tui";
import {
    CommandRegistry,
    DEFAULT_AGENT_NAME,
    agentExists,
    bustCatalogCache,
    getCatalog,
    getMcpManager,
    getExtensionHost,
    registerBuiltins,
    settingsStore,
    type CommandContext,
} from "@notshekhar/loop-core";
import type { AppDeps } from "../deps";
import type { AppState } from "../state";
import { startMcpServers } from "../startup";
import { initTheme } from "../ui/theme";
import { currentBashDeny, runBashDenyManager } from "./bashdeny-handlers";

type SettingsHandlers = Pick<CommandContext, "openSettings" | "reload">;

export function createSettingsHandlers(state: AppState, deps: AppDeps): SettingsHandlers {
    const { tui, history, footer, commands, showWorking, hideWorking, searchOnce, promptOnce, refreshCommands } = deps;

    // Boolean settings toggle in place; unset falls back to the default here.
    const BOOLEAN_DEFAULTS: Record<string, boolean> = {
        subagents: true,
        recap: false,
        clock: false,
        reminders: true,
        mcp: true,
    };
    const boolSetting = (key: string): boolean =>
        (settingsStore.get(key) as boolean | undefined) ?? BOOLEAN_DEFAULTS[key];

    return {
        async openSettings() {
            // Loop so Esc on the value prompt returns to the settings picker
            // instead of bailing out of /settings entirely. `lastIndex` re-opens
            // the picker on the row the user just acted on (toggles, value edits)
            // instead of snapping back to the top.
            let lastIndex = 0;
            while (true) {
                const items: SelectItem[] = [
                    { value: "theme", label: `theme: ${settingsStore.get("theme") ?? "dark"}` },
                    {
                        value: "maxSteps",
                        label: `maxSteps: ${(settingsStore.get("maxSteps") as number) || "unlimited"}`,
                    },
                    {
                        value: "autoCompactThreshold",
                        label: `autoCompactThreshold: ${settingsStore.get("autoCompactThreshold") ?? 0.8}`,
                    },
                    {
                        value: "workspaceContext",
                        label: `workspaceContext: ${settingsStore.get("workspaceContext") ?? true}`,
                    },
                    {
                        value: "subagents",
                        label: `subagents (task tool): ${boolSetting("subagents") ? "on" : "off"}`,
                        description: "let agents delegate work to subagents via the task tool",
                    },
                    {
                        value: "recap",
                        label: `recap: ${boolSetting("recap") ? "on" : "off"}`,
                        description: "short AI-generated recap under responses that changed files",
                    },
                    {
                        value: "clock",
                        label: `clock: ${boolSetting("clock") ? "on" : "off"}`,
                        description: "live date + hh:mm:ss in the footer",
                    },
                    {
                        value: "reminders",
                        label: `reminders: ${boolSetting("reminders") ? "on" : "off"}`,
                        description: "fire /reminder alerts; off mutes them without deleting any",
                    },
                    {
                        value: "mcp",
                        label: `mcp servers: ${boolSetting("mcp") ? "on" : "off"}`,
                        description: "connect configured MCP servers and expose their tools (/mcp to manage)",
                    },
                    {
                        value: "bashDeny",
                        label: `bash denylist: ${currentBashDeny().length} blocked`,
                        description: "add/remove bash commands the agent is refused (guardrail)",
                    },
                ];
                const pick = await searchOnce(items, "Settings (type to filter, Esc to close)", {
                    initialIndex: lastIndex,
                });
                if (!pick) return;
                lastIndex = Math.max(
                    0,
                    items.findIndex((i) => i.value === pick.value),
                );
                // Sub-flow: open the denylist manager, then return to settings.
                if (pick.value === "bashDeny") {
                    await runBashDenyManager(deps);
                    continue;
                }
                if (pick.value in BOOLEAN_DEFAULTS) {
                    const next = !boolSetting(pick.value);
                    settingsStore.set(pick.value, next);
                    deps.syncTicker(); // clock toggle starts/stops the 1s footer pulse
                    history.addSystem(`${pick.value} → ${next ? "on" : "off"}`);
                    // MCP toggle connects/tears down servers live so the change
                    // takes effect this session without a /reload. startMcpServers
                    // re-checks the (now-updated) setting + trust itself.
                    if (pick.value === "mcp") {
                        if (next) startMcpServers(state, deps);
                        else void getMcpManager().close();
                    }
                    tui.requestRender();
                    continue;
                }
                // Theme gets a picker (built-ins + ~/.loop/agent/themes/*.json) and
                // applies live — the global theme proxy makes themed components
                // re-resolve colors on the next render.
                if (pick.value === "theme") {
                    const customDir = join(process.env.HOME ?? "", ".loop", "agent", "themes");
                    const custom = existsSync(customDir)
                        ? readdirSync(customDir)
                              .filter((f) => f.endsWith(".json"))
                              .map((f) => f.replace(/\.json$/, ""))
                        : [];
                    const cur = (settingsStore.get("theme") as string) ?? "dark";
                    const themeItems: SelectItem[] = ["dark", "light", ...custom].map((n) => ({
                        value: n,
                        label: n,
                        description: n === cur ? "(current)" : "",
                    }));
                    const tPick = await searchOnce(themeItems, "Theme (type to filter)");
                    if (!tPick) continue;
                    settingsStore.set("theme", tPick.value);
                    initTheme(tPick.value);
                    tui.invalidate();
                    history.addSystem(`theme → ${tPick.value}`);
                    tui.requestRender(true);
                    continue;
                }
                history.addSystem(`enter new value for ${pick.value}: (Esc to go back)`);
                tui.requestRender();
                const v = await promptOnce("");
                if (!v) continue;
                const key = pick.value;
                const cur = settingsStore.get(key);
                const parsed = typeof cur === "number" ? Number(v) : typeof cur === "boolean" ? v === "true" : v;
                settingsStore.set(key, parsed);
                history.addSystem(`${key} → ${parsed}`);
                tui.requestRender();
            }
        },
        async reload() {
            // Hard reload: every config surface re-read from disk, models
            // re-fetched from the network (blocking, so the result is real).
            showWorking("Reloading");
            tui.requestRender();
            try {
                // Theme (settings may have changed on disk).
                initTheme((settingsStore.get("theme") as string | undefined) ?? "dark");

                // Commands: prompts, skills, agents — rebuilt from disk.
                const fresh = new CommandRegistry();
                await registerBuiltins(fresh, { cwd: state.cwd });
                // Re-apply extension command contributions so /reload keeps them
                // (no-op when no extensions are loaded).
                getExtensionHost().applyCommands(fresh);
                (commands as unknown as { commands: Map<string, unknown> }).commands = (
                    fresh as unknown as { commands: Map<string, unknown> }
                ).commands;
                refreshCommands();

                // Active agent may have been deleted on disk meanwhile.
                if (!agentExists(state.agent)) {
                    state.agent = DEFAULT_AGENT_NAME;
                    settingsStore.set("agent", DEFAULT_AGENT_NAME);
                }
                footer.setAgent(state.agent);

                // Models: force-refresh availability + model definitions.
                bustCatalogCache();
                const cat = await getCatalog({ refresh: true });
                const available = Object.values(cat).filter((m) => m.available).length;

                tui.invalidate();
                history.addSystem(
                    `reloaded — settings, theme, commands, agents, hooks config, models (${available}/${Object.keys(cat).length} available)`,
                );
            } catch (err) {
                history.addError(`reload failed: ${err instanceof Error ? err.message : String(err)}`);
            } finally {
                hideWorking();
            }
            tui.requestRender(true);
        },
    };
}
