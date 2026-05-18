import { generateText } from "ai";
import { getModel } from "../providers";
import type { Session } from "../sessions";

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

function latestCompact(session: Session) {
  let latest: { summary: string; cutAt: number; ts: number; tokensBefore: number; tokensAfter: number } | undefined;
  for (const entry of session.entries()) {
    if (entry.type === "compact") latest = entry;
  }
  return latest;
}

function messageToText(message: { role: "user" | "assistant" | "tool"; content: unknown }): string {
  return `[${message.role}] ${typeof message.content === "string" ? message.content : JSON.stringify(message.content)}`;
}

export function compactedContextMessages(session: Session): Array<{ role: "user" | "assistant" | "tool"; content: unknown }> {
  const messages = session.messages();
  const compact = latestCompact(session);
  if (!compact) return messages;

  const summary = `${COMPACTION_SUMMARY_PREFIX}${compact.summary}${COMPACTION_SUMMARY_SUFFIX}`;
  return [{ role: "user", content: summary }, ...messages.slice(compact.cutAt)];
}

export class CompactAbortedError extends Error {
  constructor() {
    super("compact aborted");
    this.name = "CompactAbortedError";
  }
}

function isAbortError(err: unknown): boolean {
  if (!err) return false;
  const e = err as { name?: string; message?: string };
  return e.name === "AbortError" || /aborted/i.test(e.message ?? "");
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
      system: COMPACT_PROMPT,
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
