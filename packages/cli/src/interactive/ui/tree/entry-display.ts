/**
 * Row presentation for the /tree selector: one-line display text, search
 * text, and label timestamps per entry type. No layout or input concerns.
 *
 * Divergence from pi-mono: our tool entries carry raw output (we don't store
 * per-tool-call blocks), so tool rows show a content snippet instead of
 * pi-mono's resolved tool-call name/arguments.
 */
import { stripSessionHookContext, type SessionTreeNode } from "@notshekhar/loop-core";
import { theme } from "../theme";

const MAX_CONTENT_LEN = 200;

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
export function getEntryDisplayText(node: SessionTreeNode, isSelected: boolean): string {
    const entry = node.entry;
    let result: string;

    const normalize = (s: string) => s.replace(/[\n\t]/g, " ").trim();

    switch (entry.type) {
        case "message": {
            if (entry.role === "user") {
                result = theme.fg("accent", "user: ") + normalize(userContent(entry.content));
            } else if (entry.role === "assistant") {
                const textContent = normalize(extractContent(entry.content));
                result = textContent
                    ? theme.fg("success", "assistant: ") + textContent
                    : theme.fg("success", "assistant: ") + theme.fg("muted", "(no content)");
            } else {
                result = theme.fg("muted", `[tool]: ${normalize(extractContent(entry.content)).slice(0, 80)}`);
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
export function getSearchableText(node: SessionTreeNode): string {
    const entry = node.entry;
    const parts: string[] = [];

    if (node.label) parts.push(node.label);

    switch (entry.type) {
        case "message":
            parts.push(entry.role, entry.role === "user" ? userContent(entry.content) : extractContent(entry.content));
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
