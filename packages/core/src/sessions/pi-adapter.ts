import type { Entry, ProviderId, UsageBlock } from "../types";

/**
 * Adapt a raw JSON line from a pi (or pi-agent) session into our Entry shape.
 * Unknown shapes fall back to { type: "custom", payload }.
 */
export function adaptPiEntry(raw: unknown): Entry | null {
  if (!raw || typeof raw !== "object") return null;
  const obj = raw as Record<string, unknown>;
  const ts = typeof obj.ts === "number" ? obj.ts : typeof obj.timestamp === "number" ? obj.timestamp : Date.now();

  switch (obj.type) {
    case "session-info":
      return {
        type: "session-info",
        ts,
        id: String(obj.id ?? ""),
        createdAt: typeof obj.createdAt === "number" ? obj.createdAt : ts,
        cwd: String(obj.cwd ?? ""),
        provider: (obj.provider as ProviderId) ?? "xai",
        model: String(obj.model ?? ""),
      };
    case "message":
      return {
        type: "message",
        ts,
        role: (obj.role as "user" | "assistant" | "tool") ?? "user",
        content: obj.content,
        usage: obj.usage as UsageBlock | undefined,
      };
    case "model-change":
      return { type: "model-change", ts, from: String(obj.from ?? ""), to: String(obj.to ?? "") };
    case "compact":
      return {
        type: "compact",
        ts,
        summary: String(obj.summary ?? ""),
        cutAt: typeof obj.cutAt === "number" ? obj.cutAt : 0,
        tokensBefore: typeof obj.tokensBefore === "number" ? obj.tokensBefore : 0,
        tokensAfter: typeof obj.tokensAfter === "number" ? obj.tokensAfter : 0,
      };
    case "branch-summary":
      return { type: "branch-summary", ts, summary: String(obj.summary ?? "") };
    default:
      // pi-specific shapes: user-prompt, assistant-message, tool-call, tool-result, etc.
      if (obj.type === "user-prompt" || obj.role === "user") {
        return { type: "message", ts, role: "user", content: obj.content ?? obj.text ?? "" };
      }
      if (obj.type === "assistant-message" || obj.role === "assistant") {
        return { type: "message", ts, role: "assistant", content: obj.content ?? obj.text ?? "" };
      }
      return { type: "custom", ts, payload: obj };
  }
}
