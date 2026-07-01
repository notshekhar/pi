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
    const outDetails =
        a.outputTokenDetails || b.outputTokenDetails
            ? {
                  textTokens: n(a.outputTokenDetails?.textTokens, b.outputTokenDetails?.textTokens),
                  reasoningTokens: n(a.outputTokenDetails?.reasoningTokens, b.outputTokenDetails?.reasoningTokens),
              }
            : undefined;
    // v7 reports reasoning tokens nested under outputTokenDetails; old (v6)
    // sessions used the flat field. Keep the flat field in sync off either.
    const reasoning = n(
        a.outputTokenDetails?.reasoningTokens ?? a.reasoningTokens,
        b.outputTokenDetails?.reasoningTokens ?? b.reasoningTokens,
    );
    return {
        inputTokens: n(a.inputTokens, b.inputTokens),
        outputTokens: n(a.outputTokens, b.outputTokens),
        totalTokens: n(a.totalTokens, b.totalTokens),
        cachedInputTokens: n(
            a.inputTokenDetails?.cacheReadTokens ?? a.cachedInputTokens,
            b.inputTokenDetails?.cacheReadTokens ?? b.cachedInputTokens,
        ),
        reasoningTokens: reasoning,
        cost: n(a.cost, b.cost),
        inputTokenDetails: details,
        outputTokenDetails: outDetails,
    };
}

/** A stored number, defended against a corrupt store (null/NaN/Infinity would
 * otherwise poison every later accumulation and get written back forever). */
function num(x: unknown): number {
    return typeof x === "number" && Number.isFinite(x) ? x : 0;
}

/** Context size implied by a usage block — provider total when present, else the sum. */
function ctxFromUsage(u: UsageBlock | undefined): number {
    if (!u) return 0;
    if (typeof u.totalTokens === "number" && u.totalTokens > 0) return u.totalTokens;
    return (u.inputTokens ?? 0) + (u.outputTokens ?? 0) + (u.cachedInputTokens ?? 0);
}

export class CostTracker {
    private session: CostBreakdown = { inputTokens: 0, outputTokens: 0, cachedInputTokens: 0, usd: 0 };
    /** Sticky once any estimated (interrupted-turn) usage lands in the session
     * total — drives the leading `~` in format()/sessionBreakdown(). */
    private estimated = false;
    /** Persist to the lifetime/daily/cwd store (default). Tests pass false so
     * exercising add() never touches the user's real ~/.loop/cost.json. */
    private readonly persist: boolean;

    constructor(opts: { persist?: boolean } = {}) {
        this.persist = opts.persist !== false;
    }

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
        if (!this.persist) return { ...this.session };

        // Fresh read + single atomic write per step. The re-read matters:
        // another loop instance accrues into the same file, and accumulating
        // onto a stale cache would overwrite its spend (lost update).
        costStore.refresh();
        const all = costStore.all as {
            lifetime?: { usd: number; byProvider: Record<string, number> };
            daily?: Record<string, number>;
            byCwd?: Record<string, number>;
        };
        const lifetime = all.lifetime ?? { usd: 0, byProvider: {} };
        lifetime.usd = num(lifetime.usd) + usd;
        lifetime.byProvider[provider] = num(lifetime.byProvider[provider]) + usd;
        all.lifetime = lifetime;

        // Daily + per-directory buckets power /cost's "today / 7d / month / here"
        // views. Only accrues from now on — pre-existing lifetime spend has no
        // time/cwd attribution to recover.
        if (usd > 0) {
            const daily = all.daily ?? {};
            daily[dayKey()] = num(daily[dayKey()]) + usd;
            all.daily = daily;
            if (cwd) {
                const byCwd = all.byCwd ?? {};
                byCwd[cwd] = num(byCwd[cwd]) + usd;
                all.byCwd = byCwd;
            }
        }
        costStore.all = all;

        return { ...this.session };
    }

    /**
     * Accumulate an *estimated* usage block into the session total only — never
     * the persistent lifetime/daily/cwd store. Used for the in-flight request
     * of an interrupted turn, whose real usage the AI SDK never reports
     * (vercel/ai#7805). Sets the `estimated` flag so the footer shows a `~`.
     */
    addEstimated(modelId: string, usage: UsageBlock, _cwd?: string): CostBreakdown {
        const { provider } = parseModelId(modelId);
        this.accumulateSession(modelId, provider, usage);
        this.estimated = true;
        return { ...this.session, estimated: true };
    }

    stats(cwd?: string): CostStats {
        // /cost is user-triggered and must reflect other live instances' spend.
        costStore.refresh();
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
        let lastAssistantUsage: UsageBlock | undefined;
        for (const e of session.entries()) {
            if (e.type === "message" && e.role === "assistant" && e.usage) {
                usages.push({ usage: e.usage, model: e.model ?? session.info.model });
                lastAssistantUsage = e.usage;
            } else if (e.type === "subagent" && e.usage) {
                usages.push({ usage: e.usage, model: e.model ?? session.info.model });
            }
        }
        this.seedFromEntries(session.info.model, usages);
        // Ctx meter tracks the MAIN conversation: a subagent's usage counts
        // toward cost, but its context is separate — if the transcript ends on
        // a subagent entry (e.g. aborted mid-task), the meter must not adopt
        // that subagent's context size.
        return { ctxTokens: ctxFromUsage(lastAssistantUsage) };
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
            if (usage.estimated) this.estimated = true;
            last = usage;
        }
        return { ctxTokens: ctxFromUsage(last) };
    }

    sessionBreakdown(): CostBreakdown {
        return { ...this.session, estimated: this.estimated };
    }

    lifetimeBreakdown(): { usd: number; byProvider: Record<ProviderId, number> } {
        const l = costStore.get("lifetime") as { usd?: number; byProvider?: Record<ProviderId, number> } | undefined;
        // Defensive copy — the store's cached object must not be mutable by callers.
        return { usd: num(l?.usd), byProvider: { ...(l?.byProvider ?? {}) } as Record<ProviderId, number> };
    }

    reset(): void {
        this.session = { inputTokens: 0, outputTokens: 0, cachedInputTokens: 0, usd: 0 };
        this.estimated = false;
    }

    format(): string {
        const s = this.session;
        // Leading `~` flags that the session total includes an estimated
        // (interrupted-turn) amount, so the figure isn't read as exact.
        const prefix = this.estimated ? "~" : "";
        return `${prefix}$${s.usd.toFixed(4)} · in:${s.inputTokens} out:${s.outputTokens} cache:${s.cachedInputTokens}`;
    }
}
