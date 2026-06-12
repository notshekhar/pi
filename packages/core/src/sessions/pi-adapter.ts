import type { Entry, ProviderId, SubagentActivityPart, UsageBlock } from "../types";

/** Activity is structured parts; entries written before that were one string. */
function parseActivity(raw: unknown): SubagentActivityPart[] | undefined {
    if (typeof raw === "string") return raw ? [{ type: "text", text: raw }] : undefined;
    if (!Array.isArray(raw)) return undefined;
    const parts = raw.filter(
        (p): p is SubagentActivityPart =>
            !!p &&
            typeof p === "object" &&
            ((p.type === "text" && typeof p.text === "string") ||
                (p.type === "reasoning" && typeof p.text === "string") ||
                (p.type === "tool" && typeof p.name === "string" && typeof p.summary === "string")),
    );
    return parts.length ? parts : undefined;
}

/** Tree fields (id/parentId) pass through so pi-mono branched sessions keep their shape. */
function treeFields(obj: Record<string, unknown>): { id?: string; parentId?: string | null } {
    const out: { id?: string; parentId?: string | null } = {};
    if (typeof obj.id === "string") out.id = obj.id;
    if (obj.parentId === null || typeof obj.parentId === "string") out.parentId = obj.parentId as string | null;
    return out;
}

/**
 * Adapt a raw JSON line from a pi (or pi-agent) session into our Entry shape.
 * Unknown shapes fall back to { type: "custom", payload }.
 */
export function adaptPiEntry(raw: unknown): Entry | null {
    if (!raw || typeof raw !== "object") return null;
    const obj = raw as Record<string, unknown>;
    const ts = typeof obj.ts === "number" ? obj.ts : typeof obj.timestamp === "number" ? obj.timestamp : Date.now();
    const tree = treeFields(obj);

    switch (obj.type) {
        case "session-info":
            return {
                type: "session-info",
                ts,
                createdAt: typeof obj.createdAt === "number" ? obj.createdAt : ts,
                cwd: String(obj.cwd ?? ""),
                provider: (obj.provider as ProviderId) ?? "xai",
                model: String(obj.model ?? ""),
                parentSession: typeof obj.parentSession === "string" ? obj.parentSession : undefined,
                ...tree,
                // session-info doubles as our SessionInfoData (id = session id),
                // which is also this root entry's tree id.
                id: tree.id ?? String(obj.id ?? ""),
            } as Entry;
        case "message": {
            // pi-mono nests the message: { type: "message", id, parentId, message: { role, content } }
            const nested = obj.message as Record<string, unknown> | undefined;
            const role = (nested?.role ?? obj.role) as string | undefined;
            const content = nested ? nested.content : obj.content;
            const mappedRole = role === "toolResult" || role === "tool" ? "tool" : role === "assistant" ? "assistant" : "user";
            return {
                type: "message",
                ts,
                role: mappedRole,
                content,
                usage: obj.usage as UsageBlock | undefined,
                ...tree,
            };
        }
        case "subagent":
            return {
                type: "subagent",
                ts,
                agent: String(obj.agent ?? "default"),
                prompt: String(obj.prompt ?? ""),
                result: String(obj.result ?? ""),
                activity: parseActivity(obj.activity),
                usage: obj.usage as UsageBlock | undefined,
                ...tree,
            };
        case "model-change":
            return { type: "model-change", ts, from: String(obj.from ?? ""), to: String(obj.to ?? ""), ...tree };
        case "model_change":
            return { type: "model-change", ts, from: "", to: String(obj.modelId ?? ""), ...tree };
        case "compact":
            return {
                type: "compact",
                ts,
                summary: String(obj.summary ?? ""),
                cutAt: typeof obj.cutAt === "number" ? obj.cutAt : 0,
                tokensBefore: typeof obj.tokensBefore === "number" ? obj.tokensBefore : 0,
                tokensAfter: typeof obj.tokensAfter === "number" ? obj.tokensAfter : 0,
                ...tree,
            };
        case "branch-summary":
            return {
                type: "branch-summary",
                ts,
                summary: String(obj.summary ?? ""),
                fromId: typeof obj.fromId === "string" ? obj.fromId : undefined,
                ...tree,
            };
        // pi-mono branch summaries
        case "branch_summary":
            return {
                type: "branch-summary",
                ts,
                summary: String(obj.summary ?? ""),
                fromId: typeof obj.fromId === "string" ? obj.fromId : undefined,
                ...tree,
            };
        case "label":
            return {
                type: "label",
                ts,
                targetId: String(obj.targetId ?? ""),
                label: typeof obj.label === "string" ? obj.label : undefined,
                ...tree,
            };
        default:
            // pi-specific shapes: user-prompt, assistant-message, tool-call, tool-result, etc.
            if (obj.type === "user-prompt" || obj.role === "user") {
                return { type: "message", ts, role: "user", content: obj.content ?? obj.text ?? "", ...tree };
            }
            if (obj.type === "assistant-message" || obj.role === "assistant") {
                return { type: "message", ts, role: "assistant", content: obj.content ?? obj.text ?? "", ...tree };
            }
            return { type: "custom", ts, payload: obj, ...tree };
    }
}
