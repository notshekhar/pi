/**
 * Subagents — the task tool runs a nested agent loop (own context window,
 * own toolset from the chosen agent). Streaming surfaces through the parent
 * emitter (subagent-delta / subagent-tool / subagent-finish, keyed by the
 * task toolCallId) and usage aggregates into the parent's CostTracker.
 * Completed runs persist as `subagent` session entries so resumes keep the
 * task box, its report, and its cost.
 */
import { stepCountIs, tool, ToolLoopAgent } from "ai";
import { z } from "zod";
import type { TurnEmitter } from "./events";
import { getModel } from "../providers";
import { getSetting } from "../settings";
import { createTools } from "../tools";
import type { Session } from "../sessions";
import type { UsageBlock } from "../types";
import { buildSystemPrompt } from "./system-prompt";
import { agentExists, DEFAULT_AGENT_NAME, getAgentPrompt, getAgentTools, listAgents } from "./agents";
import { runHooks } from "./hooks";
import { withToolHooks } from "./tool-hooks";
import type { CostTracker } from "./cost";

const SUBAGENT_SYSTEM_SUFFIX = `

You are a subagent launched by a main agent. Rules of the run:
- Work autonomously — there is no user to ask; resolve ambiguity with the most reasonable reading of the prompt and say which reading you chose.
- Stay on the given task. Do not expand scope or touch anything the prompt didn't cover.
- Only your final message returns to the main agent. Make it a complete, self-contained report: what you did or found, exact file paths and names, and anything surprising — the main agent has none of your context.
- If you cannot finish, report exactly how far you got and what blocked you; a precise partial report beats a vague complete-sounding one.`;

export interface SubagentCtx {
    modelId: string;
    cwd: string;
    tracker: CostTracker;
    emitter: TurnEmitter;
    abortSignal?: AbortSignal;
    session: Session;
    sessionId: string;
    transcriptPath: string;
    /** Caller's cap on subagent tools — intersected with the target agent's
     * own tools, so delegation can never widen access (e.g. plan spawns
     * read-only subagents even when targeting an unrestricted agent). */
    subagentToolCap?: string[];
}

export function createTaskTool(ctx: SubagentCtx) {
    const agentNames = listAgents()
        .map((a) => a.name)
        .join(", ");
    return tool({
        description:
            "Launch a subagent to handle a self-contained task and return its final report. " +
            "Use for context-heavy or parallelizable work (broad searches, analysis, multi-file changes) " +
            "to keep the main context small. The subagent runs autonomously with its own context window " +
            "and cannot ask questions — include everything it needs in the prompt, and say what output you expect. " +
            "Call task on its own: do not combine it with other tool calls in the same step — the subagent " +
            "covers the exploration itself.",
        inputSchema: z.object({
            agent: z
                .string()
                .optional()
                .describe(`Agent to run the task with (its prompt + tool restrictions apply). One of: ${agentNames}`),
            prompt: z.string().describe("Complete task description for the subagent, including the expected output"),
        }),
        execute: (input, options) =>
            runSubagent(ctx, input.agent, input.prompt, (options as { toolCallId?: string })?.toolCallId ?? "task"),
    });
}

/**
 * Resolve a subagent's effective tool names: the target agent's own tools,
 * intersected with the caller's cap. `task` is always stripped (no nesting).
 * Pure + exported so the security boundary is unit-testable.
 *   - allFileTools: the file-tool names available (no "task")
 *   - targetTools:  target agent's tools (undefined = all)
 *   - cap:          caller's subagent-tools cap (undefined = no cap)
 */
export function resolveSubagentTools(
    allFileTools: string[],
    targetTools: string[] | undefined,
    cap: string[] | undefined,
): string[] {
    const base = (targetTools?.length ? targetTools : allFileTools).filter((t) => allFileTools.includes(t));
    const capped = cap?.length ? base.filter((t) => cap.includes(t)) : base;
    return capped.filter((t) => t !== "task");
}

async function runSubagent(
    ctx: SubagentCtx,
    agentName: string | undefined,
    prompt: string,
    toolCallId: string,
): Promise<string> {
    const name = agentName && agentExists(agentName) ? agentName : DEFAULT_AGENT_NAME;
    try {
        const full = createTools({ cwd: ctx.cwd, abortSignal: ctx.abortSignal });
        const effective = resolveSubagentTools(Object.keys(full), getAgentTools(name), ctx.subagentToolCap);
        const subTools = Object.fromEntries(Object.entries(full).filter(([n]) => effective.includes(n))) as typeof full;
        // Subagent tool calls run the same PreToolUse/PostToolUse hooks,
        // tagged with agent_id so watchers can tell them apart.
        const hooked = withToolHooks(subTools, {
            cwd: ctx.cwd,
            sessionId: ctx.sessionId,
            transcriptPath: ctx.transcriptPath,
            emitter: ctx.emitter,
            agentId: toolCallId,
        });
        const maxSteps = getSetting("subagentMaxSteps") || 50;
        // AI SDK's native agent loop — same streamText core runTurn uses, with
        // the loop/stop handling owned by the SDK.
        const agent = new ToolLoopAgent({
            model: await getModel(ctx.modelId),
            instructions:
                buildSystemPrompt({ cwd: ctx.cwd, basePrompt: getAgentPrompt(name), tools: Object.keys(subTools) }) +
                SUBAGENT_SYSTEM_SUFFIX,
            tools: hooked,
            stopWhen: stepCountIs(maxSteps),
        });
        const result = await agent.stream({ prompt, abortSignal: ctx.abortSignal });

        let text = "";
        let totalUsage: UsageBlock | undefined;
        for await (const part of result.fullStream) {
            if (ctx.abortSignal?.aborted) break;
            switch (part.type) {
                case "text-delta":
                    text += part.text;
                    ctx.emitter.emit("subagent-delta", { toolCallId, agent: name, text: part.text });
                    break;
                case "tool-call":
                    ctx.emitter.emit("subagent-tool", {
                        toolCallId,
                        agent: name,
                        toolName: (part as { toolName?: string }).toolName,
                        input: (part as { input?: unknown }).input,
                    });
                    break;
                case "finish-step": {
                    // Per-step cost accrual into the parent's tracker — the
                    // footer ticks while the subagent works, and aborts keep
                    // the spend of completed steps.
                    const u = (part as { usage?: UsageBlock }).usage;
                    if (u) {
                        ctx.tracker.add(ctx.modelId, u, ctx.cwd);
                        ctx.emitter.emit("subagent-step-usage", { toolCallId, agent: name, usage: u });
                    }
                    break;
                }
                case "finish": {
                    totalUsage = (part as { totalUsage?: UsageBlock }).totalUsage;
                    ctx.emitter.emit("subagent-finish", { toolCallId, agent: name, usage: totalUsage });
                    break;
                }
            }
        }

        // SubagentStop hooks — informational for watchers, block is meaningless.
        const stop = await runHooks(
            "SubagentStop",
            undefined,
            {
                session_id: ctx.sessionId,
                transcript_path: ctx.transcriptPath,
                agent_id: toolCallId,
                stop_hook_active: false,
            },
            ctx.cwd,
        );
        for (const m of stop.messages) ctx.emitter.emit("hook-message", m);
        for (const s of stop.terminalSequences) ctx.emitter.emit("hook-terminal-sequence", s);

        // Persist the run so resumed sessions keep the task box, its report,
        // and its cost (the main turn's usage doesn't include subagent steps).
        const report = text.trim() || "(subagent produced no output)";
        await ctx.session.append({
            type: "subagent",
            ts: Date.now(),
            agent: name,
            prompt,
            result: report,
            usage: totalUsage,
        });

        // Plain text result — it renders as-is in the tool box and is exactly
        // what the parent model reads. No JSON wrapping.
        return report;
    } catch (err) {
        return `Subagent failed: ${err instanceof Error ? err.message : String(err)}`;
    }
}
