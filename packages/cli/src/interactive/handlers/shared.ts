/** Small helpers shared across the handler modules. */
import type { AppDeps } from "../deps";
import type { AppState } from "../state";
import { renderSessionBranch } from "../replay";

/** Flatten message content (string or text-part array) to plain text. */
export function extractText(content: unknown): string {
    if (typeof content === "string") return content;
    if (Array.isArray(content)) {
        return content
            .filter((c): c is { type: "text"; text: string } => !!c && typeof c === "object" && c.type === "text")
            .map((c) => c.text)
            .join("");
    }
    return "";
}

/** True (and a notice rendered) when a turn is running and the action must wait. */
export function rejectWhileBusy(state: AppState, deps: AppDeps): boolean {
    if (!state.busy) return false;
    deps.history.addSystem("busy; finish or abort current turn first");
    deps.tui.requestRender();
    return true;
}

/** Re-render the chat from the session's current branch path. */
export function replayCurrentBranch(state: AppState, deps: AppDeps): void {
    deps.history.reset();
    if (state.session) renderSessionBranch(state.session, deps.history, state.modelId);
}
