import type { TUI } from "@notshekhar/pi-tui";
import { subagentArgSummary } from "@notshekhar/pi-core";
import type { ChatHistory } from "./components/chat-history";

/** Subagent activity buffers are tail-capped — only the last slice renders. */
const MAX_BUFFER_BYTES = 6000;
/**
 * Subagent deltas arrive per token — rebuilding the tool box for each one burns
 * CPU for invisible frames. Dirty ids flush on this timer instead.
 */
const FLUSH_INTERVAL_MS = 50;

/**
 * Live subagent streaming for the task tool: activity renders inside the task
 * tool's box (keyed by the task toolCallId), coalesced behind a ~50ms timer.
 * The buffer is intentionally kept on subagent-finish — tool-result composes it
 * into the final display, then calls clear().
 */
export interface SubagentStream {
    /** A subagent invoked a tool — append a `> tool args` line. */
    onTool(toolCallId: string, toolName: string | undefined, input: unknown): void;
    /** A subagent emitted assistant text — append it to the buffer. */
    onDelta(toolCallId: string, text: string): void;
    /** Drop a finished subagent's buffer/status/dirty state. */
    clear(toolCallId: string): void;
}

export function createSubagentStream(history: ChatHistory, tui: TUI): SubagentStream {
    const buffers = new Map<string, string>();
    const statuses = new Map<string, string>();
    const dirty = new Set<string>();
    let flushTimer: ReturnType<typeof setTimeout> | null = null;

    const flush = (): void => {
        flushTimer = null;
        for (const id of dirty) {
            const buf = buffers.get(id);
            if (buf === undefined) continue; // already finished
            history.updateToolProgress(id, buf);
            const status = statuses.get(id);
            if (status) history.setToolStatus(id, status);
        }
        dirty.clear();
        tui.requestRender();
    };

    const queueRepaint = (id: string): void => {
        dirty.add(id);
        if (!flushTimer) flushTimer = setTimeout(flush, FLUSH_INTERVAL_MS);
    };

    return {
        onTool(toolCallId, toolName, input) {
            const prev = buffers.get(toolCallId) ?? "";
            const line = `> ${toolName ?? "tool"}${subagentArgSummary(input)}\n`;
            const next = `${prev}${prev && !prev.endsWith("\n") ? "\n" : ""}${line}`.slice(-MAX_BUFFER_BYTES);
            buffers.set(toolCallId, next);
            statuses.set(toolCallId, toolName ?? "running");
            queueRepaint(toolCallId);
        },
        onDelta(toolCallId, text) {
            const next = ((buffers.get(toolCallId) ?? "") + text).slice(-MAX_BUFFER_BYTES);
            buffers.set(toolCallId, next);
            statuses.set(toolCallId, "writing");
            queueRepaint(toolCallId);
        },
        clear(toolCallId) {
            buffers.delete(toolCallId);
            statuses.delete(toolCallId);
            dirty.delete(toolCallId);
        },
    };
}
