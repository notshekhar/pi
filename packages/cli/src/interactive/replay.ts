import { isRecapPayload, parseModelId, type Entry, type Session } from "@notshekhar/loop-core";
import type { ChatHistory } from "./components/chat-history";

/** AI-SDK content part shapes we replay from persisted assistant/tool messages. */
interface ReplayPart {
    type?: string;
    text?: string;
    toolName?: string;
    toolCallId?: string;
    input?: unknown;
    output?: { type?: string; value?: unknown };
}

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
            if (e.role === "user") {
                history.addUser(String(e.content ?? ""));
            } else if (e.role === "assistant") {
                history.ensureAssistant(provider, modelId);
                // Structured content (text + tool-call parts) replays the tool
                // boxes; legacy string content is plain assistant text.
                if (Array.isArray(e.content)) {
                    for (const part of e.content as ReplayPart[]) {
                        if (part.type === "text" && part.text) {
                            history.appendAssistantDelta(part.text, provider, modelId);
                        } else if (part.type === "reasoning" && part.text) {
                            history.appendAssistantThinking(part.text, provider, modelId);
                        } else if (part.type === "tool-call" && part.toolCallId) {
                            history.addToolCall(
                                part.toolName ?? "tool",
                                part.toolCallId,
                                (part.input ?? {}) as Record<string, unknown>,
                            );
                        }
                    }
                } else {
                    history.appendAssistantDelta(String(e.content ?? ""), provider, modelId);
                }
                history.finishAssistant();
            } else if (e.role === "tool") {
                // Tool results: resolve the matching tool box created above.
                if (Array.isArray(e.content)) {
                    for (const part of e.content as ReplayPart[]) {
                        if (part.type === "tool-result" && part.toolCallId) {
                            const isError = part.output?.type === "error-text" || part.output?.type === "error-json";
                            history.addToolResult(part.toolCallId, part.output ?? "", isError);
                        }
                    }
                }
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
