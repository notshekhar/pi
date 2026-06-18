import { deleteReminder, listReminders, settingsStore } from "@notshekhar/loop-core";
import { Cron } from "croner";
import type { SelectItem, TUI } from "@notshekhar/loop-tui";
import type { CostFooter } from "./components/cost-footer";
import type { AppState } from "./state";

export interface TickerDeps {
    state: AppState;
    footer: CostFooter;
    tui: TUI;
    /** Open count of selectors — notices wait until the input slot is free. */
    getSelectorDepth: () => number;
    /** Swap an "ok" prompt into the input slot (used to surface a fired notice). */
    selectOnce: (items: SelectItem[], title?: string) => Promise<unknown>;
}

export interface Ticker {
    /** Start/stop the 1s pulse to match what currently needs it, and repaint. */
    syncTicker(): void;
    /** Clear the pulse unconditionally (shutdown). Idempotent. */
    stopTicker(): void;
}

/**
 * Shared 1s ticker: footer clock, /timer countdown, reminder scheduler. Runs
 * only while one of them needs it, so idle sessions hold no timers. Pulled out
 * of the app orchestrator — it owns all timer/reminder/notice state internally
 * and exposes only syncTicker() to wire up.
 */
export function createTicker(deps: TickerDeps): Ticker {
    const { state, footer, tui, getSelectorDepth, selectOnce } = deps;

    let ticker: ReturnType<typeof setInterval> | null = null;
    let lastTickAt = Date.now();
    const notices: string[] = [];
    let noticeShowing = false;

    const remindersMuted = () => settingsStore.get("reminders") === false;

    function tickerNeeded(): boolean {
        const clockOn = settingsStore.get("clock") === true;
        const remindersPending = !remindersMuted() && listReminders().some((r) => r.enabled);
        // Keep ticking while notices are queued: a reminder/timer that fired
        // during a turn is held until !busy, and a one-shot reminder deletes
        // itself when it fires — without this the ticker could stop before the
        // turn ends, stranding the notice so it never shows.
        return clockOn || state.timerEndsAt !== null || remindersPending || notices.length > 0;
    }

    function syncTicker(): void {
        footer.setClockEnabled(settingsStore.get("clock") === true);
        const needed = tickerNeeded();
        if (needed && ticker === null) {
            lastTickAt = Date.now();
            ticker = setInterval(onTick, 1000);
        } else if (!needed && ticker !== null) {
            clearInterval(ticker);
            ticker = null;
        }
        tui.requestRender();
    }

    function onTick(): void {
        const now = Date.now();
        checkTimer(now);
        checkReminders(now);
        lastTickAt = now;
        void drainNotices();
        syncTicker(); // also renders; stops the pulse once nothing needs it
    }

    function checkTimer(now: number): void {
        if (state.timerEndsAt === null || now < state.timerEndsAt) return;
        const label = state.timerLabel;
        state.timerEndsAt = null;
        state.timerLabel = "";
        footer.setTimer(null);
        ring(`Timer over — ${label}`);
    }

    function checkReminders(now: number): void {
        if (remindersMuted()) return; // muted: nothing fires, reminders stay stored
        for (const r of listReminders()) {
            if (!r.enabled) continue;
            if (r.kind === "once") {
                // Fires only if the moment passed during this tick — a deadline
                // that lapsed while loop was closed never fires (by design).
                if (r.at > lastTickAt && r.at <= now) {
                    ring(`Reminder — ${r.text}`);
                    deleteReminder(r.id); // one-shot: gone after firing; cron ones live on
                }
            } else {
                try {
                    const next = new Cron(r.expr).nextRun(new Date(lastTickAt));
                    if (next && next.getTime() <= now) ring(`Reminder — ${r.text}`);
                } catch {
                    // invalid expression — the manager validates on entry; skip
                }
            }
        }
    }

    function ring(title: string): void {
        process.stdout.write("\x07"); // terminal bell
        notices.push(title);
    }

    // Time's-up / reminder prompts swap into the input slot like any selector,
    // but only when it's free: never during a streaming turn, never on top of
    // an open picker. Enter (ok) or Esc dismisses; queued ones follow.
    async function drainNotices(): Promise<void> {
        if (noticeShowing) return;
        noticeShowing = true;
        try {
            while (notices.length > 0 && !state.busy && getSelectorDepth() === 0) {
                const title = notices.shift()!;
                await selectOnce([{ value: "ok", label: "ok" }], title);
            }
        } finally {
            noticeShowing = false;
        }
    }

    function stopTicker(): void {
        if (ticker !== null) {
            clearInterval(ticker);
            ticker = null;
        }
    }

    return { syncTicker, stopTicker };
}
