import { getModelSync, type CostTracker, type UsageBlock } from "@notshekhar/loop-core";
import type { TUI } from "@notshekhar/loop-tui";
import type { StatusLine } from "./components/status-line";
import type { AppState } from "./state";

/** Prefer the provider's reported total; otherwise sum the parts. */
function ctxTokensFromUsage(u: UsageBlock): number {
    if (typeof u.totalTokens === "number" && u.totalTokens > 0) return u.totalTokens;
    return (u.inputTokens ?? 0) + (u.outputTokens ?? 0) + (u.cachedInputTokens ?? 0);
}

export interface StatusLineRefresher {
    /** Update cost + context and repaint. */
    refreshStatusLine(usage?: UsageBlock): void;
    /** Update only the context gauge (no cost, no repaint). */
    refreshStatusLineCtx(usage?: UsageBlock): void;
}

export function createStatusLineRefresher(
    statusLine: StatusLine,
    tracker: CostTracker,
    tui: TUI,
    state: AppState,
): StatusLineRefresher {
    function refreshStatusLineCtx(usage?: UsageBlock): void {
        if (usage) state.latestContextTokens = ctxTokensFromUsage(usage);
        const info = getModelSync(state.modelId);
        statusLine.setContext(state.latestContextTokens, info?.contextWindow ?? 0);
    }

    function refreshStatusLine(usage?: UsageBlock): void {
        statusLine.setCost(tracker.format());
        statusLine.setCostData(tracker.sessionBreakdown());
        refreshStatusLineCtx(usage);
        tui.requestRender();
    }

    return { refreshStatusLine, refreshStatusLineCtx };
}
