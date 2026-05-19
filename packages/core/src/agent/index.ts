import { streamText, stepCountIs, smoothStream } from "ai";
import type { ModelMessage } from "ai";
import { EventEmitter } from "node:events";
import { getModel, parseModelId } from "../providers";
import { getCatalog } from "../catalog";
import { settingsStore } from "../auth/storage";
import { createTools } from "../tools";
import { buildSystemPrompt } from "./system-prompt";
import { loadWorkspaceContext } from "./context";
import { loadProjectSkills } from "./skills";
import { extractImagesFromInput } from "./images";
import { CostTracker } from "./cost";
import { compactedContextMessages, runCompact } from "./compact";
import { buildProviderOptions, type ThinkingLevel } from "./thinking";
import type { Session } from "../sessions";
import type { UsageBlock } from "../types";

export { CostTracker } from "./cost";
export { runCompact, CompactAbortedError } from "./compact";
export {
  THINKING_LEVELS,
  THINKING_LEVEL_DESCRIPTIONS,
  buildProviderOptions,
  type ThinkingLevel,
} from "./thinking";
export { loadWorkspaceContext, watchWorkspaceContext } from "./context";
export { loadProjectSkills, type Skill } from "./skills";

export interface RunTurnOptions {
  session: Session;
  modelId: string;
  userInput: string;
  cwd: string;
  abortSignal?: AbortSignal;
  tracker: CostTracker;
  emitter: EventEmitter;
  maxSteps?: number;
  thinkingLevel?: ThinkingLevel;
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
  for (const m of compactedContextMessages(session)) {
    if (m.role === "tool") continue;
    out.push({ role: m.role as "user" | "assistant", content: typeof m.content === "string" ? m.content : JSON.stringify(m.content) });
  }
  return out;
}

export async function runTurn(opts: RunTurnOptions): Promise<void> {
  const { session, modelId, userInput, cwd, abortSignal, tracker, emitter } = opts;
  const maxSteps = opts.maxSteps ?? (settingsStore.get("maxSteps") as number) ?? 32;

  // Extract any image paths from the user input → ai-sdk image parts
  const { textWithoutPaths, images } = extractImagesFromInput(userInput, cwd);
  if (images.length > 0) {
    emitter.emit("attached-images", images.map((i) => i.path));
  }
  // Persist user message verbatim (paths intact for reference in transcripts)
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
      try {
        const result = await runCompact({ session, modelId, abortSignal });
        emitter.emit("compact-end", result);
      } catch (err) {
        if (abortSignal?.aborted) {
          emitter.emit("compact-end", { summary: "", cutAt: 0, tokensBefore: 0, tokensAfter: 0, aborted: true });
          return;
        }
        throw err;
      }
    }
  }

  const workspaceContext =
    (settingsStore.get("workspaceContext") as boolean) !== false ? loadWorkspaceContext(cwd) : { text: "", files: [] };
  const skillsEnabled = (settingsStore.get("skills") as boolean) !== false;
  const skills = skillsEnabled ? loadProjectSkills(cwd) : { skills: [], diagnostics: [], promptBlock: "" };

  const { provider } = parseModelId(modelId);
  const system = buildSystemPrompt({ cwd, workspaceContext: workspaceContext.text }) + (skills.promptBlock ?? "");
  const tools = createTools({ cwd, abortSignal });
  const model = await getModel(modelId);

  // If we extracted image paths, override the last user message with a multipart
  // content array (text + image parts) so vision models actually see the image.
  const messages = toModelMessages(session);
  if (images.length > 0) {
    const lastUserIdx = (() => {
      for (let i = messages.length - 1; i >= 0; i--) if (messages[i].role === "user") return i;
      return -1;
    })();
    if (lastUserIdx >= 0) {
      const parts: Array<{ type: "text"; text: string } | { type: "image"; image: Buffer; mediaType: string }> = [];
      if (textWithoutPaths) parts.push({ type: "text", text: textWithoutPaths });
      for (const img of images) parts.push({ type: "image", image: img.data, mediaType: img.mediaType });
      messages[lastUserIdx] = { role: "user", content: parts as never };
    }
  }

  const thinkingLevel: ThinkingLevel =
    opts.thinkingLevel ?? ((settingsStore.get("thinkingLevel") as ThinkingLevel | undefined) ?? "off");
  const catalogEntry = (await getCatalog())[modelId];
  const { model: modelShortId } = parseModelId(modelId);
  const providerOptions =
    catalogEntry?.reasoning === false
      ? undefined
      : buildProviderOptions(provider, thinkingLevel, modelShortId);

  const result = streamText({
    model,
    system,
    messages,
    tools,
    stopWhen: stepCountIs(maxSteps),
    abortSignal,
    experimental_transform: smoothStream({ delayInMs: 20, chunking: "word" }),
    ...(providerOptions ? { providerOptions: providerOptions as never } : {}),
  });

  let assistantText = "";
  let lastUsage: UsageBlock | undefined;
  let lastStepUsage: UsageBlock | undefined;

  for await (const part of result.fullStream) {
    if (abortSignal?.aborted) break;
    switch (part.type) {
      case "text-delta":
        assistantText += part.text;
        emitter.emit("text-delta", part.text);
        break;
      case "reasoning-delta":
        emitter.emit("reasoning-delta", (part as { text: string }).text);
        break;
      case "reasoning-start":
        emitter.emit("reasoning-start");
        break;
      case "reasoning-end":
        emitter.emit("reasoning-end");
        break;
      case "tool-call":
        emitter.emit("tool-call", part);
        break;
      case "tool-result":
        emitter.emit("tool-result", part);
        break;
      case "finish-step": {
        const u = (part as { usage?: UsageBlock }).usage;
        if (u) lastStepUsage = u;
        break;
      }
      case "finish": {
        const u = (part as { totalUsage?: UsageBlock }).totalUsage;
        lastUsage = u;
        if (u) {
          const breakdown = tracker.add(modelId, u);
          emitter.emit("finish", { usage: u, lastStepUsage, breakdown });
        } else {
          emitter.emit("finish", { usage: undefined, lastStepUsage });
        }
        break;
      }
      case "error": {
        const msg = String((part as { error?: unknown }).error ?? "");
        if (/^(reasoning|text) part .* not found$/.test(msg)) break;
        emitter.emit("error", part.error);
        break;
      }
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
