/**
 * PreToolUse / PostToolUse wrapping for tool sets — every tool execute runs
 * through the hook dispatcher. Shared by the main agent loop and subagents
 * (subagents pass agentId so hook payloads carry agent_id, Claude Code parity).
 */
import type { TurnEmitter } from "./events";
import { runHooks } from "./hooks";
import type { ToolCallMiddleware, ToolResultMiddleware, ToolCallContext, TurnContext } from "../extensions/api";

type AnyTool = { execute?: (input: unknown, options: unknown) => Promise<unknown> };

/** A plain `{}` object — the only output shape we can safely merge a field into. */
function isPlainObject(value: unknown): value is Record<string, unknown> {
    return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Attach hook feedback without mutating or corrupting the tool's output. Only a
 * plain object can take an extra field; arrays (e.g. MCP content blocks) and
 * primitives would be wrecked by a spread, so they're nested under `result`
 * instead. Keeps any tool's output shape intact for persistence + replay.
 */
export function attachHookFeedback(output: unknown, feedback: string): unknown {
    if (isPlainObject(output)) return { ...output, hook_feedback: feedback };
    return { result: output, hook_feedback: feedback };
}

export interface ToolHookCtx {
    cwd: string;
    sessionId: string;
    transcriptPath: string;
    emitter: TurnEmitter;
    /** Set for subagent tool calls — tags hook payloads with agent_id. */
    agentId?: string;
    /**
     * Extension-contributed result transforms (api.tools.onResult). Applied to
     * string tool results after execute, before PostToolUse. Empty/undefined →
     * the wrapper is a pure pass-through and tool output is untouched (the
     * zero-extensions case must be behaviorally identical to before).
     */
    resultMiddleware?: { match: (name: string) => boolean; mw: ToolResultMiddleware }[];
    /** Extension input intercepts (api.tools.onCall): rewrite args or block, pre-execute. */
    callMiddleware?: { match: (name: string) => boolean; mw: ToolCallMiddleware }[];
    /** Turn info given to call/result middleware (which agent/model/tools are running). */
    turnContext?: TurnContext;
}

/**
 * Wraps every tool's execute with PreToolUse / PostToolUse hooks.
 * Pre: deny → the tool never runs; the reason is returned as the tool result
 * so the model can react. updatedInput replaces the arguments.
 * Post: block/additionalContext are attached to the result as hook_feedback.
 */
export function withToolHooks<T extends object>(tools: T, ctx: ToolHookCtx): T {
    const agentFields = ctx.agentId ? { agent_id: ctx.agentId } : {};
    const wrapped: Record<string, AnyTool> = {};
    for (const [name, t] of Object.entries(tools as Record<string, AnyTool>)) {
        if (!t.execute) {
            wrapped[name] = t;
            continue;
        }
        wrapped[name] = {
            ...t,
            execute: async (input: unknown, options: unknown) => {
                const pre = await runHooks(
                    "PreToolUse",
                    name,
                    {
                        session_id: ctx.sessionId,
                        transcript_path: ctx.transcriptPath,
                        tool_name: name,
                        tool_input: input,
                        ...agentFields,
                    },
                    ctx.cwd,
                );
                for (const m of pre.messages) ctx.emitter.emit("hook-message", m);
                for (const s of pre.terminalSequences) ctx.emitter.emit("hook-terminal-sequence", s);
                if (pre.block) {
                    // Watchers treat Notification as "agent needs attention" — fire it
                    // for permission-style denials; its own outcome is ignored.
                    void runHooks(
                        "Notification",
                        undefined,
                        {
                            session_id: ctx.sessionId,
                            transcript_path: ctx.transcriptPath,
                            message: `Permission needed: ${name} — ${pre.reason}`,
                            title: "loop",
                        },
                        ctx.cwd,
                    ).then((n) => {
                        for (const s of n.terminalSequences) ctx.emitter.emit("hook-terminal-sequence", s);
                    });
                    return { error: `blocked by PreToolUse hook: ${pre.reason}` };
                }
                let effectiveInput = pre.updatedInput !== undefined ? pre.updatedInput : input;
                // Hook rewrote the input (e.g. rtk): update the rendered tool call in
                // place so the chat shows what actually executed — no separate line.
                // Main agent only: subagent tool calls aren't rendered as own
                // components, and their ids could collide with main ones.
                if (pre.updatedInput !== undefined && !ctx.agentId) {
                    const toolCallId = (options as { toolCallId?: string } | undefined)?.toolCallId;
                    ctx.emitter.emit("tool-input-updated", { toolCallId, toolName: name, input: effectiveInput });
                }
                // Extension onCall middleware: rewrite the input or block the call,
                // before execute. Runs after PreToolUse so the two compose. Skipped
                // (and effectiveInput untouched) when no extensions are loaded.
                const callMws = ctx.callMiddleware;
                if (callMws && callMws.length > 0) {
                    const toolCallId = (options as { toolCallId?: string } | undefined)?.toolCallId;
                    let changed = false;
                    for (const { match, mw } of callMws) {
                        if (!match(name)) continue;
                        try {
                            const r = await mw(effectiveInput, {
                                ...(ctx.turnContext ?? ({} as TurnContext)),
                                cwd: ctx.cwd,
                                toolName: name,
                                toolCallId,
                                input: effectiveInput,
                            });
                            if (r === false) return { error: `blocked by extension onCall: ${name}` };
                            if (r !== undefined) {
                                effectiveInput = r;
                                changed = true;
                            }
                        } catch {
                            // a misbehaving intercept must never break a tool call
                        }
                    }
                    if (changed && !ctx.agentId) {
                        ctx.emitter.emit("tool-input-updated", { toolCallId, toolName: name, input: effectiveInput });
                    }
                }
                const output = await t.execute!(effectiveInput, options);
                // Extension result middleware (api.tools.onResult). Operates on
                // string results only (append/transform text, e.g. LSP
                // diagnostics). When none are registered this whole block is
                // skipped and `result === output` by reference — identical to
                // the pre-extensions path.
                let result = output;
                const mws = ctx.resultMiddleware;
                if (mws && mws.length > 0 && typeof result === "string") {
                    const toolCallId = (options as { toolCallId?: string } | undefined)?.toolCallId;
                    const callCtx: ToolCallContext = {
                        ...(ctx.turnContext ?? ({} as TurnContext)),
                        cwd: ctx.cwd,
                        toolName: name,
                        toolCallId,
                        input: effectiveInput,
                    };
                    for (const { match, mw } of mws) {
                        if (!match(name)) continue;
                        try {
                            const next = await mw(result as string, callCtx);
                            if (typeof next === "string") result = next;
                        } catch {
                            // A misbehaving extension transform must never break a tool call.
                        }
                    }
                }
                const post = await runHooks(
                    "PostToolUse",
                    name,
                    {
                        session_id: ctx.sessionId,
                        transcript_path: ctx.transcriptPath,
                        tool_name: name,
                        tool_input: effectiveInput,
                        // Claude Code sends `tool_response`; keep `tool_output` too for
                        // any loop hooks already written against it.
                        tool_response: result,
                        tool_output: result,
                        ...agentFields,
                    },
                    ctx.cwd,
                );
                for (const m of post.messages) ctx.emitter.emit("hook-message", m);
                for (const s of post.terminalSequences) ctx.emitter.emit("hook-terminal-sequence", s);
                const feedback = [post.block ? `BLOCKED: ${post.reason}` : null, post.additionalContext]
                    .filter(Boolean)
                    .join("\n");
                if (feedback) return attachHookFeedback(result, feedback);
                return result;
            },
        };
    }
    return wrapped as unknown as T;
}
