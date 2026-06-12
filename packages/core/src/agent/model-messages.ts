/**
 * Session → model-message conversion and context-size estimation.
 * Pure transforms over the session transcript; no side effects.
 */
import type { ModelMessage, SystemModelMessage } from "ai";
import type { Session } from "../sessions";
import { compactedContextEntries, latestCompactEntry } from "./compact";

// Per-entry size cache for the chars/4 context heuristic. Session entries
// are append-only with stable object identity, so old entries are never
// re-stringified — the estimate stays O(new entries) per turn instead of
// O(whole history).
const entryCharCache = new WeakMap<object, number>();

export function estimateContextTokens(session: Session): number {
    const compact = latestCompactEntry(session);
    let chars = compact ? compact.summary.length + 200 : 0;
    let messageIndex = 0;
    for (const e of session.entries()) {
        if (e.type === "message") {
            const idx = messageIndex++;
            if (compact && idx < compact.cutAt) continue;
        } else if (e.type === "subagent") {
            if (compact && messageIndex < compact.cutAt) continue;
        } else {
            continue;
        }
        let n = entryCharCache.get(e);
        if (n === undefined) {
            n = JSON.stringify(e).length;
            entryCharCache.set(e, n);
        }
        chars += n;
    }
    return Math.ceil(chars / 4);
}

const ANTHROPIC_CACHE = { anthropic: { cacheControl: { type: "ephemeral" as const } } };

/** System message with a cache breakpoint — the cache prefix is tools →
 * system, so one anchor covers both. */
export function anthropicCachedSystem(system: string): SystemModelMessage {
    return { role: "system", content: system, providerOptions: ANTHROPIC_CACHE };
}

/**
 * Anthropic prompt caching: two ephemeral breakpoints (limit is 4).
 * One on the system message — the cache prefix is tools → system, so this
 * covers both. One on the last message so the whole conversation prefix is
 * a cache hit on the next turn (90% input-cost discount on reads).
 * Other providers (OpenAI, xAI, Google) cache automatically server-side.
 */
export function withAnthropicCaching(system: string, messages: ModelMessage[]): ModelMessage[] {
    const out: ModelMessage[] = [anthropicCachedSystem(system), ...messages];
    const last = out[out.length - 1];
    out[out.length - 1] = { ...last, providerOptions: ANTHROPIC_CACHE } as ModelMessage;
    return out;
}

/**
 * Per-step moving breakpoint for multi-step loops (prepareStep). Anthropic
 * only caches up to an explicit breakpoint, so without this every step of an
 * agent loop re-bills the whole accumulated context (tool results included)
 * at full input price — quadratic in steps. Re-anchoring the last message
 * each step makes step N+1 a cache read of everything step N sent.
 * The previous step's tail anchor is stripped (system anchors stay) so a
 * long run never exceeds Anthropic's 4-breakpoint-per-request limit.
 */
export function moveAnthropicCacheTail(messages: ModelMessage[]): ModelMessage[] {
    if (messages.length === 0) return messages;
    const out = messages.map((m) => {
        if (m.role === "system" || !m.providerOptions?.anthropic) return m;
        const { anthropic: _drop, ...rest } = m.providerOptions;
        const clean = { ...m, providerOptions: rest } as ModelMessage;
        if (Object.keys(rest).length === 0) delete (clean as { providerOptions?: unknown }).providerOptions;
        return clean;
    });
    const last = out[out.length - 1];
    if (last.role === "system") return out;
    out[out.length - 1] = { ...last, providerOptions: { ...last.providerOptions, ...ANTHROPIC_CACHE } } as ModelMessage;
    return out;
}

export function toModelMessages(session: Session): ModelMessage[] {
    const out: ModelMessage[] = [];
    for (const m of compactedContextEntries(session)) {
        // Subagent reports stay in the model context across resumes — the
        // main turn's text usually references them without repeating them.
        if (m.kind === "subagent") {
            out.push({ role: "assistant", content: `[subagent ${m.agent} report]\n${m.result}` });
            continue;
        }
        if (m.role === "tool") continue;
        const content = typeof m.content === "string" ? m.content : JSON.stringify(m.content);
        // Anthropic rejects empty text blocks ("text content blocks must be
        // non-empty") — aborted turns left empty assistant entries in older
        // transcripts, so filter on read, not just on write.
        if (content.trim() === "") continue;
        out.push({ role: m.role as "user" | "assistant", content });
    }
    return out;
}
