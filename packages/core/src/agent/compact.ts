import { generateText } from "ai";
import { getModel } from "../providers";
import type { Session } from "../sessions";
import { isAbortError } from "./abort";
import { BRANCH_SUMMARY_PREAMBLE } from "./branch-summary";

export const COMPACTION_SUMMARY_PREFIX = `The conversation history before this point was compacted into the following summary:

<summary>
`;
export const COMPACTION_SUMMARY_SUFFIX = `
</summary>`;

const COMPACT_PROMPT = `You are summarizing a developer's coding session. Produce a dense factual summary that preserves:
- User intent across the segment.
- Files touched (paths + nature of edits).
- Important tool outputs (errors, build results, test runs).
- Open questions and unresolved threads.

Do NOT add commentary. Use short bullet style.`;

export interface CompactResult {
    summary: string;
    cutAt: number;
    tokensBefore: number;
    tokensAfter: number;
}

function estimateTokens(text: string): number {
    // crude 4 chars/token
    return Math.ceil(text.length / 4);
}

export function latestCompactEntry(session: Session) {
    return latestCompact(session);
}

function latestCompact(session: Session) {
    // Path-based: a compaction on an abandoned branch must not apply after
    // /tree navigation moved the leaf elsewhere.
    let latest: { summary: string; cutAt: number; ts: number; tokensBefore: number; tokensAfter: number } | undefined;
    for (const entry of session.getBranch()) {
        if (entry.type === "compact") latest = entry;
    }
    return latest;
}

function messageToText(message: { role: "user" | "assistant" | "tool"; content: unknown }): string {
    return `[${message.role}] ${typeof message.content === "string" ? message.content : JSON.stringify(message.content)}`;
}

export function compactedContextMessages(
    session: Session,
): Array<{ role: "user" | "assistant" | "tool"; content: unknown }> {
    const messages = session.messages();
    const compact = latestCompact(session);
    if (!compact) return messages;

    const summary = `${COMPACTION_SUMMARY_PREFIX}${compact.summary}${COMPACTION_SUMMARY_SUFFIX}`;
    return [{ role: "user", content: summary }, ...messages.slice(compact.cutAt)];
}

export type ContextEntry =
    | { kind: "message"; role: "user" | "assistant" | "tool"; content: unknown }
    | { kind: "subagent"; agent: string; result: string };

/**
 * Like compactedContextMessages, but keeps subagent entries interleaved in
 * chronological order so resumed sessions retain subagent reports in the
 * model context. The compact cutAt counts only message entries — subagent
 * entries ride along with the messages that survive the cut.
 *
 * Walks the current branch path (leaf → root), not the whole file, so
 * abandoned branches stay out of the context after /tree navigation.
 * Branch-summary entries on the path join the context as user messages.
 */
export function compactedContextEntries(session: Session): ContextEntry[] {
    const compact = latestCompact(session);
    const out: ContextEntry[] = [];
    let messageIndex = 0;
    for (const e of session.getBranch()) {
        if (e.type === "message") {
            const idx = messageIndex++;
            if (compact && idx < compact.cutAt) continue;
            out.push({ kind: "message", role: e.role, content: e.content });
        } else if (e.type === "subagent") {
            if (compact && messageIndex < compact.cutAt) continue;
            out.push({ kind: "subagent", agent: e.agent, result: e.result });
        } else if (e.type === "branch-summary" && e.summary) {
            if (compact && messageIndex < compact.cutAt) continue;
            out.push({ kind: "message", role: "user", content: `${BRANCH_SUMMARY_PREAMBLE}${e.summary}` });
        }
    }
    if (compact) {
        const summary = `${COMPACTION_SUMMARY_PREFIX}${compact.summary}${COMPACTION_SUMMARY_SUFFIX}`;
        out.unshift({ kind: "message", role: "user", content: summary });
    }
    return out;
}

export class CompactAbortedError extends Error {
    constructor() {
        super("compact aborted");
        this.name = "CompactAbortedError";
    }
}

export async function runCompact(opts: {
    session: Session;
    modelId: string;
    keepTurns?: number;
    abortSignal?: AbortSignal;
}): Promise<CompactResult> {
    const keep = opts.keepTurns ?? 4;
    const messages = opts.session.messages();
    const previousCompact = latestCompact(opts.session);
    const previousCut = previousCompact?.cutAt ?? 0;
    const cut = Math.max(previousCut, messages.length - keep);
    if (cut <= previousCut) {
        return { summary: "", cutAt: 0, tokensBefore: 0, tokensAfter: 0 };
    }

    if (opts.abortSignal?.aborted) throw new CompactAbortedError();

    const head = messages.slice(previousCut, cut);
    const previousSummary = previousCompact
        ? `${COMPACTION_SUMMARY_PREFIX}${previousCompact.summary}${COMPACTION_SUMMARY_SUFFIX}\n`
        : "";
    const headText = previousSummary + head.map(messageToText).join("\n");
    const fullContextText = previousSummary + messages.slice(previousCut).map(messageToText).join("\n");
    const tokensBefore = estimateTokens(fullContextText);

    const model = await getModel(opts.modelId);
    let text: string;
    try {
        const result = await generateText({
            model,
            instructions: COMPACT_PROMPT,
            prompt: headText,
            abortSignal: opts.abortSignal,
        });
        text = result.text;
    } catch (err) {
        if (isAbortError(err) || opts.abortSignal?.aborted) throw new CompactAbortedError();
        throw err;
    }

    if (opts.abortSignal?.aborted) throw new CompactAbortedError();

    const tokensAfter = estimateTokens(text);
    await opts.session.append({
        type: "compact",
        ts: Date.now(),
        summary: text,
        cutAt: cut,
        tokensBefore,
        tokensAfter,
    });
    return { summary: text, cutAt: cut, tokensBefore, tokensAfter };
}
