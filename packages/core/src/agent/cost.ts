import { costStore } from "../auth/storage";
import { getModelSync } from "../catalog";
import type { CostBreakdown, ProviderId, UsageBlock } from "../types";
import type { Session } from "../sessions";
import { parseModelId } from "../providers";

export interface CostStats {
    lifetimeUsd: number;
    byProvider: Record<string, number>;
    todayUsd: number;
    last7Usd: number;
    monthUsd: number;
    cwdUsd: number;
}

/** Local-timezone YYYY-MM-DD — buckets should roll over at the user's midnight. */
function dayKey(d = new Date()): string {
    return d.toLocaleDateString("sv");
}

/** Sum two usage blocks field-wise — used to keep a running per-step total so
 * aborted runs can still persist the usage of the steps that completed. */
export function sumUsage(a: UsageBlock | undefined, b: UsageBlock): UsageBlock {
    if (!a) return { ...b, inputTokenDetails: b.inputTokenDetails ? { ...b.inputTokenDetails } : undefined };
    const n = (x?: number, y?: number) => (x === undefined && y === undefined ? undefined : (x ?? 0) + (y ?? 0));
    const details =
        a.inputTokenDetails || b.inputTokenDetails
            ? {
                  noCacheTokens: n(a.inputTokenDetails?.noCacheTokens, b.inputTokenDetails?.noCacheTokens),
                  cacheReadTokens: n(a.inputTokenDetails?.cacheReadTokens, b.inputTokenDetails?.cacheReadTokens),
                  cacheWriteTokens: n(a.inputTokenDetails?.cacheWriteTokens, b.inputTokenDetails?.cacheWriteTokens),
              }
            : undefined;
    return {
        inputTokens: n(a.inputTokens, b.inputTokens),
        outputTokens: n(a.outputTokens, b.outputTokens),
        totalTokens: n(a.totalTokens, b.totalTokens),
        cachedInputTokens: n(a.cachedInputTokens, b.cachedInputTokens),
        reasoningTokens: n(a.reasoningTokens, b.reasoningTokens),
        cost: n(a.cost, b.cost),
        inputTokenDetails: details,
    };
}

export class CostTracker {
    private session: CostBreakdown = { inputTokens: 0, outputTokens: 0, cachedInputTokens: 0, usd: 0 };

    private computeUsd(modelId: string, provider: string, usage: UsageBlock): number {
        if (typeof usage.cost === "number" && provider === "openrouter") return usage.cost;
        const model = getModelSync(modelId);
        if (!model) return 0;
        const inTok = usage.inputTokens ?? 0;
        const cacheTok = usage.inputTokenDetails?.cacheReadTokens ?? usage.cachedInputTokens ?? 0;
        const cacheWriteTok = usage.inputTokenDetails?.cacheWriteTokens ?? 0;
        const billedIn = usage.inputTokenDetails?.noCacheTokens ?? Math.max(0, inTok - cacheTok - cacheWriteTok);
        return (
            (billedIn / 1_000_000) * model.cost.input +
            ((usage.outputTokens ?? 0) / 1_000_000) * model.cost.output +
            (cacheTok / 1_000_000) * model.cost.cacheRead +
            (cacheWriteTok / 1_000_000) * model.cost.cacheWrite
        );
    }

    private accumulateSession(modelId: string, provider: string, usage: UsageBlock): number {
        const usd = this.computeUsd(modelId, provider, usage);
        this.session.inputTokens += usage.inputTokens ?? 0;
        this.session.outputTokens += usage.outputTokens ?? 0;
        this.session.cachedInputTokens += usage.inputTokenDetails?.cacheReadTokens ?? usage.cachedInputTokens ?? 0;
        this.session.usd += usd;
        return usd;
    }

    add(modelId: string, usage: UsageBlock, cwd?: string): CostBreakdown {
        const { provider } = parseModelId(modelId);
        const usd = this.accumulateSession(modelId, provider, usage);

        // Single read + single atomic write — configstore re-reads and
        // rewrites the whole file on every get/set, and add() runs per step.
        const all = costStore.all as {
            lifetime?: { usd: number; byProvider: Record<string, number> };
            daily?: Record<string, number>;
            byCwd?: Record<string, number>;
        };
        const lifetime = all.lifetime ?? { usd: 0, byProvider: {} };
        lifetime.usd = (lifetime.usd ?? 0) + usd;
        lifetime.byProvider[provider] = (lifetime.byProvider[provider] ?? 0) + usd;
        all.lifetime = lifetime;

        // Daily + per-directory buckets power /cost's "today / 7d / month / here"
        // views. Only accrues from now on — pre-existing lifetime spend has no
        // time/cwd attribution to recover.
        if (usd > 0) {
            const daily = all.daily ?? {};
            daily[dayKey()] = (daily[dayKey()] ?? 0) + usd;
            all.daily = daily;
            if (cwd) {
                const byCwd = all.byCwd ?? {};
                byCwd[cwd] = (byCwd[cwd] ?? 0) + usd;
                all.byCwd = byCwd;
            }
        }
        costStore.all = all;

        return { ...this.session };
    }

    stats(cwd?: string): CostStats {
        const all = costStore.all as {
            lifetime?: { usd: number; byProvider: Record<string, number> };
            daily?: Record<string, number>;
            byCwd?: Record<string, number>;
        };
        const lifetime = all.lifetime ?? { usd: 0, byProvider: {} };
        const daily = all.daily ?? {};
        const byCwd = all.byCwd ?? {};

        const today = dayKey();
        const last7Keys = new Set(Array.from({ length: 7 }, (_, i) => dayKey(new Date(Date.now() - i * 86_400_000))));
        const monthPrefix = today.slice(0, 7);

        let last7Usd = 0;
        let monthUsd = 0;
        for (const [day, usd] of Object.entries(daily)) {
            if (last7Keys.has(day)) last7Usd += usd;
            if (day.startsWith(monthPrefix)) monthUsd += usd;
        }

        return {
            lifetimeUsd: lifetime.usd ?? 0,
            byProvider: lifetime.byProvider ?? {},
            todayUsd: daily[today] ?? 0,
            last7Usd,
            monthUsd,
            cwdUsd: cwd ? (byCwd[cwd] ?? 0) : 0,
        };
    }

    /**
     * Rebuild session totals from a resumed transcript (assistant turns +
     * subagent runs). Session-only: lifetime/daily/cwd stores were billed
     * when those turns actually ran. Returns the last turn's token count
     * for the ctx meter.
     */
    seedFromSession(session: Session): { ctxTokens: number } {
        // Each usage is priced with the model that produced it (stamped on the
        // entry), falling back to the session model for older/unstamped entries.
        // This keeps cost correct across a mid-session model switch.
        const usages: { usage: UsageBlock; model: string }[] = [];
        for (const e of session.entries()) {
            if (e.type === "message" && e.role === "assistant" && e.usage) {
                usages.push({ usage: e.usage, model: e.model ?? session.info.model });
            } else if (e.type === "subagent" && e.usage) {
                usages.push({ usage: e.usage, model: e.model ?? session.info.model });
            }
        }
        return this.seedFromEntries(session.info.model, usages);
    }

    seedFromEntries(
        fallbackModelId: string,
        usages: UsageBlock[] | { usage: UsageBlock; model: string }[],
    ): { ctxTokens: number } {
        this.reset();
        let last: UsageBlock | undefined;
        for (const item of usages) {
            // Accept either a bare usage (legacy callers) or { usage, model }.
            const usage = "usage" in item ? item.usage : item;
            const modelId = "usage" in item ? item.model : fallbackModelId;
            const { provider } = parseModelId(modelId);
            this.accumulateSession(modelId, provider, usage);
            last = usage;
        }
        const ctxTokens = last
            ? typeof last.totalTokens === "number" && last.totalTokens > 0
                ? last.totalTokens
                : (last.inputTokens ?? 0) + (last.outputTokens ?? 0) + (last.cachedInputTokens ?? 0)
            : 0;
        return { ctxTokens };
    }

    sessionBreakdown(): CostBreakdown {
        return { ...this.session };
    }

    lifetimeBreakdown(): { usd: number; byProvider: Record<ProviderId, number> } {
        return costStore.get("lifetime") as { usd: number; byProvider: Record<ProviderId, number> };
    }

    reset(): void {
        this.session = { inputTokens: 0, outputTokens: 0, cachedInputTokens: 0, usd: 0 };
    }

    format(): string {
        const s = this.session;
        return `$${s.usd.toFixed(4)} · in:${s.inputTokens} out:${s.outputTokens} cache:${s.cachedInputTokens}`;
    }
}
