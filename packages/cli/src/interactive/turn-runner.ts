import { EventEmitter } from "node:events";
import {
    asTurnEmitter,
    type CommandContext,
    parseModelId,
    runTurn,
    subagentArgSummary,
    type UsageBlock,
} from "@notshekhar/pi-core";
import { wrapSessionHookContext } from "@notshekhar/pi-core";
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

    // Pull the next queued input and resubmit it, whatever its type (chat or
    // command). Called after every item finishes so the FIFO queue keeps
    // draining — chat turns drain from their finally, commands/guards from
    // their return paths.
    const drainNext = (): void => {
        const next = queuedMessages.shift();
        if (next === undefined) return;
        renderPending();
        if (editor.onSubmit) void editor.onSubmit(next);
    };

    const onSubmit = async (raw: string) => {
        const text = raw.trim();
        if (!text) {
            drainNext();
            return;
        }

        // Agent busy → queue every input for after the current turn, FIFO.
        // Everything queues uniformly: chat messages AND slash commands
        // (including /new and /clear). They mutate session/model state and would
        // race the running turn, so they run in order when their turn comes up
        // rather than preempting. The queue drains after each item via
        // drainNext(), whatever its type.
        if (state.busy) {
            queuedMessages.push(text);
            renderPending();
            tui.requestRender();
            return;
        }

        // Slash commands run inline. Handler errors land in chat — otherwise
        // they die as unhandled rejections nobody sees.
        if (text.startsWith("/")) {
            history.addCommand(text);
            try {
                const handled = await commands.run(text, ctx);
                if (!handled) {
                    history.addSystem(`unknown command: ${text}`);
                }
            } catch (err) {
                history.addError(formatError(err));
            }
            tui.requestRender();
            drainNext();
            return;
        }

        // No model picked yet — a chat turn can't run. Guide instead of crashing
        // on parseModelId(""); /provider and /login stay reachable above.
        if (!state.modelId) {
            history.addSystem("No model selected. Run /provider to pick one, or /login to add a provider.");
            tui.requestRender();
            drainNext();
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
        const finalInput = state.pendingInjection ? wrapSessionHookContext(state.pendingInjection, text) : text;
        state.pendingInjection = null;

        // One-shot agent (/<agent> <message>) applies to exactly this turn.
        const turnAgent = state.oneShotAgent ?? state.agent;
        state.oneShotAgent = null;

        const activeSession = await ensureSession();
        state.busy = true;
        history.addUser(finalInput);
        showWorking("Generating");
        tui.requestRender();

        const { provider: turnProvider } = parseModelId(state.modelId);
        history.ensureAssistant(turnProvider, state.modelId);
        const emitter = asTurnEmitter(new EventEmitter());
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
        emitter.on("tool-input-updated", (e: { toolCallId?: string; input?: unknown }) => {
            if (e.toolCallId) history.updateToolCallInput(e.toolCallId, (e.input ?? {}) as Record<string, unknown>);
            tui.requestRender();
        });
        // Subagent streaming: live activity renders inside the task tool's box
        // (keyed by the task toolCallId). On finish, the activity log stays on
        // top of the final report so expanding shows the whole run.
        const subagentBuf = new Map<string, string>();
        // Streaming repaint coalescing: subagent deltas arrive per token —
        // rebuilding the tool box for each one burns CPU for invisible
        // frames. Dirty ids flush on a ~50ms timer instead.
        const subagentStatus = new Map<string, string>();
        const dirtySubagents = new Set<string>();
        let subagentFlushTimer: ReturnType<typeof setTimeout> | null = null;
        const flushSubagentProgress = () => {
            subagentFlushTimer = null;
            for (const id of dirtySubagents) {
                const buf = subagentBuf.get(id);
                if (buf === undefined) continue; // already finished
                history.updateToolProgress(id, buf);
                const status = subagentStatus.get(id);
                if (status) history.setToolStatus(id, status);
            }
            dirtySubagents.clear();
            tui.requestRender();
        };
        const queueSubagentRepaint = (id: string) => {
            dirtySubagents.add(id);
            if (!subagentFlushTimer) subagentFlushTimer = setTimeout(flushSubagentProgress, 50);
        };
        emitter.on("tool-result", (part: { output?: unknown; toolCallId?: string }) => {
            const id = part.toolCallId ?? "";
            subagentBuf.delete(id);
            subagentStatus.delete(id);
            dirtySubagents.delete(id);
            // Task tools output { history, report } — stringifyResult shows
            // the full uncapped history (the live buffer is tail-capped).
            // Normal tools show their output as-is.
            history.addToolResult(id, part.output);
            showWorking("Generating");
            tui.requestRender();
        });
        // Tool failed: resolve its box in red with the error text (instead of
        // leaving it spinning), then keep working — the model already received
        // the error and may try something else.
        emitter.on("tool-error", (e: { toolCallId?: string; error: unknown }) => {
            const id = e.toolCallId ?? "";
            subagentBuf.delete(id);
            subagentStatus.delete(id);
            dirtySubagents.delete(id);
            history.addToolResult(id, formatError(e.error), true);
            showWorking("Generating");
            tui.requestRender();
        });
        emitter.on("subagent-tool", (e: { toolCallId: string; agent: string; toolName?: string; input?: unknown }) => {
            const prev = subagentBuf.get(e.toolCallId) ?? "";
            const line = `> ${e.toolName ?? "tool"}${subagentArgSummary(e.input)}\n`;
            const next = `${prev}${prev && !prev.endsWith("\n") ? "\n" : ""}${line}`.slice(-6000);
            subagentBuf.set(e.toolCallId, next);
            subagentStatus.set(e.toolCallId, e.toolName ?? "running");
            queueSubagentRepaint(e.toolCallId);
            showWorking(`Subagent ${e.agent} · ${e.toolName}…`);
        });
        emitter.on("subagent-delta", (e: { toolCallId: string; agent: string; text: string }) => {
            const next = ((subagentBuf.get(e.toolCallId) ?? "") + e.text).slice(-6000);
            subagentBuf.set(e.toolCallId, next);
            subagentStatus.set(e.toolCallId, "writing");
            queueSubagentRepaint(e.toolCallId);
        });
        emitter.on("subagent-finish", (e: { toolCallId: string }) => {
            // Buffer intentionally kept — tool-result composes it into the
            // final display, then clears it.
            refreshFooter();
            showWorking("Generating");
            tui.requestRender();
        });
        // Live cost: footer updates after every step (main and subagent), not
        // just at turn end. Step usage also carries the current context size.
        emitter.on("step-usage", (e: { usage?: UsageBlock }) => {
            refreshFooter(e.usage);
        });
        emitter.on("subagent-step-usage", () => {
            // Cost only — a subagent's context is not the main context.
            refreshFooter();
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
        // Recap generation is detached from the turn — this may fire after
        // busy is already false, and renders wherever the chat currently ends.
        emitter.on("data-recap", (e: { text: string }) => {
            history.addRecap(e.text);
            refreshFooter();
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
                agent: turnAgent,
            });
        } catch (err) {
            history.addError(formatError(err));
        } finally {
            state.busy = false;
            history.finishAssistant();
            hideWorking();
            tui.requestRender();
            // Drain the next queued input (FIFO), whatever its type. Each fresh
            // turn/command re-reads state.
            drainNext();
        }
    };

    return onSubmit;
}
