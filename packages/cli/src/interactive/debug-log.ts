/**
 * Toggleable event tracer for the interactive app.
 *
 * Off by default. Turn on with `LOOP_DEBUG_EVENTS=1` at startup, or Shift+Ctrl+D
 * at runtime. When on, every traced event is appended to ~/.loop/events-debug.log
 * (timestamped, with the delta since the previous event) AND surfaced as a dim
 * line in the chat, so the order and timing of input/turn/abort events is
 * visible live. This exists because event-ordering bugs (e.g. a single Esc
 * firing twice via the Kitty press+release pair) are near-impossible to spot by
 * reading code — but obvious the moment you see the trace.
 */
import { appendFileSync } from "node:fs";
import { join } from "node:path";
import { getLoopDir } from "@notshekhar/loop-core";

let enabled = process.env.LOOP_DEBUG_EVENTS === "1";
let sink: ((line: string) => void) | null = null;
let lastAt = 0;

const LOG_PATH = join(getLoopDir(), "events-debug.log");

/** UI sink (set by app.ts) that renders a trace line in the chat. */
export function setEventTraceSink(fn: ((line: string) => void) | null): void {
    sink = fn;
}

export function isEventTraceEnabled(): boolean {
    return enabled;
}

/** Flip tracing on/off (Shift+Ctrl+D). Returns the new state. */
export function toggleEventTrace(): boolean {
    enabled = !enabled;
    lastAt = 0; // reset the delta clock so the next line starts fresh
    return enabled;
}

/**
 * Record one event. `category` is a short tag (e.g. "input", "turn", "abort");
 * `detail` is free-form. No-op when disabled, so call sites stay cheap.
 */
export function traceEvent(category: string, detail = ""): void {
    if (!enabled) return;
    const now = Date.now();
    const delta = lastAt === 0 ? 0 : now - lastAt;
    lastAt = now;
    const ts = new Date(now).toISOString().slice(11, 23); // HH:MM:SS.mmm
    const line = `${ts} +${String(delta).padStart(4)}ms  ${category.padEnd(7)} ${detail}`;
    // File tee never throws into the turn loop — tracing must never break a run.
    try {
        appendFileSync(LOG_PATH, line + "\n");
    } catch {
        // ignore — best effort
    }
    sink?.(line);
}
