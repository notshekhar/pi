import { resolveAuthToken } from "../../auth";
import { parseModelId } from "../index";
import type { UsageBlock } from "../../types";
import { readSdkSessionRef, writeSdkSessionRef, type ExternalAgentRunner } from "./types";

type CursorSdk = typeof import("@cursor/sdk");

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

  const token = (await resolveAuthToken("cursor-agent")) ?? process.env.CURSOR_API_KEY ?? null;
  if (!token) {
    opts.emitter.emit("error", new Error("No Cursor credentials. Try: /login cursor-agent or export CURSOR_API_KEY"));
    return;
  }

  const { model } = parseModelId(opts.modelId);
  const existingAgentId = readSdkSessionRef(opts.session, "cursor-agent");

  let agent: Awaited<ReturnType<typeof sdk.Agent.create>>;
  try {
    if (existingAgentId) {
      agent = await sdk.Agent.resume(existingAgentId, { apiKey: token, model: { id: model } });
    } else {
      agent = await sdk.Agent.create({
        apiKey: token,
        model: { id: model },
        local: { cwd: opts.cwd },
      });
    }
  } catch (err) {
    opts.emitter.emit("error", err);
    return;
  }

  await writeSdkSessionRef(opts.session, "cursor-agent", agent.agentId);

  let run: Awaited<ReturnType<typeof agent.send>>;
  let assistantText = "";
  const seenToolCalls = new Set<string>();
  try {
    run = await agent.send(opts.userInput, {
      onDelta: ({ update }) => {
        if (opts.abortSignal?.aborted) return;
        const u = update as { type: string; text?: string; toolCall?: { callId?: string; name?: string; args?: unknown }; id?: string };
        if (u.type === "thinking-delta" && typeof u.text === "string") {
          opts.emitter.emit("reasoning-delta", u.text);
        } else if (u.type === "text-delta" && typeof u.text === "string") {
          assistantText += u.text;
          opts.emitter.emit("text-delta", u.text);
        } else if (u.type === "tool-call-started" && u.toolCall) {
          const id = u.toolCall.callId ?? `${u.toolCall.name}-${Date.now()}`;
          if (!seenToolCalls.has(id)) {
            seenToolCalls.add(id);
            opts.emitter.emit("tool-call", {
              toolName: u.toolCall.name,
              input: u.toolCall.args,
              toolCallId: id,
            });
          }
        } else if (u.type === "tool-call-completed") {
          const tc = u as { toolCall?: { callId?: string; result?: unknown } };
          if (tc.toolCall?.callId) {
            opts.emitter.emit("tool-result", {
              toolCallId: tc.toolCall.callId,
              output: tc.toolCall.result,
            });
          }
        }
      },
    });
  } catch (err) {
    opts.emitter.emit("error", err);
    try {
      agent.close();
    } catch {}
    return;
  }

  const onAbort = () => {
    run.cancel().catch(() => {});
  };
  opts.abortSignal?.addEventListener("abort", onAbort, { once: true });

  let lastUsage: UsageBlock | undefined;

  try {
    // Drain stream so the SDK reaches the run-end. Deltas already emitted via onDelta;
    // here we only pick up tool calls that bypassed onDelta and any final assistant blocks.
    for await (const msg of run.stream()) {
      if (opts.abortSignal?.aborted) break;

      if (msg.type === "assistant") {
        for (const block of msg.message.content) {
          if (block.type === "tool_use") {
            if (seenToolCalls.has(block.id)) continue;
            seenToolCalls.add(block.id);
            opts.emitter.emit("tool-call", {
              toolName: block.name,
              input: block.input,
              toolCallId: block.id,
            });
          }
          // text already streamed via onDelta
        }
        continue;
      }

      if (msg.type === "tool_call") {
        if (msg.status === "completed" || msg.status === "error") {
          opts.emitter.emit("tool-result", {
            toolCallId: msg.call_id,
            output: msg.result,
          });
        }
        continue;
      }
    }

    await run.wait();
  } catch (err) {
    opts.emitter.emit("error", err);
  } finally {
    opts.abortSignal?.removeEventListener("abort", onAbort);
    try {
      agent.close();
    } catch {}
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
