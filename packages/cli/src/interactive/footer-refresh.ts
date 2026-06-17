import { getModelSync, type CostTracker, type UsageBlock } from "@notshekhar/pi-core";
import type { TUI } from "@notshekhar/pi-tui";
import type { CostFooter } from "./components/cost-footer";
import type { AppState } from "./state";

/** Prefer the provider's reported total; otherwise sum the parts. */
function ctxTokensFromUsage(u: UsageBlock): number {
    if (typeof u.totalTokens === "number" && u.totalTokens > 0) return u.totalTokens;
    return (u.inputTokens ?? 0) + (u.outputTokens ?? 0) + (u.cachedInputTokens ?? 0);
}

export interface FooterRefresher {
    /** Update cost + context and repaint. */
    refreshFooter(usage?: UsageBlock): void;
    /** Update only the context gauge (no cost, no repaint). */
    refreshFooterCtx(usage?: UsageBlock): void;
}

export function createFooterRefresher(
    footer: CostFooter,
    tracker: CostTracker,
    tui: TUI,
    state: AppState,
): FooterRefresher {
    function refreshFooterCtx(usage?: UsageBlock): void {
        if (usage) state.latestContextTokens = ctxTokensFromUsage(usage);
        const info = getModelSync(state.modelId);
        footer.setContext(state.latestContextTokens, info?.contextWindow ?? 0);
    }

    function refreshFooter(usage?: UsageBlock): void {
        footer.setCost(tracker.format());
        refreshFooterCtx(usage);
        tui.requestRender();
    }

    return { refreshFooter, refreshFooterCtx };
}
