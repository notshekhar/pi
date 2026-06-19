import type { CommandContext } from "@notshekhar/loop-core";
import type { AppDeps } from "./deps";
import type { AppState } from "./state";
import { isCtrlC, isCtrlD, isCtrlE, isCtrlI, isCtrlL, isCtrlV, isEsc, isShiftTab, isTab } from "./keys";
import { isKeyRelease } from "@notshekhar/loop-tui";
import { traceEvent } from "./debug-log";
import { pickImageFile, readClipboardImageToFile } from "./clipboard-image";
import { agentExists, extractImagesFromInput, getModelSync, listAgents, settingsStore } from "@notshekhar/loop-core";

export type InputListener = (data: string) => { consume: boolean } | undefined;

// Terminals deliver both clipboard pastes and file drag-and-drop as a bracketed
// paste: ESC[200~ <content> ESC[201~ (the TUI re-wraps its paste event this way).
const BRACKETED_PASTE = /^\x1b\[200~([\s\S]*)\x1b\[201~$/;

/** Whether the active model can actually accept image attachments. */
function modelAcceptsImages(modelId: string): boolean {
    const modalities = getModelSync(modelId)?.modalities;
    // Unknown modalities → assume images are fine (don't block on missing info).
    return !Array.isArray(modalities) || modalities.includes("image");
}

/**
 * If a paste / drag-and-drop is *purely* image file path(s) — nothing but the
 * path(s), modulo surrounding whitespace — return those paths so we can turn
 * them into attachments instead of dropping a raw, shell-escaped path into the
 * editor. Mixed pastes ("look at ./a.png") return null and fall through to the
 * editor untouched; submit-time extraction still handles those.
 */
function droppedImagePaths(data: string, cwd: string): string[] | null {
    const paste = BRACKETED_PASTE.exec(data);
    if (!paste) return null;
    const { textWithoutPaths, images } = extractImagesFromInput(paste[1], cwd);
    if (images.length === 0 || textWithoutPaths.trim() !== "") return null;
    return images.map((img) => img.path);
}

export function createInputHandler(state: AppState, deps: AppDeps, ctx: CommandContext): InputListener {
    const { tui, history, queuedMessages, renderPending, hideWorking, cleanExit, editor, footer } = deps;

    return (data) => {
        // Trace raw input: shows the press/release pair, modifiers, and which
        // chord (if any) a keystroke resolves to — the view that exposes
        // double-fire / event-ordering bugs.
        traceEvent(
            "input",
            `${JSON.stringify(data)} release=${isKeyRelease(data)} esc=${isEsc(data)} busy=${state.busy} q=${queuedMessages.length}`,
        );

        // Under the Kitty keyboard protocol a single physical keypress emits BOTH
        // a press and a release event, and our chord matchers (isEsc, isCtrlC, …)
        // match the codepoint regardless of event type. The TUI filters releases
        // before the focused component, but input listeners run earlier, so a
        // lone Esc would fire the interrupt twice — the release firing lands on
        // the *next* (drained) turn and kills it. Drop releases here; every chord
        // below is a press-only action. (isKeyRelease excludes bracketed-paste.)
        if (isKeyRelease(data)) return undefined;

        // Selectors (e.g. /tree) own ctrl-key chords like ctrl+l/ctrl+d while
        // focused — global shortcuts that would shadow them only fire when the
        // editor has focus.
        const editorFocused = (editor as unknown as { focused?: boolean }).focused === true;
        // Drag-and-drop / paste of image file(s) into the prompt: attach them
        // (insert clean [image:…] tokens) instead of pasting the raw path text.
        // Editor-focused only, so it never fires while a selector owns input, and
        // only when the model accepts images — otherwise let the path paste as
        // plain text rather than swallowing it into an attachment it can't use.
        if (editorFocused && modelAcceptsImages(state.modelId)) {
            const dropped = droppedImagePaths(data, state.cwd);
            if (dropped) {
                for (const path of dropped) void ctx.attachImage(path);
                return { consume: true };
            }
        }
        // Agent cycling: Shift+Tab and plain Tab both cycle, even with text in
        // the prompt (files are reached via the "@"/"#"
        // triggers, not Tab). With the autocomplete popup open, Tab still
        // applies the selected completion. Cycle = active custom agent (if
        // selected via /agents) plus all built-ins.
        const wantsAgentCycle = isShiftTab(data) || (isTab(data) && !editor.isShowingAutocomplete());
        if (wantsAgentCycle && editorFocused) {
            // Cycle = visible built-ins, plus the one extra agent the user has
            // opted into (a custom agent or a revealed hidden built-in like
            // data-analyst). Hidden built-ins stay out until selected.
            const cycle = [
                ...(state.cycleCustomAgent && agentExists(state.cycleCustomAgent) ? [state.cycleCustomAgent] : []),
                ...listAgents()
                    .filter((a) => a.builtin && !a.hidden)
                    .map((a) => a.name),
            ];
            const next = cycle[(cycle.indexOf(state.agent) + 1) % cycle.length];
            state.agent = next;
            settingsStore.set("agent", next);
            footer.setAgent(next);
            tui.requestRender();
            return { consume: true };
        }
        if (isCtrlL(data) && editorFocused) {
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
        if (isCtrlD(data) && !state.busy && editorFocused) {
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
