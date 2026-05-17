import { EventEmitter } from "node:events";
import { CostTracker, SessionManager, runTurn, getActiveProvider, settingsStore } from "@pi-agent/core";
import type { ProviderId } from "@pi-agent/core";

export interface PrintOptions {
  prompt: string;
  modelId?: string;
  cwd: string;
}

export async function runPrint(opts: PrintOptions): Promise<void> {
  const provider = (getActiveProvider() ?? "xai") as ProviderId;
  const modelId = opts.modelId ?? (settingsStore.get("defaultModel") as string) ?? `${provider}/grok-4`;
  const manager = new SessionManager();
  const session = await manager.create({ cwd: opts.cwd, provider, model: modelId });
  const tracker = new CostTracker();
  const emitter = new EventEmitter();
  const abort = new AbortController();

  emitter.on("text-delta", (text: string) => process.stdout.write(text));
  emitter.on("tool-call", (part: { toolName?: string; input?: unknown }) => {
    process.stderr.write(`\n[tool:${part.toolName}] ${JSON.stringify(part.input)}\n`);
  });
  emitter.on("error", (err: unknown) => {
    process.stderr.write(`\n[error] ${String(err)}\n`);
  });
  emitter.on("finish", () => process.stdout.write("\n"));

  process.on("SIGINT", () => abort.abort());

  await runTurn({
    session,
    modelId,
    userInput: opts.prompt,
    cwd: opts.cwd,
    abortSignal: abort.signal,
    tracker,
    emitter,
  });

  process.stderr.write(`\n${tracker.format()}\n`);
}
