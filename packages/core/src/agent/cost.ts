import { costStore } from "../auth/storage";
import { getModelSync } from "../catalog";
import type { CostBreakdown, ProviderId, UsageBlock } from "../types";
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

        const lifetime = costStore.get("lifetime") as { usd: number; byProvider: Record<string, number> };
        lifetime.usd = (lifetime.usd ?? 0) + usd;
        lifetime.byProvider[provider] = (lifetime.byProvider[provider] ?? 0) + usd;
        costStore.set("lifetime", lifetime);

        // Daily + per-directory buckets power /cost's "today / 7d / month / here"
        // views. Only accrues from now on — pre-existing lifetime spend has no
        // time/cwd attribution to recover.
        if (usd > 0) {
            const daily = (costStore.get("daily") as Record<string, number> | undefined) ?? {};
            daily[dayKey()] = (daily[dayKey()] ?? 0) + usd;
            costStore.set("daily", daily);
            if (cwd) {
                const byCwd = (costStore.get("byCwd") as Record<string, number> | undefined) ?? {};
                byCwd[cwd] = (byCwd[cwd] ?? 0) + usd;
                costStore.set("byCwd", byCwd);
            }
        }

        return { ...this.session };
    }

    stats(cwd?: string): CostStats {
        const lifetime = costStore.get("lifetime") as { usd: number; byProvider: Record<string, number> };
        const daily = (costStore.get("daily") as Record<string, number> | undefined) ?? {};
        const byCwd = (costStore.get("byCwd") as Record<string, number> | undefined) ?? {};

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
     * Rebuild session totals from a resumed transcript's usage entries.
     * Session-only: lifetime/daily/cwd stores were billed when those turns
     * actually ran. Returns the last turn's token count for the ctx meter.
     */
    seedFromEntries(modelId: string, usages: UsageBlock[]): { ctxTokens: number } {
        this.reset();
        const { provider } = parseModelId(modelId);
        let last: UsageBlock | undefined;
        for (const u of usages) {
            this.accumulateSession(modelId, provider, u);
            last = u;
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
