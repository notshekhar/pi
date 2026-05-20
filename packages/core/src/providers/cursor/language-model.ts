import type {
  LanguageModelV2,
  LanguageModelV2CallOptions,
  LanguageModelV2Content,
  LanguageModelV2FinishReason,
  LanguageModelV2StreamPart,
  LanguageModelV2Usage,
} from "@ai-sdk/provider";
import type { InteractionUpdate } from "@cursor/sdk";
import { extractLastUserMessage } from "./prompt-flatten";
import { mapCursorTool } from "./tool-mapping";

// Adapter wrapping @cursor/sdk's Agent.send into AI SDK v6 LanguageModelV2.
//
// Per-call model: each doStream() opens a fresh cursor agent (cursor SDK
// requires it — Agent.create does not carry conversation state across our
// agent loop turns). We send only the latest user turn to keep payloads
// small; multi-turn memory needs Agent.resume(agentId) wired into sessions
// (v2 work).
//
// Tool calls from cursor are emitted with providerExecuted: true so our
// agent loop does NOT try to re-execute them — cursor's runtime ran them
// already and we relay the result for display only.
//
// Known limitation under Bun: bun's HTTP/2 client implementation drops
// connectRPC frames mid-stream (NGHTTP2_FRAME_SIZE_ERROR), so cursor's
// `tool-call-completed` events for slower tools (read/edit/write/grep/glob)
// never arrive. We mitigate by emitting synthetic tool-result on
// turn-ended / stream close so the renderer doesn't hang grey, but real
// tool output for those tools is lost until bun's http2 is fixed upstream.

export interface CursorLanguageModelOptions {
  apiKey: string;
  modelId: string;
}

// Narrow shape we actually consume from the SDK's ToolCall union. We only
// pull a handful of fields; the underlying ToolCall types vary per tool
// (ReadToolCall, EditToolCall, ShellToolCall, etc.). Defining a structural
// view here avoids importing the whole tool-call-types tree.
interface CursorToolCallView {
  name?: string;
  type?: string;
  args?: unknown;
  result?: unknown;
  isError?: boolean;
}

function readToolCall(value: unknown): CursorToolCallView {
  if (!value || typeof value !== "object") return {};
  const r = value as Record<string, unknown>;
  const rawResult = r.result;
  // Cursor wraps tool results as { status: "success", value } | { status: "error", error }.
  // Unwrap so pi's renderer sees the actual payload, not the discriminator.
  let result: unknown = rawResult;
  let isError = r.isError === true;
  if (rawResult && typeof rawResult === "object") {
    const wrapped = rawResult as Record<string, unknown>;
    if (wrapped.status === "success" && "value" in wrapped) {
      result = wrapped.value;
    } else if (wrapped.status === "error") {
      result = wrapped.error ?? wrapped;
      isError = true;
    }
  }
  return {
    name: typeof r.name === "string" ? r.name : undefined,
    type: typeof r.type === "string" ? r.type : undefined,
    args: r.args,
    result,
    isError,
  };
}

export function createCursorLanguageModel(opts: CursorLanguageModelOptions): LanguageModelV2 {
  return {
    specificationVersion: "v2",
    provider: "cursor",
    modelId: opts.modelId,
    supportedUrls: {},

    async doGenerate(callOptions: LanguageModelV2CallOptions) {
      const { stream } = await this.doStream(callOptions);
      const content: LanguageModelV2Content[] = [];
      let finishReason: LanguageModelV2FinishReason = "stop";
      let usage: LanguageModelV2Usage = { inputTokens: 0, outputTokens: 0, totalTokens: 0 };
      const reader = stream.getReader();
      let currentText = "";
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        if (value.type === "text-delta") currentText += value.delta;
        else if (value.type === "text-end") {
          if (currentText) {
            content.push({ type: "text", text: currentText });
            currentText = "";
          }
        } else if (value.type === "tool-call") {
          content.push(value);
        } else if (value.type === "finish") {
          finishReason = value.finishReason;
          usage = value.usage;
        }
      }
      if (currentText) content.push({ type: "text", text: currentText });
      return { content, finishReason, usage, warnings: [] };
    },

    async doStream(callOptions: LanguageModelV2CallOptions) {
      // Dynamic import — @cursor/sdk pulls sqlite3 native binding and
      // connectRPC; keep it lazy so non-cursor users don't pay the cost.
      const { Agent } = await import("@cursor/sdk");

      // Cursor's agent ships its own system prompt + toolset; sending pi's
      // full history + tools schema as one blob blew HTTP/2 frame limits.
      // Send only the latest user turn — multi-turn memory needs
      // Agent.resume(agentId) wired into sessions (v2).
      const userText = extractLastUserMessage(callOptions.prompt);
      const apiKey = opts.apiKey;
      const modelId = opts.modelId;

      let cancelled = false;
      callOptions.abortSignal?.addEventListener("abort", () => {
        cancelled = true;
      });

      const stream = new ReadableStream<LanguageModelV2StreamPart>({
        async start(controller) {
          const textBlockId = "text-1";
          let textStarted = false;
          // Track tool calls that started but haven't completed. Cursor's
          // SDK only emits `tool-call-completed` for tools that succeed;
          // failed/timed-out ones (notably read/edit/write/grep/glob on
          // bun due to its broken HTTP/2 client) emit `tool-call-started`
          // and then disappear. Without a synthetic result the renderer
          // leaves the tool card grey/loading forever.
          const openToolCalls = new Map<string, string>(); // id -> mapped name

          const enqueueTextStart = (): void => {
            if (!textStarted) {
              controller.enqueue({ type: "text-start", id: textBlockId });
              textStarted = true;
            }
          };

          const closeOpenTools = (): void => {
            for (const [id, name] of openToolCalls) {
              controller.enqueue({
                type: "tool-result",
                toolCallId: id,
                toolName: name,
                result: { note: "Cursor SDK did not return a result for this tool call." },
                isError: false,
                providerExecuted: true,
              });
            }
            openToolCalls.clear();
          };

          try {
            controller.enqueue({ type: "stream-start", warnings: [] });

            // LOCAL agent (cloud agents are rate-limited heavily on
            // non-Ultra plans). settingSources: [] skips project/user
            // settings to keep request small.
            const agent = await Agent.create({
              apiKey,
              model: { id: modelId },
              local: { settingSources: [] },
            });

            const run = await agent.send(
              { text: userText },
              {
                onDelta: ({ update }: { update: InteractionUpdate }) => {
                  if (cancelled) return;
                  if (process.env.PI_CURSOR_DEBUG) {
                    console.error("[cursor-debug] onDelta:", JSON.stringify(update));
                  }
                  switch (update.type) {
                    case "text-delta": {
                      enqueueTextStart();
                      controller.enqueue({
                        type: "text-delta",
                        id: textBlockId,
                        delta: update.text,
                      });
                      break;
                    }
                    case "thinking-delta": {
                      const rid = "reason-1";
                      controller.enqueue({ type: "reasoning-start", id: rid });
                      controller.enqueue({
                        type: "reasoning-delta",
                        id: rid,
                        delta: update.text,
                      });
                      controller.enqueue({ type: "reasoning-end", id: rid });
                      break;
                    }
                    case "tool-call-started": {
                      const id = String(update.callId);
                      const tc = readToolCall(update.toolCall);
                      const name = mapCursorTool(tc.name ?? tc.type ?? "unknown");
                      openToolCalls.set(id, name);
                      controller.enqueue({
                        type: "tool-input-start",
                        id,
                        toolName: name,
                        providerExecuted: true,
                      });
                      controller.enqueue({
                        type: "tool-input-delta",
                        id,
                        delta: JSON.stringify(tc.args ?? {}),
                      });
                      controller.enqueue({ type: "tool-input-end", id });
                      controller.enqueue({
                        type: "tool-call",
                        toolCallId: id,
                        toolName: name,
                        input: JSON.stringify(tc.args ?? {}),
                        providerExecuted: true,
                      });
                      break;
                    }
                    case "tool-call-completed": {
                      const id = String(update.callId);
                      const tc = readToolCall(update.toolCall);
                      const name = mapCursorTool(tc.name ?? tc.type ?? "unknown");
                      openToolCalls.delete(id);
                      controller.enqueue({
                        type: "tool-result",
                        toolCallId: id,
                        toolName: name,
                        result: tc.result ?? null,
                        isError: tc.isError === true,
                        providerExecuted: true,
                      });
                      break;
                    }
                    case "turn-ended": {
                      closeOpenTools();
                      break;
                    }
                    default:
                      // partial-tool-call, summary, step-started/completed,
                      // user-message-appended, token-delta, shell-output-delta, etc.
                      break;
                  }
                },
              },
            );

            // send() resolves once the run is queued. Deltas fire as the
            // run progresses; await run.wait() so all onDelta callbacks
            // have fired before closing the stream. Swallow trailing
            // transport errors (NGHTTP2 close race) — they fire after the
            // real data has already arrived via onDelta.
            let runStatus: string | undefined;
            try {
              const result = await run.wait();
              runStatus = result.status;
            } catch (waitErr) {
              const msg = waitErr instanceof Error ? waitErr.message : String(waitErr);
              if (msg.includes("NGHTTP2_FRAME_SIZE_ERROR") || msg.includes("Stream closed")) {
                if (process.env.PI_CURSOR_DEBUG) {
                  console.error("[cursor-debug] swallowed late transport error:", msg);
                }
              } else {
                throw waitErr;
              }
            }

            // Belt-and-braces: any tool started but never completed gets
            // a synthetic result so the renderer doesn't hang grey.
            closeOpenTools();

            if (textStarted) {
              controller.enqueue({ type: "text-end", id: textBlockId });
            }

            controller.enqueue({
              type: "finish",
              finishReason: runStatus === "cancelled" ? "other" : "stop",
              usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
            });
            controller.close();

            try { agent.close(); } catch {}
          } catch (err) {
            controller.enqueue({
              type: "error",
              error: err instanceof Error ? err.message : String(err),
            });
            controller.close();
          }
        },
      });

      return { stream };
    },
  };
}
