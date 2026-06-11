import { streamText, stepCountIs, smoothStream, tool, ToolLoopAgent } from "ai";
import type { ModelMessage } from "ai";
import { z } from "zod";
import { EventEmitter } from "node:events";
import { getModel, parseModelId } from "../providers";
import { getCatalog } from "../catalog";
import { settingsStore } from "../auth/storage";
import { getCustomProvider, isCustomProvider, parseCustomProviderId } from "../auth";
import { createTools } from "../tools";
import { buildSystemPrompt } from "./system-prompt";
import { getAgentPrompt, getAgentTools, agentExists, listAgents, DEFAULT_AGENT_NAME } from "./agents";
import { loadWorkspaceContext } from "./context";
import { loadProjectSkills } from "./skills";
import { extractImagesFromInput } from "./images";
import { CostTracker } from "./cost";
import { compactedContextMessages, runCompact } from "./compact";
import { runHooks, type HookOutcome } from "./hooks";
import { isTrusted } from "./trust";
import { buildProviderOptions, type ThinkingLevel } from "./thinking";
import type { Session } from "../sessions";
import type { UsageBlock } from "../types";

export { CostTracker } from "./cost";
export { runCompact, CompactAbortedError } from "./compact";
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
    PLAN_BASE_PROMPT,
    listAgents,
    getAgentPrompt,
    getAgentTools,
    agentExists,
    isBuiltinAgent,
    hasBuiltinOverride,
    hasDefaultOverride,
    saveAgent,
    deleteAgent,
    isValidAgentName,
    type AgentInfo,
} from "./agents";
export { DEFAULT_BASE_PROMPT } from "./system-prompt";
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
    emitter: EventEmitter;
    maxSteps?: number;
    thinkingLevel?: ThinkingLevel;
    /** Named agent whose prompt replaces the built-in persona ("default" = built-in). */
    agent?: string;
    /** internal: recursion depth for Stop-hook continuations */
    hookDepth?: number;
}

function estimateContextTokens(messages: ModelMessage[]): number {
    let total = 0;
    for (const m of messages) {
        total += Math.ceil(JSON.stringify(m).length / 4);
    }
    return total;
}

/**
 * Anthropic prompt caching: two ephemeral breakpoints (limit is 4).
 * One on the system message — the cache prefix is tools → system, so this
 * covers both. One on the last message so the whole conversation prefix is
 * a cache hit on the next turn (90% input-cost discount on reads).
 * Other providers (OpenAI, xAI, Google) cache automatically server-side.
 */
function withAnthropicCaching(system: string, messages: ModelMessage[]): ModelMessage[] {
    const cache = { anthropic: { cacheControl: { type: "ephemeral" as const } } };
    const out: ModelMessage[] = [{ role: "system", content: system, providerOptions: cache }, ...messages];
    const last = out[out.length - 1];
    out[out.length - 1] = { ...last, providerOptions: cache } as ModelMessage;
    return out;
}

function toModelMessages(session: Session): ModelMessage[] {
    const out: ModelMessage[] = [];
    for (const m of compactedContextMessages(session)) {
        if (m.role === "tool") continue;
        const content = typeof m.content === "string" ? m.content : JSON.stringify(m.content);
        // Anthropic rejects empty text blocks ("text content blocks must be
        // non-empty") — aborted turns left empty assistant entries in older
        // transcripts, so filter on read, not just on write.
        if (content.trim() === "") continue;
        out.push({ role: m.role as "user" | "assistant", content });
    }
    return out;
}

// AI SDK prints advisory warnings (e.g. about system messages inside the
// messages array — our deliberate Anthropic prompt-caching pattern) straight
// to the console, which tears the TUI's differential rendering. Silence them.
(globalThis as Record<string, unknown>).AI_SDK_LOG_WARNINGS = false;

export async function runTurn(opts: RunTurnOptions): Promise<void> {
    const { session, modelId, userInput, cwd, abortSignal, tracker, emitter } = opts;
    // Step cap is only an upper safety bound — the loop ends naturally when the
    // model returns no tool call. 0 / unset means "run until the model decides".
    const configuredSteps = opts.maxSteps ?? (settingsStore.get("maxSteps") as number | undefined) ?? 0;
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
    const threshold = (settingsStore.get("autoCompactThreshold") as number) ?? 0.8;
    if (modelInfo) {
        const messages = toModelMessages(session);
        const tokens = estimateContextTokens(messages);
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
        (settingsStore.get("workspaceContext") as boolean) !== false
            ? loadWorkspaceContext(cwd)
            : { text: "", files: [] };
    // Project skills inject instructions into the prompt — gate on trust too.
    const skillsEnabled = (settingsStore.get("skills") as boolean) !== false && isTrusted(cwd);
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
    // Subagents: only full-toolset agents get the task tool — a restricted
    // agent (e.g. plan) must not escape its sandbox through a subagent.
    // Subagents themselves never get one (no nesting).
    const toolsForTurn: Record<string, unknown> = { ...toolSet };
    if (!allowedTools?.length) {
        toolsForTurn.task = createTaskTool({
            modelId,
            cwd,
            tracker,
            emitter,
            abortSignal,
            sessionId: session.id,
            transcriptPath: session.path,
        });
    }
    // System prompt is built AFTER the task tool decision so the model's tool
    // list matches reality, plus explicit delegation guidance when present.
    const subagentNote =
        "task" in toolsForTurn
            ? `\n\nDelegate self-contained or context-heavy work (broad searches, analysis, multi-file changes) to subagents with the task tool — each runs in its own context window and returns only a final report. When you use task, call it alone in that step; never alongside other tool calls. Available agents: ${listAgents()
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

    const thinkingLevel: ThinkingLevel =
        opts.thinkingLevel ?? (settingsStore.get("thinkingLevel") as ThinkingLevel | undefined) ?? "off";
    // Custom providers (gateways like bifrost) proxy a real vendor API — map
    // thinking/caching by the configured sdk so e.g. an anthropic-compatible
    // gateway gets adaptive thinking + prompt-cache breakpoints.
    let effectiveProvider: string = provider;
    if (isCustomProvider(provider)) {
        const sdk = getCustomProvider(parseCustomProviderId(provider)!)?.sdk;
        if (sdk) effectiveProvider = sdk === "openai-compatible" ? "openai" : sdk;
    }
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
    const result = streamText({
        model,
        ...(anthropicCaching
            ? // system inside messages is our deliberate Anthropic prompt-caching
              // pattern — allowSystemInMessages opts out of the AI SDK warning.
              { messages: withAnthropicCaching(system, messages), allowSystemInMessages: true }
            : { system, messages }),
        tools,
        stopWhen: stepCountIs(maxSteps),
        abortSignal,
        experimental_transform: smoothStream({ delayInMs: 20, chunking: "word" }),
        ...(providerOptions ? { providerOptions: providerOptions as never } : {}),
    });

    let assistantText = "";
    let lastUsage: UsageBlock | undefined;
    let lastStepUsage: UsageBlock | undefined;

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
                emitter.emit("tool-call", part);
                break;
            case "tool-result":
                emitter.emit("tool-result", part);
                break;
            case "finish-step": {
                // Cost accrues per step (one API round-trip each), not at turn
                // end — the footer updates live, and an aborted turn keeps the
                // cost of the steps that already ran. Step usages sum to the
                // turn total, so nothing is added again on finish.
                const u = (part as { usage?: UsageBlock }).usage;
                if (u) {
                    lastStepUsage = u;
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
    }

    // Aborted/tool-only turns can end with no text — don't persist an empty
    // assistant message (Anthropic rejects empty text blocks on later turns).
    if (assistantText.trim() !== "" || lastUsage) {
        await session.append({
            type: "message",
            ts: Date.now(),
            role: "assistant",
            content: assistantText,
            usage: lastUsage,
        });
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

type AnyTool = { execute?: (input: unknown, options: unknown) => Promise<unknown> };

/**
 * Wraps every tool's execute with PreToolUse / PostToolUse hooks.
 * Pre: deny → the tool never runs; the reason is returned as the tool result
 * so the model can react. updatedInput replaces the arguments.
 * Post: block/additionalContext are attached to the result as hook_feedback.
 */
function withToolHooks<T extends object>(
    tools: T,
    ctx: { cwd: string; sessionId: string; transcriptPath: string; emitter: EventEmitter; agentId?: string },
): T {
    // agent_id marks subagent tool calls in hook payloads (Claude Code parity —
    // watchers like herdr use it to tell main-agent and subagent activity apart).
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
                            title: "pi",
                        },
                        ctx.cwd,
                    ).then((n) => {
                        for (const s of n.terminalSequences) ctx.emitter.emit("hook-terminal-sequence", s);
                    });
                    return { error: `blocked by PreToolUse hook: ${pre.reason}` };
                }
                const effectiveInput = pre.updatedInput !== undefined ? pre.updatedInput : input;
                // Hook rewrote the input (e.g. rtk): update the rendered tool call in
                // place so the chat shows what actually executed — no separate line.
                // Main agent only: subagent tool calls aren't rendered as own
                // components, and their ids could collide with main ones.
                if (pre.updatedInput !== undefined && !ctx.agentId) {
                    const toolCallId = (options as { toolCallId?: string } | undefined)?.toolCallId;
                    ctx.emitter.emit("tool-input-updated", { toolCallId, toolName: name, input: effectiveInput });
                }
                const output = await t.execute!(effectiveInput, options);
                const post = await runHooks(
                    "PostToolUse",
                    name,
                    {
                        session_id: ctx.sessionId,
                        transcript_path: ctx.transcriptPath,
                        tool_name: name,
                        tool_input: effectiveInput,
                        // Claude Code sends `tool_response`; keep `tool_output` too for
                        // any pi hooks already written against it.
                        tool_response: output,
                        tool_output: output,
                        ...agentFields,
                    },
                    ctx.cwd,
                );
                for (const m of post.messages) ctx.emitter.emit("hook-message", m);
                for (const s of post.terminalSequences) ctx.emitter.emit("hook-terminal-sequence", s);
                const feedback = [post.block ? `BLOCKED: ${post.reason}` : null, post.additionalContext]
                    .filter(Boolean)
                    .join("\n");
                if (feedback) {
                    if (output && typeof output === "object") return { ...(output as object), hook_feedback: feedback };
                    return { result: output, hook_feedback: feedback };
                }
                return output;
            },
        };
    }
    return wrapped as unknown as T;
}

// ---------------------------------------------------------------------------
// Subagents — the task tool runs a nested agent loop (own context window,
// own toolset from the chosen agent). Streaming surfaces through the parent
// emitter (subagent-delta / subagent-tool / subagent-finish, keyed by the
// task toolCallId) and usage aggregates into the parent's CostTracker.
// ---------------------------------------------------------------------------

const SUBAGENT_SYSTEM_SUFFIX = `

You are a subagent launched by a main agent. Work autonomously — you cannot ask the user questions. When done, end with a complete final report; only that final text is returned to the main agent.`;

interface SubagentCtx {
    modelId: string;
    cwd: string;
    tracker: CostTracker;
    emitter: EventEmitter;
    abortSignal?: AbortSignal;
    sessionId: string;
    transcriptPath: string;
}

function createTaskTool(ctx: SubagentCtx) {
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

async function runSubagent(
    ctx: SubagentCtx,
    agentName: string | undefined,
    prompt: string,
    toolCallId: string,
): Promise<string> {
    const name = agentName && agentExists(agentName) ? agentName : DEFAULT_AGENT_NAME;
    try {
        const allowed = getAgentTools(name);
        const full = createTools({ cwd: ctx.cwd, abortSignal: ctx.abortSignal });
        const subTools = (
            allowed?.length ? Object.fromEntries(Object.entries(full).filter(([n]) => allowed.includes(n))) : full
        ) as typeof full;
        // Subagent tool calls run the same PreToolUse/PostToolUse hooks,
        // tagged with agent_id so watchers can tell them apart.
        const hooked = withToolHooks(subTools, {
            cwd: ctx.cwd,
            sessionId: ctx.sessionId,
            transcriptPath: ctx.transcriptPath,
            emitter: ctx.emitter,
            agentId: toolCallId,
        });
        const maxSteps = (settingsStore.get("subagentMaxSteps") as number | undefined) || 50;
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
                    const u = (part as { totalUsage?: UsageBlock }).totalUsage;
                    ctx.emitter.emit("subagent-finish", { toolCallId, agent: name, usage: u });
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

        // Plain text result — it renders as-is in the tool box and is exactly
        // what the parent model reads. No JSON wrapping.
        return text.trim() || "(subagent produced no output)";
    } catch (err) {
        return `Subagent failed: ${err instanceof Error ? err.message : String(err)}`;
    }
}
