import { EventEmitter } from "node:events";
import { type CommandContext, parseModelId, runTurn, type UsageBlock } from "@notshekhar/pi-core";
import type { AppDeps } from "./deps";
import type { AppState } from "./state";

function pickContextUsage(event: { usage?: UsageBlock; lastStepUsage?: UsageBlock }): UsageBlock | undefined {
  return event.lastStepUsage ?? event.usage;
}

/** Errors render in chat, never persist to the session — make them readable. */
export function formatError(err: unknown): string {
  if (err instanceof Error) return err.message;
  if (typeof err === "string") return err;
  try {
    return JSON.stringify(err);
  } catch {
    return String(err);
  }
}

export function createTurnRunner(state: AppState, deps: AppDeps, ctx: CommandContext) {
  const {
    tui,
    history,
    editor,
    commands,
    queuedMessages,
    refreshFooter,
    renderPending,
    showWorking,
    hideWorking,
    ensureSession,
    tracker,
  } = deps;

  const onSubmit = async (raw: string) => {
    const text = raw.trim();
    if (!text) return;

    // Slash commands always run inline (no queueing). Handler errors land in
    // chat — otherwise they die as unhandled rejections nobody sees.
    if (text.startsWith("/")) {
      try {
        const handled = await commands.run(text, ctx);
        if (!handled) {
          history.addSystem(`unknown command: ${text}`);
        }
      } catch (err) {
        history.addError(formatError(err));
      }
      tui.requestRender();
      return;
    }

    // Agent busy → queue for after current turn
    if (state.busy) {
      queuedMessages.push(text);
      renderPending();
      tui.requestRender();
      return;
    }

    // First turn may race SessionStart hooks — wait so their injected
    // context (pendingInjection) isn't silently dropped.
    if (state.startupHooksDone) {
      try {
        await state.startupHooksDone;
      } catch (err) {
        history.addError(`startup hooks: ${formatError(err)}`);
      }
      state.startupHooksDone = null;
    }

    // SessionStart hook context must persist in the transcript (the model
    // needs it in history on every later turn), but the tag lets the TUI
    // collapse it instead of rendering it as if the user typed it.
    const finalInput = state.pendingInjection
      ? `<session-start-hook-context>\n${state.pendingInjection}\n</session-start-hook-context>\n\n${text}`
      : text;
    state.pendingInjection = null;

    const activeSession = await ensureSession();
    state.busy = true;
    history.addUser(finalInput);
    showWorking("Generating");
    tui.requestRender();

    const { provider: turnProvider } = parseModelId(state.modelId);
    history.ensureAssistant(turnProvider, state.modelId);
    const emitter = new EventEmitter();
    emitter.on("text-delta", (t: string) => {
      history.appendAssistantDelta(t, turnProvider, state.modelId);
      tui.requestRender();
    });
    emitter.on("reasoning-delta", (t: string) => {
      history.appendAssistantThinking(t, turnProvider, state.modelId);
      tui.requestRender();
    });
    emitter.on("tool-call", (part: { toolName?: string; input?: unknown; toolCallId?: string }) => {
      const id = part.toolCallId ?? `${part.toolName}-${Date.now()}`;
      history.addToolCall(part.toolName ?? "tool", id, (part.input ?? {}) as Record<string, unknown>);
      showWorking(`Running ${part.toolName}…`);
      tui.requestRender();
    });
    emitter.on("tool-result", (part: { output?: unknown; toolCallId?: string }) => {
      history.addToolResult(part.toolCallId ?? "", part.output);
      showWorking("Generating");
      tui.requestRender();
    });
    emitter.on("hook-message", (m: string) => {
      history.addHook(m);
      tui.requestRender();
    });
    // OSC sequences from hooks (Warp-style notifications): invisible control
    // sequences, safe to write directly without tearing the renderer.
    emitter.on("hook-terminal-sequence", (s: string) => {
      process.stdout.write(s);
    });
    emitter.on("compact-start", () => {
      showWorking("Compacting");
      tui.requestRender();
    });
    emitter.on(
      "compact-end",
      (r: { summary: string; tokensBefore: number; tokensAfter?: number; aborted?: boolean }) => {
        if (r.aborted) history.addSystem("compact aborted");
        else if (r.summary) history.addCompactionSummary(r.summary, r.tokensBefore);
        if (typeof r.tokensAfter === "number") state.latestContextTokens = r.tokensAfter;
        refreshFooter();
        tui.requestRender();
      },
    );
    emitter.on("finish", (event: { usage?: UsageBlock; lastStepUsage?: UsageBlock }) => {
      history.finishAssistant();
      refreshFooter(pickContextUsage(event));
      tui.requestRender();
    });
    emitter.on("error", (err: unknown) => {
      history.addError(formatError(err));
      tui.requestRender();
    });

    try {
      await runTurn({
        session: activeSession,
        modelId: state.modelId,
        userInput: finalInput,
        cwd: state.cwd,
        abortSignal: state.abort.signal,
        tracker,
        emitter,
        thinkingLevel: state.thinkingLevel,
      });
    } catch (err) {
      history.addError(formatError(err));
    } finally {
      state.busy = false;
      history.finishAssistant();
      hideWorking();
      tui.requestRender();
      // Drain queued follow-up messages (FIFO). Each fresh turn re-reads state.
      const next = queuedMessages.shift();
      if (next !== undefined) {
        renderPending();
        if (editor.onSubmit) void editor.onSubmit(next);
      }
    }
  };

  return onSubmit;
}
