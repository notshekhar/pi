import type { CommandContext } from "@notshekhar/pi-core";
import type { AppDeps } from "./deps";
import type { AppState } from "./state";
import { isCtrlC, isCtrlD, isCtrlE, isCtrlI, isCtrlL, isCtrlV, isEsc, isTab } from "./keys";
import { pickImageFile, readClipboardImageToFile } from "./clipboard-image";
import { listAgents, settingsStore } from "@notshekhar/pi-core";

export type InputListener = (data: string) => { consume: boolean } | undefined;

export function createInputHandler(state: AppState, deps: AppDeps, ctx: CommandContext): InputListener {
    const { tui, history, queuedMessages, renderPending, hideWorking, cleanExit, editor, footer } = deps;

    return (data) => {
        // Tab on an empty prompt cycles built-in agents (default ⇄ plan).
        // Guarded on editor focus + empty text so autocomplete and selectors
        // keep their Tab behavior.
        if (isTab(data) && (editor as unknown as { focused?: boolean }).focused && editor.getText().trim() === "") {
            const builtins = listAgents()
                .filter((a) => a.builtin)
                .map((a) => a.name);
            const next = builtins[(builtins.indexOf(state.agent) + 1) % builtins.length];
            state.agent = next;
            settingsStore.set("agent", next);
            footer.setAgent(next);
            tui.requestRender();
            return { consume: true };
        }
        if (isCtrlL(data)) {
            ctx.clearScreen();
            return { consume: true };
        }
        if (isCtrlE(data)) {
            history.toggleToolsExpanded();
            tui.requestRender();
            return { consume: true };
        }
        if (isCtrlV(data)) {
            const path = readClipboardImageToFile();
            if (path) {
                void ctx.attachImage(path);
                return { consume: true };
            }
        }
        if (isCtrlI(data)) {
            const path = pickImageFile();
            if (path) void ctx.attachImage(path);
            return { consume: true };
        }
        if (isCtrlD(data) && !state.busy) {
            cleanExit(0);
            return { consume: true };
        }
        if (isCtrlC(data)) {
            if (state.busy) {
                state.abort.abort();
                state.abort = new AbortController();
                state.busy = false;
                hideWorking();
                queuedMessages.length = 0;
                renderPending();
                tui.requestRender();
                return { consume: true };
            }
            const now = Date.now();
            if (now - state.lastCtrlCAt < 1000) {
                cleanExit(130);
                return { consume: true };
            }
            state.lastCtrlCAt = now;
            history.addSystem("Press Ctrl+C again to quit.");
            tui.requestRender();
            return { consume: true };
        }
        if (isEsc(data) && state.busy) {
            state.abort.abort();
            state.abort = new AbortController();
            state.busy = false;
            hideWorking();
            tui.requestRender();
            return { consume: true };
        }
        return undefined;
    };
}
