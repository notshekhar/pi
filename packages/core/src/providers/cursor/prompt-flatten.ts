import type { LanguageModelV2Prompt } from "@ai-sdk/provider";

// Cursor SDK manages conversation state inside the Agent instance — there's
// no way to pass a multi-turn history when calling `agent.send(text)`.
//
// Our agent loop calls doStream() fresh each turn, creating a new cursor
// Agent each time → cursor sees no prior context. Until we wire
// `Agent.resume(agentId)` into session storage (v2), we extract only the
// most recent user turn for the cursor agent.
//
// Lossy: tool calls/results become inline text, system prompt prepended.

function jsonify(value: unknown): string {
  if (value == null) return "";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

export function flattenPromptToText(prompt: LanguageModelV2Prompt): string {
  const lines: string[] = [];
  for (const msg of prompt) {
    switch (msg.role) {
      case "system":
        lines.push(`[system]\n${msg.content}`);
        break;
      case "user": {
        const text = msg.content
          .map((p) => (p.type === "text" ? p.text : `[file:${p.filename ?? "?"}]`))
          .join("");
        lines.push(`[user]\n${text}`);
        break;
      }
      case "assistant": {
        const parts: string[] = [];
        for (const p of msg.content) {
          if (p.type === "text") parts.push(p.text);
          else if (p.type === "reasoning") parts.push(`<thinking>${p.text}</thinking>`);
          else if (p.type === "tool-call")
            parts.push(`<tool-call name="${p.toolName}">${jsonify(p.input)}</tool-call>`);
          else if (p.type === "tool-result")
            parts.push(`<tool-result>${jsonify(p.output)}</tool-result>`);
        }
        lines.push(`[assistant]\n${parts.join("\n")}`);
        break;
      }
      case "tool":
        for (const p of msg.content) {
          lines.push(`[tool-result ${p.toolName}]\n${jsonify(p.output)}`);
        }
        break;
    }
  }
  return lines.join("\n\n");
}

// Extract only the most recent user message text. The default path for the
// cursor adapter — cursor's agent has its own system prompt + tools, so
// dumping pi's full history would just bloat the request.
export function extractLastUserMessage(prompt: LanguageModelV2Prompt): string {
  for (let i = prompt.length - 1; i >= 0; i--) {
    const msg = prompt[i];
    if (msg.role === "user") {
      return msg.content
        .filter((p): p is { type: "text"; text: string } => p.type === "text")
        .map((p) => p.text)
        .join("");
    }
  }
  return "";
}
