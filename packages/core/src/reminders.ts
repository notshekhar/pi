/**
 * Persistent reminders — ~/.pi/reminders.json via configstore, same pattern
 * as auth/settings/cost. A reminder is one-shot ("once", absolute timestamp)
 * or recurring ("cron", up to 6-field second-level expression, stored
 * verbatim). Scheduling/firing is the CLI's job; this module only persists.
 * There is deliberately no seen/missed tracking — reminders fire only while
 * pi is open.
 */
import { join } from "node:path";
import { ulid } from "ulid";
import { CachedStore, getPiDir } from "./auth/storage";

export type ReminderSchedule = { kind: "once"; at: number } | { kind: "cron"; expr: string };

export type Reminder = ReminderSchedule & {
    id: string;
    text: string;
    enabled: boolean;
};

// Cached: the 1s ticker reads this list twice a second (tickerNeeded +
// checkReminders); without the cache that's two synchronous disk reads/sec.
const remindersStore = new CachedStore(
    "pi-agent-reminders",
    { reminders: [] },
    { configPath: join(getPiDir(), "reminders.json") },
);

/** Hard cap on stored reminders — keeps the manager list and ticker scan small. */
export const MAX_REMINDERS = 10;

export function listReminders(): Reminder[] {
    return (remindersStore.get("reminders") as Reminder[] | undefined) ?? [];
}

export class ReminderLimitError extends Error {
    constructor() {
        super(`reminder limit reached (max ${MAX_REMINDERS})`);
        this.name = "ReminderLimitError";
    }
}

export function addReminder(text: string, schedule: ReminderSchedule): Reminder {
    const existing = listReminders();
    if (existing.length >= MAX_REMINDERS) throw new ReminderLimitError();
    const reminder: Reminder = { id: ulid(), text, enabled: true, ...schedule };
    remindersStore.set("reminders", [...existing, reminder]);
    return reminder;
}

export interface ReminderPatch {
    text?: string;
    enabled?: boolean;
    /** Replaces the schedule wholesale, so a kind switch leaves no stale fields. */
    schedule?: ReminderSchedule;
}

export function updateReminder(id: string, patch: ReminderPatch): Reminder | undefined {
    let updated: Reminder | undefined;
    const next = listReminders().map((r) => {
        if (r.id !== id) return r;
        const schedule: ReminderSchedule =
            patch.schedule ?? (r.kind === "once" ? { kind: "once", at: r.at } : { kind: "cron", expr: r.expr });
        updated = {
            id: r.id,
            text: patch.text ?? r.text,
            enabled: patch.enabled ?? r.enabled,
            ...schedule,
        };
        return updated;
    });
    if (updated) remindersStore.set("reminders", next);
    return updated;
}

export function deleteReminder(id: string): boolean {
    const all = listReminders();
    const next = all.filter((r) => r.id !== id);
    if (next.length === all.length) return false;
    remindersStore.set("reminders", next);
    return true;
}
