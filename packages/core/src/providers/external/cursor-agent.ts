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
  try {
    run = await agent.send(opts.userInput);
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

  let assistantText = "";
  let lastUsage: UsageBlock | undefined;

  try {
    for await (const msg of run.stream()) {
      if (opts.abortSignal?.aborted) break;

      if (msg.type === "thinking") {
        if (msg.text) opts.emitter.emit("reasoning-delta", msg.text);
        continue;
      }

      if (msg.type === "assistant") {
        for (const block of msg.message.content) {
          if (block.type === "text") {
            assistantText += block.text;
            opts.emitter.emit("text-delta", block.text);
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

      if (msg.type === "tool_call") {
        if (msg.status === "running") {
          opts.emitter.emit("tool-call", {
            toolName: msg.name,
            args: msg.args,
            toolCallId: msg.call_id,
          });
        } else if (msg.status === "completed" || msg.status === "error") {
          opts.emitter.emit("tool-result", {
            toolCallId: msg.call_id,
            output: msg.result,
          });
        }
        continue;
      }
    }

    const result = await run.wait();
    if (result.durationMs != null) {
      // Cursor SDK does not currently expose per-run token usage.
      lastUsage = undefined;
    }
    void result;
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
