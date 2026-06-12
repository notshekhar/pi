/**
 * Lifecycle hook management: /hooks.
 */
import type { SelectItem } from "@notshekhar/pi-tui";
import {
    HOOK_EVENTS,
    addPiUserHook,
    listHooksWithSources,
    removePiUserHook,
    type CommandContext,
    type HookEvent,
} from "@notshekhar/pi-core";
import type { AppDeps } from "../deps";
import type { AppState } from "../state";

type HookHandlers = Pick<CommandContext, "manageHooks">;

export function createHookHandlers(state: AppState, deps: AppDeps): HookHandlers {
    const { tui, history, selectOnce, promptOnce } = deps;

    return {
        async manageHooks() {
            // Loop so Esc in submenus returns to the hook list, like /settings.
            while (true) {
                const entries = listHooksWithSources(state.cwd);
                const items: SelectItem[] = [
                    {
                        value: "+add",
                        label: "+ add hook",
                        description: "register a pi-owned hook in ~/.pi/settings.json",
                    },
                    ...entries.map((e, i) => ({
                        value: String(i),
                        label: `${e.event}${e.matcher ? ` [${e.matcher}]` : ""}${e.async ? " (async)" : ""}`,
                        description: `${e.source} · ${e.command.length > 70 ? `${e.command.slice(0, 67)}…` : e.command}`,
                    })),
                ];
                const pick = await selectOnce(items, `Hooks — ${entries.length} loaded (Esc to close)`);
                if (!pick) return;

                if (pick.value === "+add") {
                    const ev = await selectOnce(
                        HOOK_EVENTS.map((e) => ({ value: e, label: e, description: "" })),
                        "Hook event",
                    );
                    if (!ev) continue;
                    let matcher = "";
                    if (ev.value === "PreToolUse" || ev.value === "PostToolUse") {
                        matcher = (
                            await promptOnce(`matcher for ${ev.value} (tool name or regex, empty = all)`)
                        ).trim();
                    }
                    const command = (await promptOnce("hook command (runs via sh, JSON payload on stdin)")).trim();
                    if (!command) continue;
                    addPiUserHook(ev.value as HookEvent, command, matcher || undefined);
                    history.addSystem(`hook added: ${ev.value}${matcher ? ` [${matcher}]` : ""} → ${command}`);
                    tui.requestRender();
                    continue;
                }

                const e = entries[Number(pick.value)];
                if (!e) continue;
                const label = `${e.event}: ${e.command.length > 50 ? `${e.command.slice(0, 47)}…` : e.command}`;

                if (e.source === "pi-user") {
                    const act = await selectOnce(
                        [{ value: "remove", label: "remove", description: "delete from ~/.pi/settings.json" }],
                        label,
                    );
                    if (act?.value === "remove" && removePiUserHook(e.event, e.command)) {
                        history.addSystem(`hook removed: ${e.event} → ${e.command.slice(0, 60)}`);
                        tui.requestRender();
                    }
                    continue;
                }
                if (e.source.startsWith("claude")) {
                    const act = await selectOnce(
                        [
                            {
                                value: "copy",
                                label: "copy to pi",
                                description: "own it in ~/.pi/settings.json — keeps working without Claude Code",
                            },
                        ],
                        `${label}  (${e.source})`,
                    );
                    if (act?.value === "copy") {
                        addPiUserHook(e.event, e.command, e.matcher, e.async);
                        history.addSystem(
                            `hook copied to ~/.pi: ${e.event} → ${e.command.slice(0, 60)} — adjust claudeHooksFilter if it now fires twice`,
                        );
                        tui.requestRender();
                    }
                    continue;
                }
                // pi-project hooks live in the repo — point there instead of mutating it.
                history.addSystem(`project hook — edit ${state.cwd}/.pi/settings.json: ${e.event} → ${e.command}`);
                tui.requestRender();
            }
        },
    };
}
