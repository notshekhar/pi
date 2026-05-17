import { resolveAuthToken } from "../../auth";
import { parseModelId } from "../index";
import type { UsageBlock } from "../../types";
import { readSdkSessionRef, writeSdkSessionRef, type ExternalAgentRunner } from "./types";

const ANTHROPIC_OAUTH_PREFIX = "sk-ant-oat";

type ClaudeSdk = { query: (args: { prompt: string; options: Record<string, unknown> }) => AsyncIterable<unknown> };

async function loadSdk(): Promise<ClaudeSdk | null> {
  try {
    const mod = (await import("@anthropic-ai/claude-agent-sdk" as string)) as ClaudeSdk;
    return mod;
  } catch {
    return null;
  }
}

function toAbortController(signal?: AbortSignal): AbortController {
  const ac = new AbortController();
  if (signal) {
    if (signal.aborted) ac.abort();
    else signal.addEventListener("abort", () => ac.abort(), { once: true });
  }
  return ac;
}

export const runClaudeAgentTurn: ExternalAgentRunner = async (opts) => {
  const sdk = await loadSdk();
  if (!sdk) {
    opts.emitter.emit("error", new Error("@anthropic-ai/claude-agent-sdk not installed. Run: npm i @anthropic-ai/claude-agent-sdk"));
    return;
  }

  const token =
    (await resolveAuthToken("claude-agent")) ??
    (await resolveAuthToken("anthropic")) ??
    process.env.ANTHROPIC_API_KEY ??
    process.env.CLAUDE_CODE_OAUTH_TOKEN ??
    null;

  if (!token) {
    opts.emitter.emit("error", new Error("No Claude credentials. Try: /login claude-agent or export ANTHROPIC_API_KEY"));
    return;
  }

  const isOAuth = token.includes(ANTHROPIC_OAUTH_PREFIX);
  const env: Record<string, string | undefined> = { ...process.env };
  if (isOAuth) {
    env.CLAUDE_CODE_OAUTH_TOKEN = token;
    delete env.ANTHROPIC_API_KEY;
  } else {
    env.ANTHROPIC_API_KEY = token;
    delete env.CLAUDE_CODE_OAUTH_TOKEN;
  }

  const { model } = parseModelId(opts.modelId);
  const resume = readSdkSessionRef(opts.session, "claude-agent");
  const ac = toAbortController(opts.abortSignal);

  const appendBlocks: string[] = [];
  if (opts.workspaceContext) appendBlocks.push(opts.workspaceContext);
  if (opts.skillsPrompt) appendBlocks.push(opts.skillsPrompt);
  const append = appendBlocks.join("\n\n") || undefined;

  const q = sdk.query({
    prompt: opts.userInput,
    options: {
      env,
      cwd: opts.cwd,
      model,
      resume,
      includePartialMessages: true,
      systemPrompt: append ? { type: "preset", preset: "claude_code", append } : { type: "preset", preset: "claude_code" },
      abortController: ac,
    },
  });

  let assistantText = "";
  let lastUsage: UsageBlock | undefined;
  let totalCostUsd: number | undefined;

  try {
    for await (const msg of q) {
      if (opts.abortSignal?.aborted) break;
      const m = msg as { type: string; subtype?: string; message?: unknown; event?: unknown; session_id?: string } & Record<string, unknown>;

      if (m.type === "system" && m.subtype === "init" && typeof m.session_id === "string") {
        await writeSdkSessionRef(opts.session, "claude-agent", m.session_id);
        continue;
      }

      if (m.type === "stream_event" || m.type === "partial_assistant") {
        const ev = (m.event ?? m) as {
          type?: string;
          delta?: { type?: string; text?: string; thinking?: string };
          content_block?: { type?: string; text?: string; thinking?: string };
        };
        if (ev?.delta?.type === "text_delta" && typeof ev.delta.text === "string") {
          assistantText += ev.delta.text;
          opts.emitter.emit("text-delta", ev.delta.text);
        } else if (ev?.delta?.type === "thinking_delta" && typeof ev.delta.thinking === "string") {
          opts.emitter.emit("reasoning-delta", ev.delta.thinking);
        } else if (ev?.type === "content_block_start" && ev.content_block?.type === "thinking") {
          opts.emitter.emit("reasoning-start");
        } else if (ev?.type === "content_block_stop") {
          // thinking blocks end via stop; we don't know block index here so emit a soft hint
          opts.emitter.emit("reasoning-end");
        }
        continue;
      }

      if (m.type === "assistant") {
        const content = (m.message as { content?: Array<{ type: string; text?: string; thinking?: string; name?: string; input?: unknown; id?: string }> })?.content ?? [];
        for (const block of content) {
          if (block.type === "text" && typeof block.text === "string") {
            // text streamed via partial; skip
          } else if (block.type === "thinking" && typeof block.thinking === "string") {
            // emit full thinking block if partials weren't enabled
            opts.emitter.emit("reasoning-delta", block.thinking);
          } else if (block.type === "tool_use") {
            opts.emitter.emit("tool-call", {
              toolName: block.name,
              args: block.input,
              toolCallId: block.id,
            });
          }
        }
        continue;
      }

      if (m.type === "user") {
        const tr = (m as { tool_use_result?: unknown }).tool_use_result;
        const content = (m.message as { content?: Array<{ type: string; tool_use_id?: string; content?: unknown }> })?.content ?? [];
        for (const block of content) {
          if (block.type === "tool_result") {
            opts.emitter.emit("tool-result", {
              toolCallId: block.tool_use_id,
              output: block.content ?? tr,
            });
          }
        }
        continue;
      }

      if (m.type === "result") {
        const r = m as { subtype?: string; usage?: Record<string, number>; total_cost_usd?: number };
        const u = r.usage ?? {};
        lastUsage = {
          inputTokens: u.input_tokens,
          outputTokens: u.output_tokens,
          cachedInputTokens: (u.cache_read_input_tokens ?? 0) + (u.cache_creation_input_tokens ?? 0),
          totalTokens: (u.input_tokens ?? 0) + (u.output_tokens ?? 0),
          cost: r.total_cost_usd,
        };
        totalCostUsd = r.total_cost_usd;
        if (r.subtype && r.subtype !== "success") {
          opts.emitter.emit("error", new Error(`claude-agent: ${r.subtype}`));
        }
        continue;
      }
    }
  } catch (err) {
    opts.emitter.emit("error", err);
  }

  if (lastUsage) {
    // Use SDK-provided cost directly when available (skip per-token estimate).
    const breakdown = totalCostUsd != null
      ? { inputTokens: lastUsage.inputTokens ?? 0, outputTokens: lastUsage.outputTokens ?? 0, cachedInputTokens: lastUsage.cachedInputTokens ?? 0, usd: totalCostUsd }
      : opts.tracker.add(opts.modelId, lastUsage);
    opts.emitter.emit("finish", { usage: lastUsage, breakdown });
  } else {
    opts.emitter.emit("finish", { usage: undefined });
  }

  await opts.session.append({
    type: "message",
    ts: Date.now(),
    role: "assistant",
    content: assistantText,
    usage: lastUsage,
  });
};
