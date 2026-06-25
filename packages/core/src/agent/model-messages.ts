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
    // Branch-path walk: abandoned branches don't reach the model context.
    for (const e of session.getBranch()) {
        if (e.type === "message") {
            const idx = messageIndex++;
            if (compact && idx < compact.cutAt) continue;
        } else if (e.type === "subagent" || e.type === "branch-summary") {
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

/** Appended to an interrupted assistant turn so the next request's context
 * reflects that the user cut the previous answer off (and an empty aborted turn
 * is never silently dropped, leaving the user's question unanswered). */
const INTERRUPT_NOTE = "[The user interrupted this response before it finished.]";

export function toModelMessages(session: Session): ModelMessage[] {
    const out: ModelMessage[] = [];
    for (const m of compactedContextEntries(session)) {
        // Subagent reports stay in the model context across resumes — the
        // main turn's text usually references them without repeating them.
        if (m.kind === "subagent") {
            out.push({ role: "assistant", content: `[subagent ${m.agent} report]\n${m.result}` });
            continue;
        }
        // Tool results: structured AI-SDK content, passed back in full.
        if (m.role === "tool") {
            if (Array.isArray(m.content) && m.content.length > 0) {
                out.push({ role: "tool", content: m.content } as ModelMessage);
            }
            continue;
        }
        // Assistant/user with structured content (text + tool-call parts) pass
        // through as real parts; legacy string content stays a string. Anthropic
        // rejects empty text blocks, so drop empty string entries (older aborted
        // turns left these) — unless the turn was interrupted, where an empty
        // body becomes the interruption note so the model still sees the turn.
        if (typeof m.content === "string") {
            if (m.content.trim() === "") {
                if (m.interrupted) out.push({ role: "assistant", content: INTERRUPT_NOTE });
                continue;
            }
            const content = m.interrupted ? `${m.content}\n\n${INTERRUPT_NOTE}` : m.content;
            out.push({ role: m.role as "user" | "assistant", content });
        } else if (Array.isArray(m.content) && m.content.length > 0) {
            // Pass structured content through verbatim — text, reasoning (with
            // its preserved provider signature), and tool-call parts. Nothing
            // stripped; the SDK round-trips its own response messages. An
            // interrupted turn gets a trailing text note so the model knows the
            // answer was cut off.
            const content = m.interrupted
                ? [...(m.content as unknown[]), { type: "text", text: INTERRUPT_NOTE }]
                : m.content;
            out.push({ role: m.role as "user" | "assistant", content } as ModelMessage);
        } else if (m.interrupted) {
            // Interrupted before any reasoning/text streamed (empty content) —
            // still surface the interruption so the turn isn't a silent gap.
            out.push({ role: "assistant", content: INTERRUPT_NOTE });
        }
    }
    return out;
}
