import { streamText, stepCountIs } from "ai";
import type { ModelMessage } from "ai";
import { EventEmitter } from "node:events";
import { getModel } from "../providers";
import { getCatalog } from "../catalog";
import { settingsStore } from "../auth/storage";
import { createTools } from "../tools";
import { buildSystemPrompt } from "./system-prompt";
import { loadWorkspaceContext } from "./context";
import { CostTracker } from "./cost";
import { runCompact } from "./compact";
import type { Session } from "../sessions";
import type { UsageBlock } from "../types";

export { CostTracker } from "./cost";
export { runCompact } from "./compact";
export { loadWorkspaceContext, watchWorkspaceContext } from "./context";

export interface RunTurnOptions {
  session: Session;
  modelId: string;
  userInput: string;
  cwd: string;
  abortSignal?: AbortSignal;
  tracker: CostTracker;
  emitter: EventEmitter;
  maxSteps?: number;
}

function estimateContextTokens(messages: ModelMessage[]): number {
  let total = 0;
  for (const m of messages) {
    total += Math.ceil(JSON.stringify(m).length / 4);
  }
  return total;
}

function toModelMessages(session: Session): ModelMessage[] {
  const out: ModelMessage[] = [];
  for (const m of session.messages()) {
    if (m.role === "tool") continue;
    out.push({ role: m.role as "user" | "assistant", content: typeof m.content === "string" ? m.content : JSON.stringify(m.content) });
  }
  return out;
}

export async function runTurn(opts: RunTurnOptions): Promise<void> {
  const { session, modelId, userInput, cwd, abortSignal, tracker, emitter } = opts;
  const maxSteps = opts.maxSteps ?? (settingsStore.get("maxSteps") as number) ?? 32;

  await session.append({ type: "message", ts: Date.now(), role: "user", content: userInput });

  // auto-compact check
  const catalog = await getCatalog();
  const modelInfo = catalog[modelId];
  const threshold = (settingsStore.get("autoCompactThreshold") as number) ?? 0.8;
  if (modelInfo) {
    const messages = toModelMessages(session);
    const tokens = estimateContextTokens(messages);
    if (tokens > modelInfo.contextWindow * threshold) {
      emitter.emit("compact-start", { reason: "auto" });
      const result = await runCompact({ session, modelId });
      emitter.emit("compact-end", result);
    }
  }

  const workspaceContext =
    (settingsStore.get("workspaceContext") as boolean) !== false ? loadWorkspaceContext(cwd) : { text: "", files: [] };
  const system = buildSystemPrompt({ cwd, workspaceContext: workspaceContext.text });
  const tools = createTools({ cwd, abortSignal });
  const model = await getModel(modelId);

  const result = streamText({
    model,
    system,
    messages: toModelMessages(session),
    tools,
    stopWhen: stepCountIs(maxSteps),
    abortSignal,
  });

  let assistantText = "";
  let lastUsage: UsageBlock | undefined;

  for await (const part of result.fullStream) {
    if (abortSignal?.aborted) break;
    switch (part.type) {
      case "text-delta":
        assistantText += part.text;
        emitter.emit("text-delta", part.text);
        break;
      case "tool-call":
        emitter.emit("tool-call", part);
        break;
      case "tool-result":
        emitter.emit("tool-result", part);
        break;
      case "finish": {
        const u = (part as { totalUsage?: UsageBlock }).totalUsage;
        lastUsage = u;
        if (u) {
          const breakdown = tracker.add(modelId, u);
          emitter.emit("finish", { usage: u, breakdown });
        } else {
          emitter.emit("finish", { usage: undefined });
        }
        break;
      }
      case "error":
        emitter.emit("error", part.error);
        break;
    }
  }

  await session.append({
    type: "message",
    ts: Date.now(),
    role: "assistant",
    content: assistantText,
    usage: lastUsage,
  });
}
