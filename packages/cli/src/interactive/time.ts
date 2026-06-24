/**
 * Time parsing/formatting for /timer, /reminder, and the status line clock.
 */

const DURATION_UNIT_MS: Record<string, number> = {
    s: 1000,
    m: 60_000,
    h: 3_600_000,
    d: 86_400_000,
};

/** "1h30m" / "45s" / "2d" → milliseconds, or null if not a pure duration. */
export function parseDuration(input: string): number | null {
    const trimmed = input.trim().toLowerCase();
    if (!/^(\d+[smhd])+$/.test(trimmed)) return null;
    let ms = 0;
    for (const [, n, unit] of trimmed.matchAll(/(\d+)([smhd])/g)) {
        ms += Number(n) * DURATION_UNIT_MS[unit];
    }
    return ms > 0 ? ms : null;
}

/**
 * One-time reminder input → absolute ms epoch, or null if unparseable.
 * Accepts a duration from now ("10m"), "HH:MM" (today, or tomorrow if that
 * time already passed), and "YYYY-MM-DD HH:MM".
 */
export function parseOnceWhen(input: string, now = Date.now()): number | null {
    const trimmed = input.trim();

    const duration = parseDuration(trimmed);
    if (duration !== null) return now + duration;

    const timeOnly = /^(\d{1,2}):(\d{2})$/.exec(trimmed);
    if (timeOnly) {
        const at = new Date(now);
        at.setHours(Number(timeOnly[1]), Number(timeOnly[2]), 0, 0);
        if (at.getTime() <= now) at.setDate(at.getDate() + 1);
        return at.getTime();
    }

    const dateTime = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{1,2}):(\d{2})$/.exec(trimmed);
    if (dateTime) {
        const [, y, mo, d, h, mi] = dateTime;
        const at = new Date(Number(y), Number(mo) - 1, Number(d), Number(h), Number(mi), 0, 0);
        return Number.isNaN(at.getTime()) ? null : at.getTime();
    }

    return null;
}

/** Remaining ms → "45s" / "12:34" / "1:02:03" / "2d 01:02:03". */
export function formatCountdown(remainingMs: number): string {
    const total = Math.max(0, Math.ceil(remainingMs / 1000));
    const days = Math.floor(total / 86_400);
    const hours = Math.floor((total % 86_400) / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    const seconds = total % 60;
    const pad = (n: number) => String(n).padStart(2, "0");
    if (days > 0) return `${days}d ${pad(hours)}:${pad(minutes)}:${pad(seconds)}`;
    if (hours > 0) return `${hours}:${pad(minutes)}:${pad(seconds)}`;
    if (minutes > 0) return `${minutes}:${pad(seconds)}`;
    return `${seconds}s`;
}

/** "2026-06-13 21:48:32" — the status line clock. */
export function formatClock(date = new Date()): string {
    const pad = (n: number) => String(n).padStart(2, "0");
    const ymd = `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
    const hms = `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
    return `${ymd} ${hms}`;
}

/** Human label for a reminder's schedule, shown in the manager list. */
export function formatWhen(at: number): string {
    return formatClock(new Date(at)).slice(0, 16); // drop seconds
}
