/**
 * Thin adapters that fold extension turn-middleware over the assembly points of
 * runTurn. Each reads the host's middleware list, which is empty when no
 * extensions are loaded — so every function here is a guaranteed no-op in a
 * clean install (the result is the same reference/value that was passed in).
 *
 * A throwing middleware must never break a turn, so each call is defensively
 * wrapped; the one exception is onBeforeTurn, where an explicit `false` is a
 * deliberate block (a thrown error there is swallowed and treated as "allow").
 */
import { getExtensionHost } from "../extensions";
import type { TurnContext, TurnMiddleware } from "../extensions/api";

function middlewares(): TurnMiddleware[] {
    return getExtensionHost().getTurnMiddleware();
}

/** Returns false if any middleware blocked the turn. */
export async function runBeforeTurn(ctx: {
    input: string;
    cwd: string;
    sessionId: string;
    agent: string;
    modelId: string;
}): Promise<boolean> {
    for (const m of middlewares()) {
        if (!m.onBeforeTurn) continue;
        try {
            if ((await m.onBeforeTurn(ctx)) === false) return false;
        } catch {
            // a broken guard must not block the turn
        }
    }
    return true;
}

export async function applySystemPrompt(prompt: string, ctx: TurnContext): Promise<string> {
    let out = prompt;
    for (const m of middlewares()) {
        if (!m.onSystemPrompt) continue;
        try {
            const r = await m.onSystemPrompt(out, ctx);
            if (typeof r === "string") out = r;
        } catch {
            /* keep the prompt as-is */
        }
    }
    return out;
}

export async function applyAssembleTools(
    tools: Record<string, unknown>,
    ctx: TurnContext,
): Promise<Record<string, unknown>> {
    let out = tools;
    for (const m of middlewares()) {
        if (!m.onAssembleTools) continue;
        try {
            const r = await m.onAssembleTools(out as never, ctx);
            if (r && typeof r === "object") out = r as Record<string, unknown>;
        } catch {
            /* keep tools as-is */
        }
    }
    return out;
}

export function applyProviderOptions(opts: unknown, ctx: TurnContext): unknown {
    let out = opts;
    for (const m of middlewares()) {
        if (!m.onProviderOptions) continue;
        try {
            const r = m.onProviderOptions(out, ctx);
            if (r !== undefined) out = r;
        } catch {
            /* keep options as-is */
        }
    }
    return out;
}

export async function runAfterTurn(ctx: TurnContext): Promise<void> {
    for (const m of middlewares()) {
        if (!m.onAfterTurn) continue;
        try {
            await m.onAfterTurn(ctx);
        } catch {
            /* post-turn side effects are best-effort */
        }
    }
}
