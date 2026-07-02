import type { UsageBlock } from "../types";

/**
 * One flat, fully-resolved view of a UsageBlock. ai-sdk v7 reports token
 * detail nested under inputTokenDetails/outputTokenDetails; sessions persisted
 * under v6 carry flat cachedInputTokens/reasoningTokens instead. This helper
 * is the single place that precedence lives — cost math, the entry usage
 * columns, and display must all read usage through it, never the raw block.
 */
export interface NormalizedUsage {
    input?: number;
    output?: number;
    total?: number;
    noCache?: number;
    cacheRead?: number;
    cacheWrite?: number;
    text?: number;
    reasoning?: number;
    /** Provider-reported dollar cost (openrouter), when present. */
    cost?: number;
    estimated: boolean;
}

export function normalizeUsage(u: UsageBlock): NormalizedUsage {
    return {
        input: u.inputTokens,
        output: u.outputTokens,
        total: u.totalTokens,
        noCache: u.inputTokenDetails?.noCacheTokens,
        cacheRead: u.inputTokenDetails?.cacheReadTokens ?? u.cachedInputTokens,
        cacheWrite: u.inputTokenDetails?.cacheWriteTokens,
        text: u.outputTokenDetails?.textTokens,
        reasoning: u.outputTokenDetails?.reasoningTokens ?? u.reasoningTokens,
        cost: u.cost,
        estimated: u.estimated === true,
    };
}
