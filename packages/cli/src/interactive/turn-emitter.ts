import type { TUI } from "@notshekhar/loop-tui";
import { asTurnEmitter, type UsageBlock } from "@notshekhar/loop-core";
import type { ChatHistory } from "./components/chat-history";
import type { AppState } from "./state";
import type { SubagentStream } from "./subagent-stream";
import { formatError } from "./format-error";

type TurnEmitter = ReturnType<typeof asTurnEmitter>;

function pickContextUsage(event: { usage?: UsageBlock; lastStepUsage?: UsageBlock }): UsageBlock | undefined {
    return event.lastStepUsage ?? event.usage;
}

export interface TurnEmitterDeps {
    history: ChatHistory;
    tui: TUI;
    state: AppState;
    /** Provider parsed once from the turn's model id (model can't change mid-turn). */
    turnProvider: string;
    subagentStream: SubagentStream;
    showWorking: (message?: string) => void;
    refreshStatusLine: (usage?: UsageBlock) => void;
}

/**
 * Wire every turn event to the chat UI: assistant/reasoning deltas, tool
 * lifecycle, subagent streaming, live cost/context, hooks, compaction, finish.
 * Pulled out of the turn runner so its control flow (guards, queue, drain,
 * runTurn try/finally) reads on its own. Behavior is unchanged from the inline
 * wiring it replaces.
 */
export function wireTurnEmitter(emitter: TurnEmitter, deps: TurnEmitterDeps): void {
    const { history, tui, state, turnProvider, subagentStream, showWorking, refreshStatusLine } = deps;

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
        const id = part.toolCallId ?? "";
        subagentStream.clear(id);
        // Task tools output { history, report } — stringifyResult shows the full
        // uncapped history (the live buffer is tail-capped). Normal tools show
        // their output as-is.
        history.addToolResult(id, part.output);
        showWorking("Generating");
        tui.requestRender();
    });
    // Tool failed: resolve its box in red with the error text (instead of
    // leaving it spinning), then keep working — the model already received
    // the error and may try something else.
    emitter.on("tool-error", (e: { toolCallId?: string; error: unknown }) => {
        const id = e.toolCallId ?? "";
        subagentStream.clear(id);
        history.addToolResult(id, formatError(e.error), true);
        showWorking("Generating");
        tui.requestRender();
    });
    emitter.on("subagent-tool", (e: { toolCallId: string; agent: string; toolName?: string; input?: unknown }) => {
        subagentStream.onTool(e.toolCallId, e.toolName, e.input);
        showWorking(`Subagent ${e.agent} · ${e.toolName}…`);
    });
    emitter.on("subagent-delta", (e: { toolCallId: string; agent: string; text: string }) => {
        subagentStream.onDelta(e.toolCallId, e.text);
    });
    emitter.on("subagent-finish", (_e: { toolCallId: string }) => {
        // Buffer intentionally kept — tool-result composes it into the final
        // display, then clears it.
        refreshStatusLine();
        showWorking("Generating");
        tui.requestRender();
    });
    // Live cost: status line updates after every step (main and subagent), not just
    // at turn end. Step usage also carries the current context size.
    emitter.on("step-usage", (e: { usage?: UsageBlock }) => {
        refreshStatusLine(e.usage);
    });
    emitter.on("subagent-step-usage", () => {
        // Cost only — a subagent's context is not the main context.
        refreshStatusLine();
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
            refreshStatusLine();
            tui.requestRender();
        },
    );
    emitter.on("finish", (event: { usage?: UsageBlock; lastStepUsage?: UsageBlock }) => {
        history.finishAssistant();
        refreshStatusLine(pickContextUsage(event));
        tui.requestRender();
    });
    emitter.on("error", (err: unknown) => {
        history.addError(formatError(err));
        tui.requestRender();
    });
    // Recap generation is detached from the turn — this may fire after busy is
    // already false, and renders wherever the chat currently ends.
    emitter.on("data-recap", (e: { text: string }) => {
        history.addRecap(e.text);
        refreshStatusLine();
        tui.requestRender();
    });
}
