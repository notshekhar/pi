/**
 * Subagents — the task tool runs a nested agent loop (own context window,
 * own toolset from the chosen agent). Streaming surfaces through the parent
 * emitter (subagent-delta / subagent-tool / subagent-finish, keyed by the
 * task toolCallId) and usage aggregates into the parent's CostTracker.
 * Completed runs persist as `subagent` session entries so resumes keep the
 * task box, its report, and its cost.
 */
import { stepCountIs, tool, ToolLoopAgent } from "ai";
import type { ModelMessage } from "ai";
import { z } from "zod";
import type { TurnEmitter } from "./events";
import { effectiveSdkProvider } from "../auth";
import { getModel, parseModelId } from "../providers";
import { anthropicCachedSystem, moveAnthropicCacheTail } from "./model-messages";
import { getSetting } from "../settings";
import { createTools } from "../tools";
import { getMcpManager } from "../mcp";
import { getExtensionHost } from "../extensions";
import type { Session } from "../sessions";
import type { SubagentActivityPart, UsageBlock } from "../types";
import { buildSystemPrompt } from "./system-prompt";
import {
    agentExists,
    DEFAULT_AGENT_NAME,
    getAgentPrompt,
    getAgentTools,
    isReadOnlyBashAgent,
    listAgents,
} from "./agents";
import { runHooks } from "./hooks";
import { withToolHooks } from "./tool-hooks";
import { sumUsage, type CostTracker } from "./cost";

const SUBAGENT_SYSTEM_SUFFIX = `

You are a subagent launched by a main agent. Rules of the run:
- Work autonomously — there is no user to ask; resolve ambiguity with the most reasonable reading of the prompt and state which reading you chose.
- Stay on the given task. Do not expand scope or touch anything the prompt didn't cover.
- Be economical while investigating: take the shortest path to the answer. Don't repeat searches, or exhaustively sweep directories when targeted reads suffice. Stop the moment you can answer — every extra step costs real money and delays the main agent.
- Only your final message returns to the main agent — it is their only window into your work. Write like the main agent would: lead with the outcome, then the insight needed to understand and act (exact paths, names, key findings, conclusions, evidence, surprises). Be precise and concise for what was asked — complete enough that the main agent need not redo your investigation, but no padding and no play-by-play of what you tried.
- If you cannot finish, report exactly how far you got and what blocked you; a precise partial report beats a vague complete-sounding one.`;

/** One-line summary of a tool call's input for the activity log (` arg`, capped). */
export function subagentArgSummary(input: unknown): string {
    if (!input || typeof input !== "object") return "";
    const a = input as Record<string, unknown>;
    const v = a.command ?? a.path ?? a.file_path ?? a.pattern ?? a.prompt;
    if (typeof v !== "string" || !v) return "";
    const one = v.split("\n")[0];
    return ` ${one.length > 70 ? `${one.slice(0, 67)}…` : one}`;
}

export interface SubagentCtx {
    modelId: string;
    cwd: string;
    tracker: CostTracker;
    emitter: TurnEmitter;
    abortSignal?: AbortSignal;
    session: Session;
    sessionId: string;
    transcriptPath: string;
    /** Agent active for this turn (session agent or one-shot /<agent>) — the
     * subagent is a fork of it unless the task call names another agent. */
    turnAgent?: string;
    /** Parent's effective file-tool names. Always the cap on what a subagent
     * may use: a fork inherits exactly the parent's tools, and a named
     * override is intersected with them — delegation never widens access. */
    parentTools: string[];
    /** Parent's workspace context (CLAUDE.md etc.) — a fork sees the same rules. */
    workspaceContext?: string;
    /** Parent's skills prompt block. */
    skillsPrompt?: string;
}

export function createTaskTool(ctx: SubagentCtx) {
    const agentNames = listAgents()
        .map((a) => a.name)
        .join(", ");
    return tool({
        description:
            "Launch a subagent to handle a self-contained task and return its final report. " +
            "Use for context-heavy or parallelizable work (broad searches, analysis, multi-file changes) " +
            "to keep the main context small. By default the subagent is a fork of you — same prompt and " +
            "tools (minus task), fresh context window. It runs autonomously and cannot ask questions — " +
            "include everything it needs in the prompt, and say what output you expect. " +
            "Call task on its own: do not combine it with other tool calls in the same step — the subagent " +
            "covers the exploration itself.",
        inputSchema: z.object({
            agent: z
                .string()
                .optional()
                .describe(
                    `Optional named agent to run instead of a fork of yourself (its prompt applies; its tools are capped to yours). One of: ${agentNames}`,
                ),
            prompt: z.string().describe("Complete task description for the subagent, including the expected output"),
        }),
        execute: (input, options) =>
            runSubagent(ctx, input.agent, input.prompt, (options as { toolCallId?: string })?.toolCallId ?? "task"),
        // Anti-bloat (AI SDK subagents pattern): the parent model receives only
        // the subagent's final report, bounded — never the full history of
        // intermediate tool calls or file contents. The history is the tool
        // *output* (rendered in the tool box, persisted with the session);
        // toModelOutput is what keeps it out of the parent context. Without
        // this a runaway subagent could blow the main context, defeating the
        // point of delegating.
        // NOTE: the SDK calls this with an options object ({ toolCallId, input,
        // output }), not the bare output — destructure, or the parent reads the
        // wrapper and every report degrades to the no-response placeholder.
        toModelOutput: taskToolModelOutput,
    });
}

/** What the parent model receives for a task tool call. Exported for tests:
 * the argument shape must match the SDK's createToolModelOutput call site —
 * an options object, NOT the bare output (regression: reading the wrapper
 * made every subagent report degrade to the no-response placeholder). */
export function taskToolModelOutput({ output }: { output: unknown }): { type: "text"; value: string } {
    return { type: "text", value: boundReport(reportOf(output)) };
}

export interface SubagentOutput {
    /** Full ordered run (text/reasoning/tool parts, stream order) — what the box shows. */
    history: SubagentActivityPart[];
    /** The subagent's final response text — what the parent model reads. */
    report: string;
}

/**
 * Flatten an activity log to display text: text parts as-is, tool parts as
 * "> name summary" lines. Reasoning is skipped (the live view never streams
 * it); it's kept in the parts so a future renderer can style it.
 */
export function formatSubagentActivity(parts: SubagentActivityPart[]): string {
    let out = "";
    for (const p of parts) {
        if (p.type === "tool") {
            out += `${out && !out.endsWith("\n") ? "\n" : ""}> ${p.name}${p.summary}\n`;
        } else if (p.type === "text") {
            out += p.text;
        }
    }
    return out.trim();
}

/** Final text for the model — always non-empty, even when the subagent never
 * produced a closing response (aborted, tool-only run, provider hiccup). */
function reportOf(output: unknown): string {
    const o = output as (Partial<SubagentOutput> & { hook_feedback?: unknown }) | string | null;
    const report = typeof o === "string" ? o : (o?.report ?? "");
    const text = report.trim() || "(subagent finished without a final response)";
    // PostToolUse hooks attach feedback onto the output object — keep it
    // visible to the parent model, not just in the transcript.
    const feedback = typeof o === "object" && o?.hook_feedback ? `\n\n[hook] ${JSON.stringify(o.hook_feedback)}` : "";
    return text + feedback;
}

// Cap the report handed to the parent model. Generous enough for a real
// report, bounded so delegation always shrinks (never grows) main context.
const MAX_REPORT_CHARS = 24_000;
function boundReport(report: string): string {
    if (report.length <= MAX_REPORT_CHARS) return report;
    return `${report.slice(0, MAX_REPORT_CHARS)}\n\n[subagent report truncated at ${MAX_REPORT_CHARS} chars — ask a narrower follow-up task if you need more]`;
}

/**
 * Resolve a subagent's effective tool names: the target agent's own tools,
 * intersected with the parent's effective tools. `task` is always stripped
 * (no nesting). A fork (target = parent agent) resolves to exactly the
 * parent's tools; a named override can only narrow, never widen.
 * Pure + exported so the security boundary is unit-testable.
 *   - allFileTools: the file-tool names available (no "task")
 *   - targetTools:  target agent's tools (undefined = all)
 *   - cap:          parent's effective tools (undefined = no cap)
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
): Promise<SubagentOutput> {
    // No override → fork of the turn's agent (session agent or one-shot
    // /<agent> for this message): same prompt, same tools, fresh context.
    const fork = ctx.turnAgent && agentExists(ctx.turnAgent) ? ctx.turnAgent : DEFAULT_AGENT_NAME;
    const name = agentName && agentExists(agentName) ? agentName : fork;
    try {
        // MCP tools the parent exposed are inheritable: they're already in
        // ctx.parentTools, so the resolver's cap keeps them for a fork and drops
        // them for a named agent that doesn't list them — the same widen/narrow
        // rule files get. They already carry per-call timeouts (set when the
        // manager built them) and run the same hooks below.
        const mcpTools = getMcpManager().getTools();
        // Extension tools (and removals) are candidates too, exactly like MCP —
        // the resolver's parentTools cap then keeps them for a fork and drops
        // them for a named agent that doesn't list them. Empty when no
        // extensions are loaded, so the candidate pool is unchanged.
        const extTools = getExtensionHost().getTools();
        const fileToolNames = Object.keys(createTools({ cwd: ctx.cwd, abortSignal: ctx.abortSignal }));
        const candidateNames = [...fileToolNames, ...Object.keys(mcpTools), ...extTools.add.keys()].filter(
            (n) => !extTools.remove.has(n),
        );
        const effective = resolveSubagentTools(candidateNames, getAgentTools(name), ctx.parentTools);
        // A subagent allowed bash but not write/edit (e.g. a plan fork) gets the
        // same fail-closed read-only sandbox guarantee as the top-level agent.
        const readOnlyFs = isReadOnlyBashAgent(effective);
        const full: Record<string, unknown> = {
            ...createTools({ cwd: ctx.cwd, abortSignal: ctx.abortSignal, readOnlyFs }),
            ...mcpTools,
            ...Object.fromEntries(extTools.add),
        };
        for (const n of extTools.remove) delete full[n];
        // Cast mirrors the main turn (toolsForTurn → fullToolSet): the merged
        // map is structurally a ToolSet; MCP entries are AI-SDK tools too.
        const subTools = Object.fromEntries(
            Object.entries(full).filter(([n]) => effective.includes(n)),
        ) as unknown as ReturnType<typeof createTools>;
        // Subagent tool calls run the same PreToolUse/PostToolUse hooks,
        // tagged with agent_id so watchers can tell them apart.
        const { provider: subProvider, model: subModel } = parseModelId(ctx.modelId);
        const hooked = withToolHooks(subTools, {
            cwd: ctx.cwd,
            sessionId: ctx.sessionId,
            transcriptPath: ctx.transcriptPath,
            emitter: ctx.emitter,
            agentId: toolCallId,
            // Extension onCall/onResult middleware applies in subagents too
            // (isSubagent: true lets a transform scope itself). Empty → pass-through.
            callMiddleware: getExtensionHost().getToolCallMiddleware(),
            resultMiddleware: getExtensionHost().getToolResultMiddleware(),
            turnContext: {
                sessionId: ctx.sessionId,
                transcriptPath: ctx.transcriptPath,
                cwd: ctx.cwd,
                agent: name,
                modelId: ctx.modelId,
                provider: subProvider,
                model: subModel,
                tools: Object.keys(subTools),
                isSubagent: true,
            },
        });
        const maxSteps = getSetting("subagentMaxSteps") || 50;
        // Same composition as the parent's system prompt (workspace context +
        // agent prompt + skills) so a fork behaves like the main agent — only
        // the subagent run rules and the missing task tool differ.
        const system =
            buildSystemPrompt({
                cwd: ctx.cwd,
                workspaceContext: ctx.workspaceContext,
                basePrompt: getAgentPrompt(name),
                tools: Object.keys(subTools),
            }) +
            (ctx.skillsPrompt ?? "") +
            SUBAGENT_SYSTEM_SUFFIX;
        // Anthropic-shaped providers only cache up to explicit cache_control
        // breakpoints. runTurn sets them, but this loop didn't — so every
        // subagent step re-billed its whole accumulated context at full input
        // price (a single long run burned millions of uncached tokens). Anchor
        // the system prompt once and move a tail breakpoint every step, same
        // as runTurn.
        const anthropicCaching = effectiveSdkProvider(parseModelId(ctx.modelId).provider) === "anthropic";
        // AI SDK's native agent loop — same streamText core runTurn uses, with
        // the loop/stop handling owned by the SDK.
        const agent = new ToolLoopAgent({
            model: await getModel(ctx.modelId),
            instructions: anthropicCaching ? anthropicCachedSystem(system) : system,
            tools: hooked,
            stopWhen: stepCountIs(maxSteps),
            ...(anthropicCaching
                ? {
                      prepareStep: ({ messages }: { messages: ModelMessage[] }) => ({
                          messages: moveAnthropicCacheTail(messages),
                      }),
                  }
                : {}),
        });
        const result = await agent.stream({ prompt, abortSignal: ctx.abortSignal });

        let totalUsage: UsageBlock | undefined;
        // Running per-step sum — when the run is aborted mid-flight `finish`
        // never arrives, but the completed steps were already billed; persist
        // their sum so a resumed session seeds the real cost instead of 0.
        let stepUsageSum: UsageBlock | undefined;
        // One ordered activity log: text, reasoning, and tool parts appended in
        // stream order so the subagent's real flow (text → tool → text → …) is
        // preserved — structured, so renderers can style each kind on its own.
        // Consecutive deltas of the same kind merge into one part.
        const activity: SubagentActivityPart[] = [];
        const appendDelta = (type: "text" | "reasoning", text: string) => {
            const last = activity[activity.length - 1];
            if (last && last.type === type) last.text += text;
            else activity.push({ type, text });
        };
        let lastYieldAt = Date.now();
        for await (const part of result.fullStream) {
            if (ctx.abortSignal?.aborted) break;
            switch (part.type) {
                case "text-delta":
                    appendDelta("text", part.text);
                    ctx.emitter.emit("subagent-delta", { toolCallId, agent: name, text: part.text });
                    break;
                case "reasoning-delta":
                    appendDelta("reasoning", part.text);
                    break;
                case "tool-call": {
                    const toolName = (part as { toolName?: string }).toolName;
                    const input = (part as { input?: unknown }).input;
                    activity.push({ type: "tool", name: toolName ?? "tool", summary: subagentArgSummary(input) });
                    ctx.emitter.emit("subagent-tool", { toolCallId, agent: name, toolName, input });
                    break;
                }
                case "finish-step": {
                    // Per-step cost accrual into the parent's tracker — the
                    // footer ticks while the subagent works, and aborts keep
                    // the spend of completed steps.
                    const u = (part as { usage?: UsageBlock }).usage;
                    if (u) {
                        stepUsageSum = sumUsage(stepUsageSum, u);
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
            // Yield to the event loop between buffered parts so the parent TUI's
            // render timers fire — same starvation fix as the main loop in
            // index.ts (see yieldToEventLoop there for the full rationale).
            if (Date.now() - lastYieldAt >= 16) {
                await new Promise((resolve) => setImmediate(resolve));
                lastYieldAt = Date.now();
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

        // Report = the AI SDK's final response text (result.text — the
        // subagent's concluding message), not the concatenation of every
        // intermediate step's text. It can legitimately be empty (aborted
        // mid-run, tool-only finish); toModelOutput substitutes a default so
        // the parent model always receives something.
        let report = "";
        try {
            report = (await result.text).trim();
        } catch {
            // aborted/errored before a final response — report stays empty
        }

        // Persist for resume, same shape the events streamed: `activity` is
        // the full ordered run (what the box renders next time), `result` the
        // final report (what the model re-reads via toModelMessages).
        await ctx.session.append({
            type: "subagent",
            ts: Date.now(),
            agent: name,
            prompt,
            result: report || formatSubagentActivity(activity) || "(subagent produced no output)",
            activity: activity.length ? activity : undefined,
            usage: totalUsage ?? stepUsageSum,
            model: ctx.modelId,
        });

        // The history is the tool output (saved + rendered); toModelOutput
        // extracts the report for the model.
        return { history: activity, report };
    } catch (err) {
        const msg = `Subagent failed: ${err instanceof Error ? err.message : String(err)}`;
        return { history: [{ type: "text", text: msg }], report: msg };
    }
}
