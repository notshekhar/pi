import { streamText, stepCountIs } from "ai";
import type { ModelMessage } from "ai";
import type { TurnEmitter } from "./events";
import { getModel, parseModelId } from "../providers";
import { getCatalog } from "../catalog";
import { getSetting } from "../settings";
import { effectiveSdkProvider } from "../auth";
import { createTools } from "../tools";
import { createSqlTool } from "../tools/sql";
import { buildSystemPrompt } from "./system-prompt";
import { getAgentPrompt, getAgentTools, listAgents } from "./agents";
import { loadWorkspaceContext } from "./context";
import { loadProjectSkills } from "./skills";
import { extractImagesFromInput } from "./images";
import { CostTracker, sumUsage } from "./cost";
import { runCompact } from "./compact";
import { runRecap, turnDeservesRecap } from "./recap";
import { runHooks, type HookOutcome } from "./hooks";
import { isTrusted } from "./trust";
import { buildProviderOptions, type ThinkingLevel } from "./thinking";
import { estimateContextTokens, moveAnthropicCacheTail, toModelMessages, withAnthropicCaching } from "./model-messages";
import { withToolHooks } from "./tool-hooks";
import { createTaskTool } from "./subagent";
import { getMcpManager } from "../mcp";
import type { Session } from "../sessions";
import type { UsageBlock } from "../types";

export { CostTracker } from "./cost";
export { runCompact, CompactAbortedError } from "./compact";
export { runRecap, isRecapPayload, RECAP_KIND, type RecapPayload } from "./recap";
export {
    runBranchSummary,
    BranchSummaryAbortedError,
    collectEntriesForBranchSummary,
    BRANCH_SUMMARY_PREAMBLE,
} from "./branch-summary";
export { estimateContextTokens } from "./model-messages";
export { THINKING_LEVELS, THINKING_LEVEL_DESCRIPTIONS, buildProviderOptions, type ThinkingLevel } from "./thinking";
export { loadWorkspaceContext, watchWorkspaceContext } from "./context";
export { loadProjectSkills, type Skill } from "./skills";
export {
    runHooks,
    loadHooksConfig,
    hookBus,
    listHooksWithSources,
    addPiUserHook,
    removePiUserHook,
    HOOK_EVENTS,
    type HookEvent,
    type HooksConfig,
    type HookOutcome,
    type HookSourceEntry,
} from "./hooks";
export {
    DEFAULT_AGENT_NAME,
    DATA_ANALYST_AGENT_NAME,
    PLAN_BASE_PROMPT,
    ANALYST_BASE_PROMPT,
    listAgents,
    getAgentPrompt,
    getAgentTools,
    agentExists,
    isBuiltinAgent,
    isHiddenAgent,
    hasBuiltinOverride,
    hasDefaultOverride,
    saveAgent,
    deleteAgent,
    isValidAgentName,
    parseAgentFile,
    AGENT_TOOL_NAMES,
    type AgentInfo,
} from "./agents";
export { DEFAULT_BASE_PROMPT } from "./system-prompt";
export { subagentArgSummary, formatSubagentActivity, type SubagentOutput } from "./subagent";
export { asTurnEmitter, type TurnEmitter, type TurnEvents } from "./events";
export {
    hasProjectTrustInputs,
    getTrustDecision,
    isTrusted,
    setTrust,
    trustForSession,
    getTrustOptions,
    type TrustOption,
    type TrustDecision,
} from "./trust";

export interface RunTurnOptions {
    session: Session;
    modelId: string;
    userInput: string;
    cwd: string;
    abortSignal?: AbortSignal;
    tracker: CostTracker;
    emitter: TurnEmitter;
    maxSteps?: number;
    thinkingLevel?: ThinkingLevel;
    /** Named agent whose prompt replaces the built-in persona ("default" = built-in). */
    agent?: string;
    /** Post-turn data-recap generation. Defaults to the `recap` setting (off). */
    recap?: boolean;
    /** internal: recursion depth for Stop-hook continuations */
    hookDepth?: number;
}

// AI SDK prints advisory warnings (e.g. about system messages inside the
// messages array — our deliberate Anthropic prompt-caching pattern) straight
// to the console, which tears the TUI's differential rendering. Silence them.
(globalThis as Record<string, unknown>).AI_SDK_LOG_WARNINGS = false;

/**
 * Hand control back to the event loop's macrotask phases.
 *
 * `result.fullStream` parts arrive buffered, so each read resolves as a
 * microtask. Consuming them in a tight `for await` — each part firing
 * `emitter.emit(...)` → `tui.requestRender()` (which uses `process.nextTick`) —
 * keeps the microtask/nextTick queues perpetually non-empty. Those queues drain
 * *before* the timers phase, so the TUI's `setTimeout` render flush and the
 * spinner's `setInterval` never get to run: the screen freezes mid-turn even
 * though the agent keeps working, and only unsticks when real I/O (a keypress)
 * forces the loop past the timers phase. Awaiting a `setImmediate` makes the
 * loop complete a full iteration — running timers on the way to the check phase —
 * so renders flush. Cheap (~microseconds); we call it time-gated, not per part.
 */
function yieldToEventLoop(): Promise<void> {
    return new Promise((resolve) => setImmediate(resolve));
}

// How long the stream loop may run without yielding before it must let the
// render timers fire. ~1 frame at 60fps — bounds freeze to one frame.
const STREAM_YIELD_INTERVAL_MS = 16;

interface PersistedTurnMessage {
    role: "assistant" | "tool";
    content: unknown;
    usage?: UsageBlock;
}

/**
 * Turn one completed step's messages into session entries — assistant text +
 * tool calls, and the tool results — in canonical AI-SDK shape (so they replay
 * and feed straight back to the model). Mirrors pi-mono: one entry per message,
 * persisted as the step finishes (abort-safe — completed steps survive).
 *
 * Task (subagent) tool calls/results are filtered out: they persist separately
 * as `subagent` entries (with their activity log), and keeping them here too
 * would double them on resume and in the model context.
 *
 * Per-step usage rides the step's assistant message: resume seeding sums
 * assistant usages (= turn total) and reads the last for context size.
 */
function stepMessagesToEntries(
    messages: ReadonlyArray<{ role: string; content: unknown }>,
    stepUsage: UsageBlock | undefined,
): PersistedTurnMessage[] {
    // The ONLY thing held back is the `task` (subagent) call/result — and not
    // to drop data: the subagent is persisted MORE completely as its own
    // `subagent` entry (prompt + full internal activity log + report), so
    // letting it through here too would duplicate the box on resume. Every
    // other part — text, reasoning, tool calls, tool results — is persisted
    // exactly as it streamed.
    const taskCallIds = new Set<string>();
    const out: PersistedTurnMessage[] = [];
    for (const m of messages) {
        if (m.role === "assistant") {
            const parts = Array.isArray(m.content) ? m.content : [{ type: "text", text: String(m.content) }];
            const kept = parts.filter((p: { type?: string; toolName?: string; toolCallId?: string }) => {
                if (p?.type === "tool-call" && p.toolName === "task") {
                    if (p.toolCallId) taskCallIds.add(p.toolCallId);
                    return false;
                }
                return true;
            });
            if (kept.length > 0) out.push({ role: "assistant", content: kept, usage: stepUsage });
        } else if (m.role === "tool") {
            const parts = Array.isArray(m.content) ? m.content : [];
            const kept = parts.filter(
                (p: { toolCallId?: string }) => !(p?.toolCallId && taskCallIds.has(p.toolCallId)),
            );
            if (kept.length > 0) out.push({ role: "tool", content: kept });
        }
    }
    return out;
}

export async function runTurn(opts: RunTurnOptions): Promise<void> {
    const { session, modelId, userInput, cwd, abortSignal, tracker, emitter } = opts;
    // Step cap is only an upper safety bound — the loop ends naturally when the
    // model returns no tool call. 0 / unset means "run until the model decides".
    const configuredSteps = opts.maxSteps ?? getSetting("maxSteps") ?? 0;
    const maxSteps = configuredSteps > 0 ? configuredSteps : Number.MAX_SAFE_INTEGER;

    // Extract any image paths from the user input → ai-sdk image parts
    const { textWithoutPaths, images } = extractImagesFromInput(userInput, cwd);
    if (images.length > 0) {
        emitter.emit(
            "attached-images",
            images.map((i) => i.path),
        );
    }
    // UserPromptSubmit hooks: may block the turn or inject context for the
    // model. Skipped for Stop-hook continuations (hookDepth > 0) — those are
    // synthetic turns, and Claude Code doesn't fire UserPromptSubmit for them.
    const hookDepth = opts.hookDepth ?? 0;
    let promptHooks: HookOutcome = { block: false, messages: [], terminalSequences: [] };
    if (hookDepth === 0) {
        promptHooks = await runHooks(
            "UserPromptSubmit",
            undefined,
            { session_id: session.id, transcript_path: session.path, prompt: userInput },
            cwd,
        );
    }
    for (const m of promptHooks.messages) emitter.emit("hook-message", m);
    for (const s of promptHooks.terminalSequences) emitter.emit("hook-terminal-sequence", s);
    if (promptHooks.block) {
        emitter.emit("error", `prompt blocked by hook: ${promptHooks.reason}`);
        emitter.emit("finish", { usage: undefined });
        return;
    }

    // Persist user message verbatim (paths intact for reference in transcripts)
    await session.append({ type: "message", ts: Date.now(), role: "user", content: userInput });

    const { provider, model: modelShortId } = parseModelId(modelId);

    // auto-compact check
    const catalog = await getCatalog();
    const modelInfo = catalog[modelId];
    const threshold = getSetting("autoCompactThreshold") ?? 0.8;
    if (modelInfo) {
        const tokens = estimateContextTokens(session);
        if (tokens > modelInfo.contextWindow * threshold) {
            emitter.emit("compact-start", { reason: "auto" });
            // PreCompact is informational for watchers — block is ignored.
            await runHooks(
                "PreCompact",
                "auto",
                { session_id: session.id, transcript_path: session.path, trigger: "auto" },
                cwd,
            );
            try {
                const result = await runCompact({ session, modelId, abortSignal });
                emitter.emit("compact-end", result);
            } catch (err) {
                if (abortSignal?.aborted) {
                    emitter.emit("compact-end", {
                        summary: "",
                        cutAt: 0,
                        tokensBefore: 0,
                        tokensAfter: 0,
                        aborted: true,
                    });
                    return;
                }
                throw err;
            }
        }
    }

    const workspaceContext =
        getSetting("workspaceContext") !== false ? loadWorkspaceContext(cwd) : { text: "", files: [] };
    // Project skills inject instructions into the prompt — gate on trust too.
    const skillsEnabled = getSetting("skills") !== false && isTrusted(cwd);
    const skills = skillsEnabled ? await loadProjectSkills(cwd) : { skills: [], diagnostics: [], promptBlock: "" };

    const agentPrompt = opts.agent ? getAgentPrompt(opts.agent) : undefined;
    // Per-agent tool restriction (e.g. plan = read-only). undefined = all tools.
    const allowedTools = opts.agent ? getAgentTools(opts.agent) : undefined;
    const fullToolSet = createTools({ cwd, abortSignal });
    const toolSet = (
        allowedTools?.length
            ? Object.fromEntries(Object.entries(fullToolSet).filter(([name]) => allowedTools.includes(name)))
            : fullToolSet
    ) as typeof fullToolSet;
    // Subagents: master `subagents` setting gates the task tool entirely (off
    // → no agent gets it). Otherwise unrestricted agents always get it;
    // restricted agents get it only when their tool list opts in ("task").
    // A subagent is a fork of this turn's agent by default; either way its
    // tools are capped to this turn's file tools, so delegation never widens
    // access. Subagents never get task themselves (no nesting).
    const subagentsEnabled = getSetting("subagents") !== false;
    const toolsForTurn: Record<string, unknown> = { ...toolSet };
    // `sql` lives outside the file toolset (createTools) and AGENT_TOOL_NAMES,
    // so it can never reach the default agent or any custom agent. It joins the
    // turn only for an agent whose fixed tool set opts in (the data-analyst
    // built-in).
    if (allowedTools?.includes("sql")) {
        toolsForTurn.sql = createSqlTool({ abortSignal });
    }
    if (subagentsEnabled && (!allowedTools?.length || allowedTools.includes("task"))) {
        toolsForTurn.task = createTaskTool({
            modelId,
            cwd,
            tracker,
            emitter,
            abortSignal,
            session,
            sessionId: session.id,
            transcriptPath: session.path,
            turnAgent: opts.agent,
            parentTools: Object.keys(toolSet),
            workspaceContext: workspaceContext.text,
            skillsPrompt: skills.promptBlock,
        });
    }
    // MCP tools (already namespaced mcp__server__tool) join the turn for
    // unrestricted agents only — a restricted agent (e.g. plan) keeps its
    // explicit allowlist. Gated by the master `mcp` setting + project trust,
    // mirroring skills/subagents. The manager was connected once at startup;
    // here we just read its aggregated tool set.
    // MCP is opt-in (default off): only enabled when the setting is explicitly
    // true. Temporarily disabled by default while the MCP path is stabilized.
    const mcpEnabled = getSetting("mcp") === true && isTrusted(cwd);
    if (mcpEnabled && !allowedTools?.length) {
        Object.assign(toolsForTurn, getMcpManager().getTools());
    }
    // System prompt is built AFTER the task tool decision so the model's tool
    // list matches reality, plus explicit delegation guidance when present.
    const subagentNote =
        "task" in toolsForTurn
            ? `\n\nSubagents (task tool): delegate work that would flood your context — broad codebase exploration, analyzing many files, research across directories, or an independent multi-file change. Each subagent runs in its own context window and returns only a final report.
Use task when: the job is self-contained, needs many file reads/searches, or you want parallel investigation of separate areas.
Do NOT use task when: the job is one or two tool calls, needs back-and-forth with the user, or depends on context only you have (unless you include it in the prompt).
Write complete prompts: the subagent knows nothing about this conversation — include paths, goals, constraints, and the exact output you expect. Call task alone in its step, never alongside other tool calls. By default the subagent is a fork of you (same prompt and tools, minus task); pass agent to run a named agent instead. Available agents: ${listAgents()
                  .map((a) => a.name)
                  .join(", ")}.`
            : "";
    const system =
        buildSystemPrompt({
            cwd,
            workspaceContext: workspaceContext.text,
            basePrompt: agentPrompt,
            tools: Object.keys(toolsForTurn),
        }) +
        subagentNote +
        (skills.promptBlock ?? "");
    const tools = withToolHooks(toolsForTurn as typeof fullToolSet, {
        cwd,
        sessionId: session.id,
        transcriptPath: session.path,
        emitter,
    });
    const model = await getModel(modelId);

    // If we extracted image paths, override the last user message with a multipart
    // content array (text + image parts) so vision models actually see the image.
    const messages = toModelMessages(session);
    if (images.length > 0) {
        const lastUserIdx = (() => {
            for (let i = messages.length - 1; i >= 0; i--) if (messages[i].role === "user") return i;
            return -1;
        })();
        if (lastUserIdx >= 0) {
            const parts: Array<{ type: "text"; text: string } | { type: "image"; image: Buffer; mediaType: string }> =
                [];
            if (textWithoutPaths) parts.push({ type: "text", text: textWithoutPaths });
            for (const img of images) parts.push({ type: "image", image: img.data, mediaType: img.mediaType });
            messages[lastUserIdx] = { role: "user", content: parts as never };
        }
    }

    // Hook-injected context rides only the model-bound copy, not the transcript.
    // Must target the *last* user message even when image extraction already
    // turned it into a parts array — falling through to an earlier message would
    // rewrite history and bust the prompt-cache prefix.
    if (promptHooks.additionalContext) {
        const ctxBlock = `<hook-context>\n${promptHooks.additionalContext}\n</hook-context>`;
        for (let i = messages.length - 1; i >= 0; i--) {
            const m = messages[i];
            if (m.role !== "user") continue;
            if (typeof m.content === "string") {
                messages[i] = { role: "user", content: `${m.content}\n\n${ctxBlock}` };
            } else if (Array.isArray(m.content)) {
                messages[i] = { role: "user", content: [...m.content, { type: "text", text: ctxBlock }] as never };
            }
            break;
        }
    }

    const thinkingLevel: ThinkingLevel = opts.thinkingLevel ?? getSetting("thinkingLevel") ?? "off";
    // Custom providers (gateways like bifrost) proxy a real vendor API — map
    // thinking/caching by the configured sdk so e.g. an anthropic-compatible
    // gateway gets adaptive thinking + prompt-cache breakpoints.
    const effectiveProvider = effectiveSdkProvider(provider);
    let providerOptions =
        modelInfo?.reasoning === false
            ? undefined
            : buildProviderOptions(effectiveProvider, thinkingLevel, modelShortId);

    // Ollama defaults num_ctx to ~4096 regardless of the model's real context,
    // which truncates long agent loops and makes the model stop early. Pin it to
    // the model's actual context window so history isn't silently dropped.
    if (provider === "ollama") {
        const numCtx = modelInfo?.contextWindow ?? 8192;
        const existing = (providerOptions?.ollama as Record<string, unknown> | undefined) ?? {};
        const existingOpts = (existing.options as Record<string, unknown> | undefined) ?? {};
        providerOptions = {
            ...providerOptions,
            ollama: { ...existing, options: { ...existingOpts, num_ctx: numCtx } },
        };
    }

    const anthropicCaching = effectiveProvider === "anthropic";
    // Incremental persistence (pi-mono parity): each completed step's messages
    // are written as they finish, so tool calls/results survive turn boundaries
    // and aborts. Tracks whether anything was persisted for the abort fallback.
    let persistedAnyMessage = false;
    const persistStep = async (step: {
        response: { messages: ReadonlyArray<{ role: string; content: unknown }> };
        usage?: UsageBlock;
    }): Promise<void> => {
        for (const entry of stepMessagesToEntries(step.response.messages, step.usage)) {
            await session.append({
                type: "message",
                ts: Date.now(),
                role: entry.role,
                content: entry.content,
                ...(entry.usage ? { usage: entry.usage } : {}),
            });
            persistedAnyMessage = true;
        }
    };
    const result = streamText({
        model,
        ...(anthropicCaching
            ? // system inside messages is our deliberate Anthropic prompt-caching
              // pattern — allowSystemInMessages opts out of the AI SDK warning.
              {
                  messages: withAnthropicCaching(system, messages),
                  allowSystemInMessages: true,
                  // Per-step moving breakpoint: without it, every step after the
                  // first re-bills all accumulated tool results at full input
                  // price (quadratic in steps on long turns).
                  prepareStep: ({ messages: stepMessages }: { messages: ModelMessage[] }) => ({
                      messages: moveAnthropicCacheTail(stepMessages),
                  }),
              }
            : { system, messages }),
        tools,
        stopWhen: stepCountIs(maxSteps),
        abortSignal,
        // Persist each step's messages as it finishes (mirrors pi-mono's
        // per-message persistence). Errors here must not break the turn.
        onStepFinish: (step) => persistStep(step as never).catch(() => {}),
        // smoothStream removed: it re-buffers tokens and releases them on its
        // own 20ms timers, coupling stream delivery to the timer phase — the
        // same phase that starves during a turn, which can deadlock delivery
        // and freeze the TUI. pi-mono doesn't use it; we stream parts raw.
        ...(providerOptions ? { providerOptions: providerOptions as never } : {}),
    });

    let assistantText = "";
    const toolsUsed: string[] = [];
    let lastUsage: UsageBlock | undefined;
    let lastStepUsage: UsageBlock | undefined;
    // Per-step running sum — an aborted turn never sees `finish`, but its
    // completed steps were billed; persisting the sum keeps resumed-session
    // cost seeding honest (same pattern as the subagent loop).
    let stepUsageSum: UsageBlock | undefined;

    let lastYieldAt = Date.now();
    for await (const part of result.fullStream) {
        if (abortSignal?.aborted) break;
        switch (part.type) {
            case "text-delta":
                assistantText += part.text;
                emitter.emit("text-delta", part.text);
                break;
            case "reasoning-delta":
                emitter.emit("reasoning-delta", (part as { text: string }).text);
                break;
            case "reasoning-start":
                emitter.emit("reasoning-start");
                break;
            case "reasoning-end":
                emitter.emit("reasoning-end");
                break;
            case "tool-call":
                if (part.toolName) toolsUsed.push(part.toolName);
                emitter.emit("tool-call", part);
                break;
            case "tool-result":
                emitter.emit("tool-result", part);
                break;
            case "tool-error": {
                // A tool's execute threw (MCP transport failure, timeout, bad
                // args…). The AI SDK still feeds the error back to the model as
                // the tool result, so the loop continues and the model can try
                // another approach — we just surface it in the UI (red) instead
                // of leaving the tool box spinning forever.
                const e = part as { toolCallId?: string; toolName?: string; error?: unknown };
                emitter.emit("tool-error", { toolCallId: e.toolCallId, toolName: e.toolName, error: e.error });
                break;
            }
            case "finish-step": {
                // Cost accrues per step (one API round-trip each), not at turn
                // end — the footer updates live, and an aborted turn keeps the
                // cost of the steps that already ran. Step usages sum to the
                // turn total, so nothing is added again on finish.
                const u = (part as { usage?: UsageBlock }).usage;
                if (u) {
                    lastStepUsage = u;
                    stepUsageSum = sumUsage(stepUsageSum, u);
                    const breakdown = tracker.add(modelId, u, cwd);
                    emitter.emit("step-usage", { usage: u, breakdown });
                }
                break;
            }
            case "finish": {
                const u = (part as { totalUsage?: UsageBlock }).totalUsage;
                lastUsage = u;
                emitter.emit("finish", { usage: u, lastStepUsage });
                break;
            }
            case "error": {
                const msg = String((part as { error?: unknown }).error ?? "");
                if (/^(reasoning|text) part .* not found$/.test(msg)) break;
                emitter.emit("error", part.error);
                break;
            }
        }
        // Let the TUI's render timers fire between bursts of buffered parts.
        if (Date.now() - lastYieldAt >= STREAM_YIELD_INTERVAL_MS) {
            await yieldToEventLoop();
            lastYieldAt = Date.now();
        }
    }

    // Steps persist incrementally via onStepFinish (above). Fallback: if a turn
    // was aborted before any step finished but produced partial text or usage,
    // keep it so resume isn't empty and cost seeding stays honest. Anthropic
    // rejects empty text blocks, so only persist when there's text or usage.
    if (!persistedAnyMessage && (assistantText.trim() !== "" || lastUsage || stepUsageSum)) {
        await session.append({
            type: "message",
            ts: Date.now(),
            role: "assistant",
            content: assistantText,
            usage: lastUsage ?? stepUsageSum,
        });
    }

    // Post-turn recap: only for turns that wrote/edited files, detached so the
    // prompt frees immediately — the data-recap event lands in the UI whenever
    // generation finishes. Skipped for hook continuations (the final
    // continuation recaps the whole turn).
    const recapEnabled = opts.recap ?? getSetting("recap") === true;
    const recapWorthy = turnDeservesRecap(toolsUsed) && assistantText.trim() !== "";
    if (recapEnabled && recapWorthy && hookDepth === 0 && !abortSignal?.aborted) {
        void runRecap({ session, modelId, userInput, assistantText, toolsUsed, tracker, cwd, abortSignal })
            .then((text) => text && emitter.emit("data-recap", { text }))
            .catch(() => {}); // best-effort — a failed recap never fails the turn
    }

    // Stop hooks: a block sends the reason back as a follow-up turn so the
    // agent keeps working (Claude Code parity, e.g. "tests must pass"). Depth
    // capped to avoid infinite hook loops.
    if (!abortSignal?.aborted) {
        // stop_hook_active mirrors Claude Code: true when this turn is already a
        // stop-hook continuation, so ported hooks can use it as their loop guard.
        const stopHooks = await runHooks(
            "Stop",
            undefined,
            { session_id: session.id, transcript_path: session.path, stop_hook_active: hookDepth > 0 },
            cwd,
        );
        for (const m of stopHooks.messages) emitter.emit("hook-message", m);
        for (const s of stopHooks.terminalSequences) emitter.emit("hook-terminal-sequence", s);
        if (stopHooks.block && hookDepth < 3) {
            emitter.emit("hook-message", `stop hook requested continuation: ${stopHooks.reason}`);
            await runTurn({ ...opts, userInput: `[stop hook] ${stopHooks.reason}`, hookDepth: hookDepth + 1 });
        }
    }
}
