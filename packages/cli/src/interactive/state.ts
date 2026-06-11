import type { ProviderId, Session, ThinkingLevel } from "@notshekhar/pi-core";

/**
 * Mutable runtime state for the interactive app. Handlers in
 * command-handlers / input-handler / turn-runner read and mutate fields here
 * so app.ts doesn't have to thread dozens of `let` bindings through closures.
 */
export interface AppState {
    cwd: string;
    modelId: string;
    provider: ProviderId;
    thinkingLevel: ThinkingLevel;
    /** Active agent name ("default" = built-in system prompt). */
    agent: string;
    /** One-shot agent for the next turn only (/<agent> <message>); cleared at turn start. */
    oneShotAgent: string | null;
    /** Last custom agent selected via /agents — stays in the Tab cycle alongside built-ins. */
    cycleCustomAgent: string | null;
    session: Session | null;
    latestContextTokens: number;
    busy: boolean;
    abort: AbortController;
    pendingInjection: string | null;
    lastCtrlCAt: number;
    /** Resolves when trust prompt + SessionStart hooks settle; the first turn
     * awaits it so hook-injected context isn't lost to a fast first prompt. */
    startupHooksDone: Promise<void> | null;
}
