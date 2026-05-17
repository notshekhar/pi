import { generateText } from "ai";
import { getModel } from "../providers";
import type { Session } from "../sessions";

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

export async function runCompact(opts: {
  session: Session;
  modelId: string;
  keepTurns?: number;
}): Promise<CompactResult> {
  const keep = opts.keepTurns ?? 4;
  const messages = opts.session.messages();
  const cut = Math.max(0, messages.length - keep);
  if (cut <= 0) {
    return { summary: "", cutAt: 0, tokensBefore: 0, tokensAfter: 0 };
  }

  const head = messages.slice(0, cut);
  const headText = head.map((m) => `[${m.role}] ${typeof m.content === "string" ? m.content : JSON.stringify(m.content)}`).join("\n");
  const tokensBefore = estimateTokens(headText);

  const model = await getModel(opts.modelId);
  const { text } = await generateText({
    model,
    system: COMPACT_PROMPT,
    prompt: headText,
  });

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
