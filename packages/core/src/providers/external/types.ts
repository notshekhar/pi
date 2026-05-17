import type { EventEmitter } from "node:events";
import type { CostTracker } from "../../agent/cost";
import type { Session } from "../../sessions";

export interface ExternalRunOpts {
  session: Session;
  modelId: string;
  userInput: string;
  cwd: string;
  abortSignal?: AbortSignal;
  tracker: CostTracker;
  emitter: EventEmitter;
  workspaceContext?: string;
  skillsPrompt?: string;
}

export type ExternalAgentRunner = (opts: ExternalRunOpts) => Promise<void>;

export interface SdkSessionRefPayload {
  kind: "sdk-session-ref";
  provider: string;
  sdkSessionId: string;
}

export function readSdkSessionRef(session: Session, provider: string): string | undefined {
  for (const entry of session.entries()) {
    if (entry.type !== "custom") continue;
    const p = entry.payload as SdkSessionRefPayload;
    if (p?.kind === "sdk-session-ref" && p.provider === provider) {
      return p.sdkSessionId;
    }
  }
  return undefined;
}

export async function writeSdkSessionRef(session: Session, provider: string, sdkSessionId: string): Promise<void> {
  if (readSdkSessionRef(session, provider) === sdkSessionId) return;
  const payload: SdkSessionRefPayload = { kind: "sdk-session-ref", provider, sdkSessionId };
  await session.append({ type: "custom", payload, ts: Date.now() });
}
