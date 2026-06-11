import { EventEmitter } from "node:events";
import {
    CostTracker,
    SessionManager,
    runTurn,
    runHooks,
    getActiveProvider,
    getProjectModel,
    settingsStore,
} from "@notshekhar/pi-core";
import type { ProviderId } from "@notshekhar/pi-core";

export interface PrintOptions {
    prompt: string;
    modelId?: string;
    cwd: string;
}

export async function runPrint(opts: PrintOptions): Promise<void> {
    const provider = (getActiveProvider() ?? "xai") as ProviderId;
    const modelId =
        opts.modelId ??
        getProjectModel(opts.cwd) ??
        (settingsStore.get("defaultModel") as string) ??
        `${provider}/grok-build-0.1`;
    const manager = new SessionManager();
    const session = await manager.create({ cwd: opts.cwd, provider, model: modelId });
    const tracker = new CostTracker();
    const emitter = new EventEmitter();
    const abort = new AbortController();

    emitter.on("text-delta", (text: string) => process.stdout.write(text));
    emitter.on("tool-call", (part: { toolName?: string; input?: unknown }) => {
        process.stderr.write(`\n[tool:${part.toolName}] ${JSON.stringify(part.input)}\n`);
    });
    emitter.on("tool-input-updated", (e: { toolName?: string; input?: unknown }) => {
        process.stderr.write(`[tool:${e.toolName} rewritten] ${JSON.stringify(e.input)}\n`);
    });
    emitter.on("subagent-tool", (e: { agent: string; toolName?: string; input?: unknown }) => {
        process.stderr.write(`[subagent:${e.agent}] ${e.toolName} ${JSON.stringify(e.input)}\n`);
    });
    emitter.on("subagent-finish", (e: { agent: string; usage?: { totalTokens?: number } }) => {
        process.stderr.write(
            `[subagent:${e.agent}] done${e.usage?.totalTokens ? ` (${e.usage.totalTokens} tokens)` : ""}\n`,
        );
    });
    emitter.on("hook-message", (m: string) => {
        process.stderr.write(`\n[hook] ${m}\n`);
    });
    emitter.on("hook-terminal-sequence", (s: string) => {
        process.stdout.write(s);
    });
    emitter.on("error", (err: unknown) => {
        process.stderr.write(`\n[error] ${String(err)}\n`);
    });
    emitter.on("finish", () => process.stdout.write("\n"));

    process.on("SIGINT", () => abort.abort());

    // SessionStart hooks run in print mode too (Claude Code -p parity);
    // additionalContext is prepended to the one-shot prompt.
    const startHooks = await runHooks(
        "SessionStart",
        "startup",
        { session_id: session.id, transcript_path: session.path, source: "startup" },
        opts.cwd,
    );
    for (const m of startHooks.messages) process.stderr.write(`\n[hook] ${m}\n`);
    for (const s of startHooks.terminalSequences) process.stdout.write(s);
    const userInput = startHooks.additionalContext ? `${startHooks.additionalContext}\n\n${opts.prompt}` : opts.prompt;

    await runTurn({
        session,
        modelId,
        userInput,
        cwd: opts.cwd,
        abortSignal: abort.signal,
        tracker,
        emitter,
        agent: (settingsStore.get("agent") as string | undefined) ?? undefined,
    });

    // SessionEnd hooks: give them a moment, then finish regardless.
    await Promise.race([
        runHooks(
            "SessionEnd",
            undefined,
            { session_id: session.id, transcript_path: session.path, reason: "exit" },
            opts.cwd,
        ),
        new Promise((r) => setTimeout(r, 3_000)),
    ]);

    process.stderr.write(`\n${tracker.format()}\n`);
}
