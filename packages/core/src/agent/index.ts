import { streamText, isStepCount } from "ai";
import type { ModelMessage } from "ai";
import type { TurnEmitter } from "./events";
import { getModel, parseModelId } from "../providers";
import { getCatalog } from "../catalog";
import { getSetting } from "../settings";
import { effectiveSdkProvider } from "../auth";
import { createTools } from "../tools";
import { buildSystemPrompt } from "./system-prompt";
import { getAgentPrompt, getAgentTools, isReadOnlyBashAgent, listAgents } from "./agents";
import { loadWorkspaceContext } from "./context";
import { loadProjectSkills } from "./skills";
import { extractImagesFromInput } from "./images";
import { CostTracker, sumUsage } from "./cost";
import { runCompact } from "./compact";
import { runRecap, turnDeservesRecap } from "./recap";
import { runHooks, type HookOutcome } from "./hooks";
import { isTrusted } from "./trust";
import { buildReasoningParams, type ThinkingLevel } from "./thinking";
import { estimateContextTokens, moveAnthropicCacheTail, toModelMessages, withAnthropicCaching } from "./model-messages";
import { withToolHooks } from "./tool-hooks";
import { createTaskTool } from "./subagent";
import { isAbortError } from "./abort";
import { debugLog } from "../debug";
import { getMcpManager, isMcpEnabled } from "../mcp";
import { getExtensionHost } from "../extensions";
import {
    applyAssembleTools,
    applyProviderOptions,
    applySystemPrompt,
    runAfterTurn,
    runBeforeTurn,
} from "./turn-middleware";
import type { Session } from "../sessions";
import type { UsageBlock } from "../types";

export { CostTracker } from "./cost";
export { buildSteakGrid, type SteakGrid, type SteakOptions } from "./steak";
export { runCompact, CompactAbortedError } from "./compact";
export { runRecap, isRecapPayload, RECAP_KIND, type RecapPayload } from "./recap";
export {
    runBranchSummary,
    BranchSummaryAbortedError,
    collectEntriesForBranchSummary,
    BRANCH_SUMMARY_PREAMBLE,
} from "./branch-summary";
export { estimateContextTokens } from "./model-messages";
export {
    THINKING_LEVELS,
    THINKING_LEVEL_DESCRIPTIONS,
    buildProviderOptions,
    reasoningEffort,
    type ThinkingLevel,
} from "./thinking";
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
export { extractImagesFromInput, type ExtractedImages } from "./images";
export { asTurnEmitter, TURN_EVENT_NAMES, type TurnEmitter, type TurnEvents } from "./events";
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
 * `result.stream` parts arrive buffered, so each read resolves as a
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
 * and feed straight back to the model). mirrors the reference: one entry per message,
 * persisted as the step finishes (abort-safe — completed steps survive).
 *
 * Task (subagent) tool calls/results are filtered out: they persist separately
 * as `subagent` entries (with their activity log), and keeping them here too
 * would double them on resume and in the model context.
 *
 * Per-step usage rides the step's assistant message: resume seeding sums
 * assistant usages (= turn total) and reads the last for context size.
 */
export function stepMessagesToEntries(
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
    // Resume-time cost seeding sums every usage-bearing assistant entry, so a
    // step's usage must ride exactly ONE of its messages — stamping each would
    // double-bill the step if the SDK ever emits two assistant messages per step.
    let usageToStamp = stepUsage;
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
            if (kept.length > 0) {
                out.push({ role: "assistant", content: kept, usage: usageToStamp });
                usageToStamp = undefined;
            }
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

/** Most recent provider-reported (non-estimated) usage in the transcript — the
 * input/cache profile to anchor an interrupted-step estimate to when the
 * current turn has no finished step yet (an interrupt on the very first step). */
function lastUsageInSession(session: Session): UsageBlock | undefined {
    const all = session.entries();
    for (let i = all.length - 1; i >= 0; i--) {
        const e = all[i];
        if (e.type === "message" && e.role === "assistant" && e.usage && !e.usage.estimated) return e.usage;
        if (e.type === "subagent" && e.usage && !e.usage.estimated) return e.usage;
    }
    return undefined;
}

/**
 * Estimate usage for the in-flight request of an interrupted turn. The AI SDK
 * records a step only on its `finish-step` chunk and reports no usage on abort
 * (vercel/ai#7805), so the cut-off round-trip has no provider numbers — yet the
 * provider billed it (input + the output streamed before the cut).
 *
 * Output is the only estimated part: the partial text + reasoning at chars/4
 * (loop's house heuristic; small, since an interrupt lands early). Input — the
 * expensive, cache-split-sensitive part — is NOT guessed: it's taken from the
 * adjacent real step (`basis`), since the interrupted request resent
 * essentially the same context, so its input count and cache-read/write split
 * match within a few hundred tokens. Marked `estimated` → session total only,
 * rendered with a leading `~`.
 */
function estimateInterruptedUsage(
    tailText: string,
    tailReasoning: string,
    basis: UsageBlock | undefined,
    session: Session,
): UsageBlock | undefined {
    const textTokens = Math.ceil(tailText.length / 4);
    const reasoningTokens = Math.ceil(tailReasoning.length / 4);
    const outputTokens = textTokens + reasoningTokens;
    // No output streamed and no real basis to anchor to → fabricate nothing.
    if (outputTokens === 0 && !basis) return undefined;
    const inputTokens = basis?.inputTokens ?? estimateContextTokens(session);
    const inputTokenDetails = basis?.inputTokenDetails ? { ...basis.inputTokenDetails } : undefined;
    const cachedInputTokens = basis?.inputTokenDetails?.cacheReadTokens ?? basis?.cachedInputTokens;
    return {
        inputTokens,
        outputTokens,
        totalTokens: inputTokens + outputTokens,
        cachedInputTokens,
        inputTokenDetails,
        outputTokenDetails: { textTokens, reasoningTokens },
        estimated: true,
    };
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

    // Extension turn middleware (onBeforeTurn) may block the turn. No-op when no
    // extensions are loaded.
    if (
        !(await runBeforeTurn({
            input: userInput,
            cwd,
            sessionId: session.id,
            agent: opts.agent ?? "default",
            modelId,
        }))
    ) {
        emitter.emit("error", "turn blocked by extension");
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
    // An agent allowed bash but NOT write/edit (e.g. plan) gets bash forced into
    // a fail-closed read-only sandbox, so the kernel guarantees no mutation.
    const readOnlyFs = isReadOnlyBashAgent(allowedTools);
    const fullToolSet = createTools({ cwd, abortSignal, readOnlyFs });
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
    let toolsForTurn: Record<string, unknown> = { ...toolSet };

    // MCP tools (already namespaced mcp__server__tool) join the turn for
    // unrestricted agents only — a restricted agent (e.g. plan) keeps its
    // explicit allowlist. Gated by the master `mcp` setting (default on) +
    // project trust, mirroring skills/subagents. The manager was connected once
    // at startup; here we just read its aggregated tool set.
    //
    // Added BEFORE the task tool so MCP names land in `parentTools` — a subagent
    // fork then inherits the same MCP tools, while the resolver's cap still lets
    // a named subagent only narrow, never widen.
    const mcpEnabled = isMcpEnabled() && isTrusted(cwd);
    if (mcpEnabled && !allowedTools?.length) {
        Object.assign(toolsForTurn, getMcpManager().getTools());
    }

    // Extension tools — same gating as MCP: unrestricted agents only (a
    // restricted agent like plan keeps its explicit allowlist), trust-gated.
    // With no extensions loaded both maps are empty, so this is a no-op and the
    // turn's toolset is byte-for-byte the builtin set. `default` (unrestricted)
    // therefore gets every extension tool automatically; removals drop builtins
    // an extension explicitly overrode.
    if (!allowedTools?.length && isTrusted(cwd)) {
        const ext = getExtensionHost().getTools();
        for (const [name, tool] of ext.add) toolsForTurn[name] = tool as never;
        for (const name of ext.remove) delete toolsForTurn[name];
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
            // Everything the parent can call (incl. MCP); task isn't added yet,
            // so it can't recurse into itself.
            parentTools: Object.keys(toolsForTurn),
            workspaceContext: workspaceContext.text,
            skillsPrompt: skills.promptBlock,
        });
    }
    // TurnContext carries which agent/model/tools are running. It's handed to
    // every turn-middleware seam (so an extension can scope by ctx.agent — e.g.
    // update one specific agent's system prompt) and to tool call/result
    // middleware. Built here, then refreshed after onAssembleTools so its tool
    // list reflects any additions/removals.
    let turnContext = {
        sessionId: session.id,
        transcriptPath: session.path,
        cwd,
        agent: opts.agent ?? "default",
        modelId,
        provider,
        model: modelShortId,
        tools: Object.keys(toolsForTurn),
        isSubagent: false,
    };
    // Extension turn middleware may add/remove/wrap tools before the prompt is
    // built (so the tool list the model sees reflects any changes). No-op when no
    // extensions are loaded.
    toolsForTurn = await applyAssembleTools(toolsForTurn, turnContext);
    turnContext = { ...turnContext, tools: Object.keys(toolsForTurn) };

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
    let system =
        buildSystemPrompt({
            cwd,
            workspaceContext: workspaceContext.text,
            basePrompt: agentPrompt,
            tools: Object.keys(toolsForTurn),
        }) +
        subagentNote +
        (skills.promptBlock ?? "");
    // Extension turn middleware may transform the system prompt, scoped by
    // ctx.agent (update any specific agent's prompt). No-op when none.
    system = await applySystemPrompt(system, turnContext);
    const tools = withToolHooks(toolsForTurn as typeof fullToolSet, {
        cwd,
        sessionId: session.id,
        transcriptPath: session.path,
        emitter,
        turnContext,
        // Empty when no extensions are loaded → withToolHooks is a pass-through.
        callMiddleware: getExtensionHost().getToolCallMiddleware(),
        resultMiddleware: getExtensionHost().getToolResultMiddleware(),
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
            // AI SDK v6 user-content attachment shape: a `file` part with the
            // bytes as `data` and an IANA `mediaType`. (The older `image` part is
            // deprecated in favor of this for all attachment kinds.)
            const parts: Array<{ type: "text"; text: string } | { type: "file"; data: Buffer; mediaType: string }> = [];
            if (textWithoutPaths) parts.push({ type: "text", text: textWithoutPaths });
            for (const img of images) parts.push({ type: "file", data: img.data, mediaType: img.mediaType });
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
    // First-party providers translate the level via the portable `reasoning`
    // param; community providers (and edge cases) fall back to providerOptions.
    // Anthropic adaptive-thinking models (Sonnet 5, Opus 4.6+, …) are driven
    // through providerOptions so the request shape doesn't depend on the SDK's
    // model table (which 400s on ids it doesn't recognize).
    const { reasoning, providerOptions: reasoningOptions } = buildReasoningParams(
        effectiveProvider,
        modelShortId,
        thinkingLevel,
        modelInfo?.reasoning !== false,
    );
    let providerOptions = reasoningOptions;

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

    // Extension turn middleware may tweak provider options (thinking/caching).
    // No-op when no extensions are loaded.
    providerOptions = applyProviderOptions(providerOptions, turnContext) as typeof providerOptions;

    const anthropicCaching = effectiveProvider === "anthropic";
    // Incremental persistence: each completed step's messages are written as it
    // finishes, so tool calls/results AND the final answer survive turn
    // boundaries and aborts. `step.response.messages` holds only THIS step's new
    // messages — the AI SDK resets its per-step content buffer on every
    // `start-step`, so it is NOT cumulative. Persist all of them; there is no
    // overlap across steps to dedupe. (Slicing against a running count, on the
    // mistaken assumption it was cumulative, dropped every step after the first
    // — losing the final answer and its usage on reopen.)
    let persistedAnyMessage = false;
    // Serialize appends so each step's messages land in order even if onStepEnd
    // callbacks overlap.
    let persistChain: Promise<void> = Promise.resolve();
    const persistStep = (step: {
        response: { messages: ReadonlyArray<{ role: string; content: unknown }> };
        usage?: UsageBlock;
    }): Promise<void> => {
        const entries = stepMessagesToEntries(step.response.messages, step.usage);
        if (entries.length > 0) persistedAnyMessage = true;
        persistChain = persistChain
            .then(() =>
                // One batch = one file lock/write for the whole step's messages.
                session.appendAll(
                    entries.map((entry) => ({
                        type: "message" as const,
                        ts: Date.now(),
                        role: entry.role,
                        content: entry.content,
                        // Stamp the model on usage-bearing (assistant) entries so
                        // cost seeding prices them correctly after a model switch.
                        ...(entry.usage ? { usage: entry.usage, model: modelId } : {}),
                    })),
                ),
            )
            // Persistence must never break a turn — but a failed transcript
            // write is data loss, so leave a breadcrumb (LOOP_DEBUG=1).
            .catch((err) => debugLog("persist", `step persistence failed for session ${session.id}:`, err));
        return persistChain;
    };
    const result = streamText({
        model,
        // Cap output at the model's real max. The AI SDK's per-model default is
        // 4096 for any Anthropic id its baked-in table doesn't recognize (a newer
        // or gateway-named model) — which would truncate replies.
        ...(effectiveProvider === "anthropic" && modelInfo?.maxOutput ? { maxOutputTokens: modelInfo.maxOutput } : {}),
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
            : { instructions: system, messages }),
        tools,
        stopWhen: isStepCount(maxSteps),
        abortSignal,
        // v7 portable reasoning effort for first-party providers (off → "none").
        // Undefined for community providers / non-reasoning models, which use
        // providerOptions (or nothing) instead.
        ...(reasoning ? { reasoning } : {}),
        // Persist each step's messages as it finishes (mirrors the reference's
        // per-message persistence). Errors here must not break the turn.
        onStepEnd: (step) => {
            void persistStep(step as never);
        },
        // The SDK's default onError does console.error(error) with the whole
        // APICallError (request body, tool defs — a wall of noise). We already
        // surface stream errors cleanly via the stream "error" part below,
        // so swallow this duplicate to keep the console clean.
        onError: () => {},
        // smoothStream removed: it re-buffers tokens and releases them on its
        // own 20ms timers, coupling stream delivery to the timer phase — the
        // same phase that starves during a turn, which can deadlock delivery
        // and freeze the TUI. the reference doesn't use it; we stream parts raw.
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
    // Text/reasoning streamed since the last FINISHED step (reset on each
    // finish-step). On abort this is the un-persisted tail of the interrupted
    // step — the only part not already saved/billed — used to recover its
    // partial output and estimate its cost.
    let textSinceStep = "";
    let reasoningSinceStep = "";

    let lastYieldAt = Date.now();
    // The interrupt can land between parts (caught by the `break` below) or while
    // awaiting the next part. A real fetch-backed provider rejects its body
    // stream on abort, which can throw straight out of `for await`; without this
    // guard that throw would skip the persistence below, losing the partial
    // reply. Swallow only aborts; any other error still propagates.
    try {
        for await (const part of result.stream) {
            if (abortSignal?.aborted) break;
            switch (part.type) {
                case "text-delta":
                    assistantText += part.text;
                    textSinceStep += part.text;
                    emitter.emit("text-delta", part.text);
                    break;
                case "reasoning-delta": {
                    const rt = (part as { text: string }).text;
                    reasoningSinceStep += rt;
                    emitter.emit("reasoning-delta", rt);
                    break;
                }
                case "reasoning-start":
                    emitter.emit("reasoning-start");
                    break;
                case "reasoning-end":
                    emitter.emit("reasoning-end");
                    break;
                case "tool-input-start":
                    // Surface the pending tool box as soon as the call begins,
                    // before its (possibly large) input has finished streaming.
                    emitter.emit("tool-input-start", {
                        toolName: (part as { toolName?: string }).toolName,
                        toolCallId: (part as { toolCallId?: string }).toolCallId,
                    });
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
                    // This step's text/reasoning is now persisted via onStepEnd
                    // and billed via the usage above — reset the tail so it
                    // tracks only what streams in the NEXT (possibly aborted)
                    // step. Keeps multi-step aborts from re-persisting or
                    // double-counting finished text.
                    textSinceStep = "";
                    reasoningSinceStep = "";
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
    } catch (err) {
        if (!isAbortError(err) && !abortSignal?.aborted) throw err;
    }

    // Flush any in-flight step persistence before deciding on the fallback /
    // returning, so the session file is complete and ordered.
    await persistChain;

    const aborted = abortSignal?.aborted === true;

    // The un-persisted tail: reasoning + text streamed since the last FINISHED
    // step. Finished steps already persisted (onStepEnd) and billed (finish-step
    // usage) their own content, so this is exactly the interrupted step's
    // partial output — nothing finished is lost or duplicated. Reasoning is kept
    // (an interrupt during the thinking phase still records what it produced)
    // and the answer text after it; both also count toward the cost estimate.
    const tailParts: Array<{ type: "reasoning"; text: string } | { type: "text"; text: string }> = [];
    if (reasoningSinceStep.trim() !== "") tailParts.push({ type: "reasoning", text: reasoningSinceStep });
    if (textSinceStep.trim() !== "") tailParts.push({ type: "text", text: textSinceStep });
    const streamedTail = tailParts.length > 0;

    if (aborted) {
        // Estimate the interrupted in-flight request's usage — the SDK reports
        // none on abort (vercel/ai#7805). Only when output actually streamed in
        // an unfinished step: a clean step-boundary interrupt left every
        // finished step already billed via finish-step, so there's nothing to
        // add. Input is anchored to the adjacent real step (this turn's last, or
        // the previous turn's); only the small output tail is approximated.
        // Session total only (never the persistent cost store), shown with `~`.
        let est: UsageBlock | undefined;
        if (streamedTail) {
            est = estimateInterruptedUsage(
                textSinceStep,
                reasoningSinceStep,
                lastStepUsage ?? lastUsageInSession(session),
                session,
            );
            if (est) {
                tracker.addEstimated(modelId, est, cwd);
                emitter.emit("step-usage", { usage: est, breakdown: tracker.sessionBreakdown() });
            }
        }
        // Persist the interrupted turn so the transcript AND the next turn's
        // context both reflect it. `interrupted: true` makes toModelMessages
        // append a note (and never silently drop an empty aborted turn). Only
        // text/usage here — never tool-call parts (those persist on finished
        // steps only), so there's no orphaned tool_use to break the next turn.
        if (streamedTail || !persistedAnyMessage) {
            await session.append({
                type: "message",
                ts: Date.now(),
                role: "assistant",
                content: tailParts.length > 0 ? tailParts : "",
                interrupted: true,
                ...(est ? { usage: est, model: modelId } : {}),
            });
        }
    } else if (!persistedAnyMessage && (tailParts.length > 0 || lastUsage || stepUsageSum)) {
        // Non-abort edge: the stream ended without any step persisting (e.g. a
        // provider error after partial output). Keep what streamed, with the
        // real usage we did capture.
        await session.append({
            type: "message",
            ts: Date.now(),
            role: "assistant",
            content: tailParts.length > 0 ? tailParts : "",
            usage: lastUsage ?? stepUsageSum,
            model: modelId,
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

    // Extension turn middleware (onAfterTurn) — best-effort, never fails a turn.
    // No-op when no extensions are loaded.
    await runAfterTurn(turnContext);
}
