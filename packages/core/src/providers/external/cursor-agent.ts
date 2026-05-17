import { resolveAuthToken } from "../../auth";
import { parseModelId } from "../index";
import type { UsageBlock } from "../../types";
import { readSdkSessionRef, writeSdkSessionRef, type ExternalAgentRunner } from "./types";

type CursorSdk = { Agent: { create: (cfg: unknown) => Promise<unknown> } };

async function loadSdk(): Promise<CursorSdk | null> {
  try {
    return (await import("@cursor/sdk" as string)) as CursorSdk;
  } catch {
    return null;
  }
}

export const runCursorAgentTurn: ExternalAgentRunner = async (opts) => {
  const sdk = await loadSdk();
  if (!sdk) {
    opts.emitter.emit("error", new Error("@cursor/sdk not installed. Run: npm i @cursor/sdk"));
    return;
  }

  const token =
    (await resolveAuthToken("cursor-agent")) ??
    process.env.CURSOR_API_KEY ??
    null;

  if (!token) {
    opts.emitter.emit("error", new Error("No Cursor credentials. Try: /login cursor-agent or export CURSOR_API_KEY"));
    return;
  }

  const { model } = parseModelId(opts.modelId);
  const existingAgentId = readSdkSessionRef(opts.session, "cursor-agent");

  // sdk type loose — beta API. Use any to avoid typecheck churn.
  const Agent = sdk.Agent as {
    create: (cfg: unknown) => Promise<unknown>;
  };

  let agent: { send: (text: string) => Promise<{ id?: string; agentId?: string; stream: () => AsyncIterable<unknown>; cancel?: () => Promise<void> }> };
  try {
    agent = (await Agent.create({
      apiKey: token,
      model: { id: model },
      local: { cwd: opts.cwd },
      ...(existingAgentId ? { agentId: existingAgentId } : {}),
    })) as never;
  } catch (err) {
    opts.emitter.emit("error", err);
    return;
  }

  let run: Awaited<ReturnType<typeof agent.send>>;
  try {
    run = await agent.send(opts.userInput);
  } catch (err) {
    opts.emitter.emit("error", err);
    return;
  }

  const onAbort = () => { run.cancel?.().catch(() => {}); };
  opts.abortSignal?.addEventListener("abort", onAbort, { once: true });

  let assistantText = "";
  let lastUsage: UsageBlock | undefined;
  const agentRunId = run.id ?? run.agentId;
  if (agentRunId) await writeSdkSessionRef(opts.session, "cursor-agent", agentRunId);

  try {
    for await (const event of run.stream()) {
      if (opts.abortSignal?.aborted) break;
      const e = event as { type?: string; message?: { content?: Array<{ type: string; text?: string; name?: string; input?: unknown; id?: string; tool_call_id?: string; output?: unknown }> }; usage?: { input_tokens?: number; output_tokens?: number }; status?: string };

      if (e.type === "assistant" && Array.isArray(e.message?.content)) {
        for (const block of e.message.content) {
          if (block.type === "text" && typeof block.text === "string") {
            assistantText += block.text;
            opts.emitter.emit("text-delta", block.text);
          } else if (block.type === "tool_call") {
            opts.emitter.emit("tool-call", {
              toolName: block.name,
              args: block.input,
              toolCallId: block.id,
            });
          }
        }
        continue;
      }

      if (e.type === "tool_call") {
        const c = (e as unknown) as { name?: string; input?: unknown; id?: string };
        opts.emitter.emit("tool-call", { toolName: c.name, args: c.input, toolCallId: c.id });
        continue;
      }

      if (e.type === "tool_result" || e.type === "task") {
        const c = (e as unknown) as { tool_call_id?: string; output?: unknown };
        if (c.tool_call_id) {
          opts.emitter.emit("tool-result", { toolCallId: c.tool_call_id, output: c.output });
        }
        continue;
      }

      if (e.usage) {
        lastUsage = {
          inputTokens: e.usage.input_tokens,
          outputTokens: e.usage.output_tokens,
          totalTokens: (e.usage.input_tokens ?? 0) + (e.usage.output_tokens ?? 0),
        };
      }
    }
  } catch (err) {
    opts.emitter.emit("error", err);
  } finally {
    opts.abortSignal?.removeEventListener("abort", onAbort);
  }

  const breakdown = lastUsage ? opts.tracker.add(opts.modelId, lastUsage) : undefined;
  opts.emitter.emit("finish", { usage: lastUsage, breakdown });

  await opts.session.append({
    type: "message",
    ts: Date.now(),
    role: "assistant",
    content: assistantText,
    usage: lastUsage,
  });
};
