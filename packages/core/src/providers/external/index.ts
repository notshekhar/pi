import { runClaudeAgentTurn } from "./claude-agent";
import { runCursorAgentTurn } from "./cursor-agent";
import type { ExternalAgentRunner, ExternalRunOpts } from "./types";

const REGISTRY: Record<string, ExternalAgentRunner> = {
  "claude-agent": runClaudeAgentTurn,
  "cursor-agent": runCursorAgentTurn,
};

export function getExternalRunner(provider: string): ExternalAgentRunner | undefined {
  return REGISTRY[provider];
}

export async function runExternalAgentTurn(provider: string, opts: ExternalRunOpts): Promise<void> {
  const runner = getExternalRunner(provider);
  if (!runner) throw new Error(`No external runner for provider: ${provider}`);
  return runner(opts);
}

export type { ExternalAgentRunner, ExternalRunOpts } from "./types";
export { readSdkSessionRef, writeSdkSessionRef } from "./types";
