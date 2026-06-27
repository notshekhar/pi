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
    /** A tool call has begun streaming its input — fires before `tool-call`
     * (which only arrives once the whole input is parsed). Lets the UI show the
     * tool box as pending immediately, so a tool with a large input (e.g. write's
     * file content) doesn't appear to pop in late. */
    "tool-input-start": { toolName?: string; toolCallId?: string };
    "tool-call": { toolName?: string; input?: unknown; toolCallId?: string };
    "tool-result": { toolCallId?: string; output?: unknown };
    /** A tool's execute threw (or timed out). The error rides back to the model
     * as the tool result, so the agent can recover; the UI renders it red. */
    "tool-error": { toolCallId?: string; toolName?: string; error: unknown };
    "tool-input-updated": { toolCallId?: string; toolName: string; input: unknown };
    "attached-images": string[];
    "hook-message": string;
    "hook-terminal-sequence": string;
    "compact-start": { reason: string };
    "compact-end": { summary: string; cutAt: number; tokensBefore: number; tokensAfter?: number; aborted?: boolean };
    "step-usage": { usage: UsageBlock; breakdown: CostBreakdown };
    /** Post-turn one-line recap (AI SDK data-* part convention). Arrives after finish. */
    "data-recap": { text: string };
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
