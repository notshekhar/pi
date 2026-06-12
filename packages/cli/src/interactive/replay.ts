import { isRecapPayload, parseModelId, type Entry, type Session } from "@notshekhar/pi-core";
import type { ChatHistory } from "./components/chat-history";

/**
 * Render the session's current branch path (root → leaf) into the chat.
 * Shared by /resume, /fork, and /tree navigation so all three replay the
 * same way. Path-based: abandoned branches don't render.
 */
export function renderSessionBranch(session: Session, history: ChatHistory, modelId: string): void {
    const path = session.getBranch();

    let latestCompact: Extract<Entry, { type: "compact" }> | undefined;
    for (const e of path) {
        if (e.type === "compact") latestCompact = e;
    }
    if (latestCompact) {
        history.addCompactionSummary(latestCompact.summary, latestCompact.tokensBefore, latestCompact.ts);
    }

    const { provider } = parseModelId(modelId);
    let messageIndex = 0;
    for (const e of path) {
        if (e.type === "message") {
            const currentMessageIndex = messageIndex++;
            if (latestCompact && currentMessageIndex < latestCompact.cutAt) continue;
            const content = String(e.content ?? "");
            if (e.role === "user") {
                history.addUser(content);
            } else if (e.role === "assistant") {
                history.ensureAssistant(provider, modelId);
                history.appendAssistantDelta(content, provider, modelId);
                history.finishAssistant();
            }
        } else if (e.type === "subagent") {
            if (latestCompact && messageIndex < latestCompact.cutAt) continue;
            // Replay the task box exactly like a live run's final state: same
            // { history, report } shape the task tool outputs.
            const id = `replay-task-${e.ts}`;
            history.addToolCall("task", id, { agent: e.agent, prompt: e.prompt });
            history.addToolResult(id, e.activity ? { history: e.activity, report: e.result } : e.result);
        } else if (e.type === "branch-summary" && e.summary) {
            if (latestCompact && messageIndex < latestCompact.cutAt) continue;
            history.addBranchSummary(e.summary);
        } else if (e.type === "custom" && isRecapPayload(e.payload)) {
            if (latestCompact && messageIndex < latestCompact.cutAt) continue;
            history.addRecap(e.payload.text);
        }
    }
}
