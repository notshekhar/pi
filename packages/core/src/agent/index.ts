import { streamText, stepCountIs, smoothStream } from "ai";
import type { ModelMessage } from "ai";
import { EventEmitter } from "node:events";
import { getModel, parseModelId } from "../providers";
import { getCatalog } from "../catalog";
import { settingsStore } from "../auth/storage";
import { getCustomProvider, isCustomProvider, parseCustomProviderId } from "../auth";
import { createTools } from "../tools";
import { buildSystemPrompt } from "./system-prompt";
import { loadWorkspaceContext } from "./context";
import { loadProjectSkills } from "./skills";
import { extractImagesFromInput } from "./images";
import { CostTracker } from "./cost";
import { compactedContextMessages, runCompact } from "./compact";
import { runHooks, type HookOutcome } from "./hooks";
import { isTrusted } from "./trust";
import { buildProviderOptions, type ThinkingLevel } from "./thinking";
import type { Session } from "../sessions";
import type { UsageBlock } from "../types";

export { CostTracker } from "./cost";
export { runCompact, CompactAbortedError } from "./compact";
export { THINKING_LEVELS, THINKING_LEVEL_DESCRIPTIONS, buildProviderOptions, type ThinkingLevel } from "./thinking";
export { loadWorkspaceContext, watchWorkspaceContext } from "./context";
export { loadProjectSkills, type Skill } from "./skills";
export { runHooks, loadHooksConfig, hookBus, type HookEvent, type HooksConfig, type HookOutcome } from "./hooks";
export {
  hasProjectTrustInputs,
  getTrustDecision,
  isTrusted,
  setTrust,
  trustForSession,
  getTrustOptions,
  type TrustOption,
  type TrustDecision,
} from "./trust";

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
  /** internal: recursion depth for Stop-hook continuations */
  hookDepth?: number;
}

function estimateContextTokens(messages: ModelMessage[]): number {
  let total = 0;
  for (const m of messages) {
    total += Math.ceil(JSON.stringify(m).length / 4);
  }
  return total;
}

/**
 * Anthropic prompt caching: two ephemeral breakpoints (limit is 4).
 * One on the system message — the cache prefix is tools → system, so this
 * covers both. One on the last message so the whole conversation prefix is
 * a cache hit on the next turn (90% input-cost discount on reads).
 * Other providers (OpenAI, xAI, Google) cache automatically server-side.
 */
function withAnthropicCaching(system: string, messages: ModelMessage[]): ModelMessage[] {
  const cache = { anthropic: { cacheControl: { type: "ephemeral" as const } } };
  const out: ModelMessage[] = [{ role: "system", content: system, providerOptions: cache }, ...messages];
  const last = out[out.length - 1];
  out[out.length - 1] = { ...last, providerOptions: cache } as ModelMessage;
  return out;
}

function toModelMessages(session: Session): ModelMessage[] {
  const out: ModelMessage[] = [];
  for (const m of compactedContextMessages(session)) {
    if (m.role === "tool") continue;
    out.push({
      role: m.role as "user" | "assistant",
      content: typeof m.content === "string" ? m.content : JSON.stringify(m.content),
    });
  }
  return out;
}

// AI SDK prints advisory warnings (e.g. about system messages inside the
// messages array — our deliberate Anthropic prompt-caching pattern) straight
// to the console, which tears the TUI's differential rendering. Silence them.
(globalThis as Record<string, unknown>).AI_SDK_LOG_WARNINGS = false;

export async function runTurn(opts: RunTurnOptions): Promise<void> {
  const { session, modelId, userInput, cwd, abortSignal, tracker, emitter } = opts;
  // Step cap is only an upper safety bound — the loop ends naturally when the
  // model returns no tool call. 0 / unset means "run until the model decides".
  const configuredSteps = opts.maxSteps ?? (settingsStore.get("maxSteps") as number | undefined) ?? 0;
  const maxSteps = configuredSteps > 0 ? configuredSteps : Number.MAX_SAFE_INTEGER;

  // Extract any image paths from the user input → ai-sdk image parts
  const { textWithoutPaths, images } = extractImagesFromInput(userInput, cwd);
  if (images.length > 0) {
    emitter.emit(
      "attached-images",
      images.map((i) => i.path),
    );
  }
  // UserPromptSubmit hooks: may block the turn or inject context for the
  // model. Skipped for Stop-hook continuations (hookDepth > 0) — those are
  // synthetic turns, and Claude Code doesn't fire UserPromptSubmit for them.
  const hookDepth = opts.hookDepth ?? 0;
  let promptHooks: HookOutcome = { block: false, messages: [], terminalSequences: [] };
  if (hookDepth === 0) {
    promptHooks = await runHooks(
      "UserPromptSubmit",
      undefined,
      { session_id: session.id, transcript_path: session.path, prompt: userInput },
      cwd,
    );
  }
  for (const m of promptHooks.messages) emitter.emit("hook-message", m);
  for (const s of promptHooks.terminalSequences) emitter.emit("hook-terminal-sequence", s);
  if (promptHooks.block) {
    emitter.emit("error", `prompt blocked by hook: ${promptHooks.reason}`);
    emitter.emit("finish", { usage: undefined });
    return;
  }

  // Persist user message verbatim (paths intact for reference in transcripts)
  await session.append({ type: "message", ts: Date.now(), role: "user", content: userInput });

  const { provider, model: modelShortId } = parseModelId(modelId);

  // auto-compact check
  const catalog = await getCatalog();
  const modelInfo = catalog[modelId];
  const threshold = (settingsStore.get("autoCompactThreshold") as number) ?? 0.8;
  if (modelInfo) {
    const messages = toModelMessages(session);
    const tokens = estimateContextTokens(messages);
    if (tokens > modelInfo.contextWindow * threshold) {
      emitter.emit("compact-start", { reason: "auto" });
      // PreCompact is informational for watchers — block is ignored.
      await runHooks(
        "PreCompact",
        "auto",
        { session_id: session.id, transcript_path: session.path, trigger: "auto" },
        cwd,
      );
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
  // Project skills inject instructions into the prompt — gate on trust too.
  const skillsEnabled = (settingsStore.get("skills") as boolean) !== false && isTrusted(cwd);
  const skills = skillsEnabled ? await loadProjectSkills(cwd) : { skills: [], diagnostics: [], promptBlock: "" };

  const system = buildSystemPrompt({ cwd, workspaceContext: workspaceContext.text }) + (skills.promptBlock ?? "");
  const tools = withToolHooks(createTools({ cwd, abortSignal }), {
    cwd,
    sessionId: session.id,
    transcriptPath: session.path,
    emitter,
  });
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

  // Hook-injected context rides only the model-bound copy, not the transcript.
  // Must target the *last* user message even when image extraction already
  // turned it into a parts array — falling through to an earlier message would
  // rewrite history and bust the prompt-cache prefix.
  if (promptHooks.additionalContext) {
    const ctxBlock = `<hook-context>\n${promptHooks.additionalContext}\n</hook-context>`;
    for (let i = messages.length - 1; i >= 0; i--) {
      const m = messages[i];
      if (m.role !== "user") continue;
      if (typeof m.content === "string") {
        messages[i] = { role: "user", content: `${m.content}\n\n${ctxBlock}` };
      } else if (Array.isArray(m.content)) {
        messages[i] = { role: "user", content: [...m.content, { type: "text", text: ctxBlock }] as never };
      }
      break;
    }
  }

  const thinkingLevel: ThinkingLevel =
    opts.thinkingLevel ?? (settingsStore.get("thinkingLevel") as ThinkingLevel | undefined) ?? "off";
  // Custom providers (gateways like bifrost) proxy a real vendor API — map
  // thinking/caching by the configured sdk so e.g. an anthropic-compatible
  // gateway gets adaptive thinking + prompt-cache breakpoints.
  let effectiveProvider: string = provider;
  if (isCustomProvider(provider)) {
    const sdk = getCustomProvider(parseCustomProviderId(provider)!)?.sdk;
    if (sdk) effectiveProvider = sdk === "openai-compatible" ? "openai" : sdk;
  }
  let providerOptions =
    modelInfo?.reasoning === false ? undefined : buildProviderOptions(effectiveProvider, thinkingLevel, modelShortId);

  // Ollama defaults num_ctx to ~4096 regardless of the model's real context,
  // which truncates long agent loops and makes the model stop early. Pin it to
  // the model's actual context window so history isn't silently dropped.
  if (provider === "ollama") {
    const numCtx = modelInfo?.contextWindow ?? 8192;
    const existing = (providerOptions?.ollama as Record<string, unknown> | undefined) ?? {};
    const existingOpts = (existing.options as Record<string, unknown> | undefined) ?? {};
    providerOptions = {
      ...providerOptions,
      ollama: { ...existing, options: { ...existingOpts, num_ctx: numCtx } },
    };
  }

  const anthropicCaching = effectiveProvider === "anthropic";
  const result = streamText({
    model,
    ...(anthropicCaching
      ? // system inside messages is our deliberate Anthropic prompt-caching
        // pattern — allowSystemInMessages opts out of the AI SDK warning.
        { messages: withAnthropicCaching(system, messages), allowSystemInMessages: true }
      : { system, messages }),
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
          const breakdown = tracker.add(modelId, u, cwd);
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

  // Stop hooks: a block sends the reason back as a follow-up turn so the
  // agent keeps working (Claude Code parity, e.g. "tests must pass"). Depth
  // capped to avoid infinite hook loops.
  if (!abortSignal?.aborted) {
    // stop_hook_active mirrors Claude Code: true when this turn is already a
    // stop-hook continuation, so ported hooks can use it as their loop guard.
    const stopHooks = await runHooks(
      "Stop",
      undefined,
      { session_id: session.id, transcript_path: session.path, stop_hook_active: hookDepth > 0 },
      cwd,
    );
    for (const m of stopHooks.messages) emitter.emit("hook-message", m);
    for (const s of stopHooks.terminalSequences) emitter.emit("hook-terminal-sequence", s);
    if (stopHooks.block && hookDepth < 3) {
      emitter.emit("hook-message", `stop hook requested continuation: ${stopHooks.reason}`);
      await runTurn({ ...opts, userInput: `[stop hook] ${stopHooks.reason}`, hookDepth: hookDepth + 1 });
    }
  }
}

type AnyTool = { execute?: (input: unknown, options: unknown) => Promise<unknown> };

/**
 * Wraps every tool's execute with PreToolUse / PostToolUse hooks.
 * Pre: deny → the tool never runs; the reason is returned as the tool result
 * so the model can react. updatedInput replaces the arguments.
 * Post: block/additionalContext are attached to the result as hook_feedback.
 */
function withToolHooks<T extends object>(
  tools: T,
  ctx: { cwd: string; sessionId: string; transcriptPath: string; emitter: EventEmitter },
): T {
  const wrapped: Record<string, AnyTool> = {};
  for (const [name, t] of Object.entries(tools as Record<string, AnyTool>)) {
    if (!t.execute) {
      wrapped[name] = t;
      continue;
    }
    wrapped[name] = {
      ...t,
      execute: async (input: unknown, options: unknown) => {
        const pre = await runHooks(
          "PreToolUse",
          name,
          { session_id: ctx.sessionId, transcript_path: ctx.transcriptPath, tool_name: name, tool_input: input },
          ctx.cwd,
        );
        for (const m of pre.messages) ctx.emitter.emit("hook-message", m);
        for (const s of pre.terminalSequences) ctx.emitter.emit("hook-terminal-sequence", s);
        if (pre.block) {
          // Watchers treat Notification as "agent needs attention" — fire it
          // for permission-style denials; its own outcome is ignored.
          void runHooks(
            "Notification",
            undefined,
            {
              session_id: ctx.sessionId,
              transcript_path: ctx.transcriptPath,
              message: `Permission needed: ${name} — ${pre.reason}`,
              title: "pi",
            },
            ctx.cwd,
          ).then((n) => {
            for (const s of n.terminalSequences) ctx.emitter.emit("hook-terminal-sequence", s);
          });
          return { error: `blocked by PreToolUse hook: ${pre.reason}` };
        }
        const effectiveInput = pre.updatedInput !== undefined ? pre.updatedInput : input;
        // The tool-call event already showed the original args — tell the user
        // the hook rewrote them so UI and execution don't silently diverge.
        if (pre.updatedInput !== undefined) {
          ctx.emitter.emit("hook-message", `PreToolUse hook updated ${name} input: ${JSON.stringify(effectiveInput)}`);
        }
        const output = await t.execute!(effectiveInput, options);
        const post = await runHooks(
          "PostToolUse",
          name,
          {
            session_id: ctx.sessionId,
            transcript_path: ctx.transcriptPath,
            tool_name: name,
            tool_input: effectiveInput,
            // Claude Code sends `tool_response`; keep `tool_output` too for
            // any pi hooks already written against it.
            tool_response: output,
            tool_output: output,
          },
          ctx.cwd,
        );
        for (const m of post.messages) ctx.emitter.emit("hook-message", m);
        for (const s of post.terminalSequences) ctx.emitter.emit("hook-terminal-sequence", s);
        const feedback = [post.block ? `BLOCKED: ${post.reason}` : null, post.additionalContext]
          .filter(Boolean)
          .join("\n");
        if (feedback) {
          if (output && typeof output === "object") return { ...(output as object), hook_feedback: feedback };
          return { result: output, hook_feedback: feedback };
        }
        return output;
      },
    };
  }
  return wrapped as unknown as T;
}
