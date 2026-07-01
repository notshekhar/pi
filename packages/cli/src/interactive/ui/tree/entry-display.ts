/**
 * Row presentation for the /tree selector: one-line display text, search
 * text, and label timestamps per entry type. No layout or input concerns.
 *
 * Tool rows resolve nicely: a `tool` message carries `tool-result` blocks that
 * only reference a `toolCallId`, so we look the call up in a map built from the
 * assistant `tool-call` blocks and render its name + args (`read src/x.ts`,
 * `bash git status`) — the same summary the live tool box shows.
 */
import { stripSessionHookContext, type SessionTreeNode } from "@notshekhar/loop-core";
import { theme } from "../theme";
import { formatToolInvocation } from "../tool-summary";

const MAX_CONTENT_LEN = 200;

/** A resolved tool call, keyed by toolCallId for tool-result rows to look up. */
export interface ToolCallInfo {
    toolName: string;
    input: Record<string, unknown>;
}

/** Context threaded into row/search rendering so tool calls resolve to args. */
export interface TreeRenderContext {
    toolCalls: Map<string, ToolCallInfo>;
    cwd: string;
}

/**
 * Index every assistant `tool-call` block in the tree by its toolCallId, so a
 * later `tool-result` row can render the original name + arguments. Built once
 * per tree (walks all branches, not just the active path).
 */
export function buildToolCallMap(roots: SessionTreeNode[]): Map<string, ToolCallInfo> {
    const map = new Map<string, ToolCallInfo>();
    const stack = [...roots];
    while (stack.length > 0) {
        const node = stack.pop()!;
        const entry = node.entry;
        if (entry.type === "message" && entry.role === "assistant" && Array.isArray(entry.content)) {
            for (const block of entry.content) {
                if (block && typeof block === "object" && (block as { type?: string }).type === "tool-call") {
                    const tc = block as { toolCallId?: string; toolName?: string; input?: unknown };
                    if (tc.toolCallId) {
                        map.set(tc.toolCallId, {
                            toolName: tc.toolName ?? "tool",
                            input: (tc.input ?? {}) as Record<string, unknown>,
                        });
                    }
                }
            }
        }
        for (const child of node.children) stack.push(child);
    }
    return map;
}

/**
 * Describe the tool calls/results carried by a message's structured content as
 * a comma-joined one-liner (`read src/x.ts, bash git status`). Empty when the
 * content holds no tool blocks.
 */
function describeToolBlocks(content: unknown, ctx: TreeRenderContext): string {
    if (!Array.isArray(content)) return "";
    const labels: string[] = [];
    for (const block of content) {
        if (!block || typeof block !== "object" || !("type" in block)) continue;
        const type = (block as { type?: string }).type;
        if (type === "tool-call") {
            const b = block as { toolName?: string; input?: unknown };
            labels.push(
                formatToolInvocation(b.toolName ?? "tool", (b.input ?? {}) as Record<string, unknown>, ctx.cwd),
            );
        } else if (type === "tool-result") {
            const b = block as { toolCallId?: string; toolName?: string };
            const call = b.toolCallId ? ctx.toolCalls.get(b.toolCallId) : undefined;
            labels.push(call ? formatToolInvocation(call.toolName, call.input, ctx.cwd) : (b.toolName ?? "tool"));
        }
    }
    return labels.join(", ");
}

function extractContent(content: unknown): string {
    if (typeof content === "string") return content.slice(0, MAX_CONTENT_LEN);
    if (Array.isArray(content)) {
        let result = "";
        for (const c of content) {
            if (typeof c === "object" && c !== null && "type" in c && c.type === "text") {
                result += (c as { text: string }).text;
                if (result.length >= MAX_CONTENT_LEN) return result.slice(0, MAX_CONTENT_LEN);
            }
        }
        return result;
    }
    return "";
}

/** User message text for display: hook-context wrapper stripped. */
function userContent(content: unknown): string {
    return stripSessionHookContext(typeof content === "string" ? content : extractContent(content)).slice(
        0,
        MAX_CONTENT_LEN,
    );
}

export function hasTextContent(content: unknown): boolean {
    if (typeof content === "string") return content.trim().length > 0;
    if (Array.isArray(content)) {
        for (const c of content) {
            if (typeof c === "object" && c !== null && "type" in c && c.type === "text") {
                const text = (c as { text?: string }).text;
                if (text && text.trim().length > 0) return true;
            }
        }
    }
    return false;
}

/** One-line themed row text for an entry. */
export function getEntryDisplayText(node: SessionTreeNode, isSelected: boolean, ctx?: TreeRenderContext): string {
    const entry = node.entry;
    let result: string;

    const normalize = (s: string) => s.replace(/[\n\t]/g, " ").trim();

    switch (entry.type) {
        case "message": {
            if (entry.role === "user") {
                result = theme.fg("accent", "user: ") + normalize(userContent(entry.content));
            } else if (entry.role === "assistant") {
                const textContent = normalize(extractContent(entry.content));
                if (textContent) {
                    result = theme.fg("success", "assistant: ") + textContent;
                } else {
                    // A pure tool-call turn (no prose): show the calls it made
                    // instead of "(no content)".
                    const tools = ctx ? describeToolBlocks(entry.content, ctx) : "";
                    result =
                        theme.fg("success", "assistant: ") +
                        theme.fg("muted", tools ? normalize(tools) : "(no content)");
                }
            } else {
                // tool result: resolve to the original call's name + args.
                const desc = ctx ? describeToolBlocks(entry.content, ctx) : "";
                const fallback = normalize(extractContent(entry.content)).slice(0, 80);
                result = theme.fg("muted", desc || fallback || "tool");
            }
            break;
        }
        case "subagent":
            result = theme.fg("muted", `[subagent ${entry.agent}]: `) + normalize(entry.prompt).slice(0, 80);
            break;
        case "compact": {
            const tokens = Math.round(entry.tokensBefore / 1000);
            result = theme.fg("borderAccent", `[compaction: ${tokens}k tokens]`);
            break;
        }
        case "branch-summary":
            result = theme.fg("warning", `[branch summary]: `) + normalize(entry.summary);
            break;
        case "model-change":
            result = theme.fg("dim", `[model: ${entry.to}]`);
            break;
        case "session-info":
            result = theme.fg("dim", `[session start]`);
            break;
        case "custom":
            result = theme.fg("dim", `[custom]`);
            break;
        case "label":
            result = theme.fg("dim", `[label: ${entry.label ?? "(cleared)"}]`);
            break;
        default:
            result = "";
    }

    return isSelected ? theme.bold(result) : result;
}

/** Plain text used for type-to-search matching. */
export function getSearchableText(node: SessionTreeNode, ctx?: TreeRenderContext): string {
    const entry = node.entry;
    const parts: string[] = [];

    if (node.label) parts.push(node.label);

    switch (entry.type) {
        case "message":
            if (entry.role === "user") parts.push("user", userContent(entry.content));
            else if (entry.role === "assistant")
                parts.push(
                    "assistant",
                    extractContent(entry.content),
                    ctx ? describeToolBlocks(entry.content, ctx) : "",
                );
            else parts.push("tool", ctx ? describeToolBlocks(entry.content, ctx) : "");
            break;
        case "subagent":
            parts.push("subagent", entry.agent, entry.prompt);
            break;
        case "compact":
            parts.push("compaction");
            break;
        case "branch-summary":
            parts.push("branch summary", entry.summary);
            break;
        case "session-info":
            parts.push("session start");
            break;
        case "model-change":
            parts.push("model", entry.to);
            break;
        case "custom":
            parts.push("custom");
            break;
        case "label":
            parts.push("label", entry.label ?? "");
            break;
    }

    return parts.join(" ");
}

/** Compact local time: today → HH:MM, this year → M/D HH:MM, else YY/M/D HH:MM. */
export function formatLabelTimestamp(timestamp: number): string {
    const date = new Date(timestamp);
    const now = new Date();
    const hours = date.getHours().toString().padStart(2, "0");
    const minutes = date.getMinutes().toString().padStart(2, "0");
    const time = `${hours}:${minutes}`;

    if (
        date.getFullYear() === now.getFullYear() &&
        date.getMonth() === now.getMonth() &&
        date.getDate() === now.getDate()
    ) {
        return time;
    }

    const month = date.getMonth() + 1;
    const day = date.getDate();
    if (date.getFullYear() === now.getFullYear()) {
        return `${month}/${day} ${time}`;
    }

    const year = date.getFullYear().toString().slice(-2);
    return `${year}/${month}/${day} ${time}`;
}
