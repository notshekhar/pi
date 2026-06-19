/**
 * /timer and /reminder.
 *
 * Timer: in-memory countdown shown in the footer; the time's-up prompt is
 * handled by the app ticker (app.ts). Reminders: persisted CRUD over
 * ~/.loop/reminders.json — one-time ("10m", "18:30", "2026-06-15 09:00") or
 * cron-scheduled (up to 6 fields, second-level), managed through the same
 * selector flow as the model picker.
 */
import type { SelectItem } from "@notshekhar/loop-tui";
import chalk from "chalk";
import {
    addReminder,
    deleteReminder,
    listReminders,
    MAX_REMINDERS,
    updateReminder,
    type CommandContext,
    type Reminder,
    type ReminderSchedule,
} from "@notshekhar/loop-core";
import { Cron } from "croner";
import type { AppDeps } from "../deps";
import type { AppState } from "../state";
import { formatCountdown, formatWhen, parseDuration, parseOnceWhen } from "../time";

type TimerHandlers = Pick<CommandContext, "setTimer" | "openReminders">;

const ADD = "\x00add";

export function createTimerHandlers(state: AppState, deps: AppDeps): TimerHandlers {
    const { tui, history, footer, selectOnce, searchOnce, promptOnce, syncTicker } = deps;

    const say = (text: string) => {
        history.addSystem(text);
        tui.requestRender();
    };

    /** Prompt for a schedule: once (duration or absolute) or cron. Null = cancelled. */
    async function promptSchedule(initial?: ReminderSchedule): Promise<ReminderSchedule | null> {
        const kind = await selectOnce(
            [
                { value: "once", label: "once", description: "10m · 18:30 · 2026-06-15 09:00" },
                { value: "cron", label: "cron", description: "second-level, up to 6 fields, e.g. 0 */45 * * * *" },
            ],
            "Reminder schedule",
        );
        if (!kind) return null;

        if (kind.value === "once") {
            const raw = await promptOnce("when (10m / 18:30 / 2026-06-15 09:00)");
            const at = parseOnceWhen(raw);
            if (at === null || at <= Date.now()) {
                say(chalk.red(`can't parse a future time from: ${raw}`));
                return null;
            }
            return { kind: "once", at };
        }

        const expr = (await promptOnce("cron expression", initial?.kind === "cron" ? initial.expr : "")).trim();
        if (!expr) return null;
        try {
            new Cron(expr); // validate only — scheduling happens in the app ticker
        } catch {
            say(chalk.red(`invalid cron expression: ${expr}`));
            return null;
        }
        return { kind: "cron", expr };
    }

    function scheduleLabel(r: Reminder): string {
        const when = r.kind === "once" ? formatWhen(r.at) : r.expr;
        return `${r.kind} ${when}${r.enabled ? "" : "  (off)"}`;
    }

    return {
        setTimer(args) {
            const input = args.trim().toLowerCase();
            if (input === "off" || input === "cancel") {
                state.timerEndsAt = null;
                state.timerLabel = "";
                footer.setTimer(null);
                syncTicker();
                say("timer cancelled");
                return;
            }
            if (!input) {
                if (state.timerEndsAt === null) {
                    say("no timer running — usage: /timer 1h30m · /timer off");
                } else {
                    say(`timer: ${formatCountdown(state.timerEndsAt - Date.now())} left (of ${state.timerLabel})`);
                }
                return;
            }
            const ms = parseDuration(input);
            if (ms === null) {
                say(chalk.red(`can't parse duration: ${input} — try 30s, 5m, 1h30m, 1d`));
                return;
            }
            state.timerEndsAt = Date.now() + ms;
            state.timerLabel = input;
            footer.setTimer(state.timerEndsAt);
            syncTicker();
            say(`timer set — ${input}`);
        },

        async openReminders() {
            let lastIndex = 0;
            while (true) {
                const reminders = listReminders();
                const items: SelectItem[] = [
                    { value: ADD, label: "+ add reminder…", description: "one-time or cron-scheduled" },
                    ...reminders.map((r) => ({
                        value: r.id,
                        label: r.text,
                        description: scheduleLabel(r),
                    })),
                ];
                const pick = await searchOnce(items, `Reminders · ${reminders.length}`, { initialIndex: lastIndex });
                if (!pick) return;
                lastIndex = Math.max(0, items.findIndex((i) => i.value === pick.value));

                if (pick.value === ADD) {
                    if (reminders.length >= MAX_REMINDERS) {
                        say(chalk.yellow(`reminder limit reached (max ${MAX_REMINDERS}) — delete one first`));
                        continue;
                    }
                    const text = (await promptOnce("reminder text")).trim();
                    if (!text) continue;
                    const schedule = await promptSchedule();
                    if (!schedule) continue;
                    addReminder(text, schedule);
                    syncTicker();
                    say(`reminder added — ${text}`);
                    continue;
                }

                const reminder = reminders.find((r) => r.id === pick.value);
                if (!reminder) continue;
                const action = await selectOnce(
                    [
                        { value: "toggle", label: reminder.enabled ? "disable" : "enable" },
                        { value: "text", label: "edit text", description: reminder.text },
                        { value: "schedule", label: "edit schedule", description: scheduleLabel(reminder) },
                        { value: "delete", label: "delete" },
                    ],
                    reminder.text,
                );
                if (!action) continue;

                if (action.value === "toggle") {
                    updateReminder(reminder.id, { enabled: !reminder.enabled });
                } else if (action.value === "text") {
                    const text = (await promptOnce("reminder text", reminder.text)).trim();
                    if (text) updateReminder(reminder.id, { text });
                } else if (action.value === "schedule") {
                    const schedule = await promptSchedule(reminder);
                    if (schedule) updateReminder(reminder.id, { schedule, enabled: true });
                } else if (action.value === "delete") {
                    deleteReminder(reminder.id);
                }
                syncTicker();
            }
        },
    };
}
