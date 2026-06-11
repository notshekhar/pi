/**
 * Typed event map for the turn emitter. Every event the agent loop emits is
 * declared here — a typo'd event name or wrong payload shape fails the build
 * instead of silently doing nothing at runtime.
 */
import type { EventEmitter } from "node:events";
import type { CostBreakdown, UsageBlock } from "../types";

export interface TurnEvents {
    "text-delta": string;
    "reasoning-start": void;
    "reasoning-delta": string;
    "reasoning-end": void;
    "tool-call": { toolName?: string; input?: unknown; toolCallId?: string };
    "tool-result": { toolCallId?: string; output?: unknown };
    "tool-input-updated": { toolCallId?: string; toolName: string; input: unknown };
    "attached-images": string[];
    "hook-message": string;
    "hook-terminal-sequence": string;
    "compact-start": { reason: string };
    "compact-end": { summary: string; cutAt: number; tokensBefore: number; tokensAfter?: number; aborted?: boolean };
    "step-usage": { usage: UsageBlock; breakdown: CostBreakdown };
    finish: { usage?: UsageBlock; lastStepUsage?: UsageBlock };
    error: unknown;
    "subagent-delta": { toolCallId: string; agent: string; text: string };
    "subagent-tool": { toolCallId: string; agent: string; toolName?: string; input?: unknown };
    "subagent-step-usage": { toolCallId: string; agent: string; usage: UsageBlock };
    "subagent-finish": { toolCallId: string; agent: string; usage?: UsageBlock };
}

type Args<K extends keyof TurnEvents> = TurnEvents[K] extends void ? [] : [TurnEvents[K]];

/** Structurally satisfied by node's EventEmitter — `new EventEmitter()` works. */
export interface TurnEmitter {
    emit<K extends keyof TurnEvents>(event: K, ...args: Args<K>): boolean;
    on<K extends keyof TurnEvents>(event: K, listener: (...args: Args<K>) => void): this;
}

/** Convenience: a plain EventEmitter viewed through the typed surface. */
export function asTurnEmitter(emitter: EventEmitter): TurnEmitter {
    return emitter as unknown as TurnEmitter;
}
