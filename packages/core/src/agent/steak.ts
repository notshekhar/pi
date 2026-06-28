/**
 * /steak — a GitHub-contributions-style calendar heatmap of token usage.
 *
 * This module is pure layout: it turns a per-day token map into a grid of
 * intensity levels plus month labels. Coloring lives in the CLI (chalk), so
 * core stays presentation-free. The day map itself comes from
 * `SessionManager.dailyTokens()`, which reconstructs full history from session
 * transcripts — so the graph is rich on first run, no cold-start.
 */

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** Local-timezone YYYY-MM-DD — buckets roll over at the user's midnight, matching cost.ts. */
function dayKey(d: Date): string {
    return d.toLocaleDateString("sv");
}

export interface SteakGrid {
    /** Sum of tokens across every visible day. */
    totalTokens: number;
    /** Number of week columns. */
    weeks: number;
    /**
     * cells[weekday][weekIndex], weekday 0=Sun..6=Sat.
     * Value is an intensity level: -1 = outside range (blank), 0 = no usage,
     * 1..4 = quartile buckets of non-zero days (GitHub-style relative shading).
     */
    cells: number[][];
    /** Month abbrev to print above each week column, or "" for none. */
    monthLabels: string[];
    /** The 3 quartile cutoffs (q1,q2,q3) used for bucketing, for reference. */
    thresholds: [number, number, number];
}

export interface SteakOptions {
    /** Render a specific calendar year; omit for the trailing 52 weeks. */
    year?: number;
    /** "Today" reference — injectable for tests. Defaults to now. */
    now?: Date;
}

/** Build the heatmap grid from a `dayKey -> tokens` map. */
export function buildSteakGrid(daily: Map<string, number>, opts: SteakOptions = {}): SteakGrid {
    const now = opts.now ?? new Date();

    // Range is expressed as [firstSunday .. lastDay] inclusive. Columns are
    // whole Sun..Sat weeks; days outside [lower..lastDay] render as blank.
    let firstSunday: Date;
    let lastDay: Date;
    let lower: Date; // earliest day that should render (>= firstSunday)
    if (opts.year !== undefined) {
        const jan1 = new Date(opts.year, 0, 1);
        lower = jan1;
        lastDay = new Date(opts.year, 11, 31);
        firstSunday = new Date(jan1);
        firstSunday.setDate(jan1.getDate() - jan1.getDay());
    } else {
        const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
        lastDay = today;
        const lastSunday = new Date(today);
        lastSunday.setDate(today.getDate() - today.getDay());
        firstSunday = new Date(lastSunday);
        firstSunday.setDate(lastSunday.getDate() - 52 * 7);
        lower = firstSunday;
    }

    const weeks = Math.round((lastDay.getTime() - firstSunday.getTime()) / (7 * 86_400_000)) + 1;
    const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    const isYear = opts.year !== undefined;

    // First pass: raw tokens per cell + collect non-zero values for bucketing.
    // Sentinel -1 = blank (no square). Days with no usage render as empty squares
    // (level 0) to keep the grid a solid rectangle. The exception is the trailing
    // default view, where genuinely future days are left blank so the graph ends
    // at "today" (GitHub-style). Year mode always draws the full year frame, with
    // not-yet-happened days shown as empties to fill in.
    const raw: number[][] = Array.from({ length: 7 }, () => new Array<number>(weeks).fill(-1));
    const nonZero: number[] = [];
    let totalTokens = 0;
    for (let c = 0; c < weeks; c++) {
        for (let r = 0; r < 7; r++) {
            const d = new Date(firstSunday);
            d.setDate(firstSunday.getDate() + c * 7 + r);
            const future = d.getTime() > today.getTime();
            if (future && !isYear) continue; // trailing view: leave future blank
            const counted = !future && d.getTime() >= lower.getTime() && d.getTime() <= lastDay.getTime();
            const tok = counted ? (daily.get(dayKey(d)) ?? 0) : 0;
            raw[r][c] = tok; // 0 for empty/future days, keeping the rectangle solid
            if (counted) totalTokens += tok;
            if (tok > 0) nonZero.push(tok);
        }
    }

    // Quartile thresholds over non-zero days — relative shading, so the graph
    // reads well whether the user burns 10k or 10M tokens a day.
    nonZero.sort((a, b) => a - b);
    // Nearest-rank over n-1 so the busiest day always clears q3 → top bucket.
    const q = (p: number) => (nonZero.length ? nonZero[Math.floor(p * (nonZero.length - 1))] : 0);
    const q1 = q(0.25);
    const q2 = q(0.5);
    const q3 = q(0.75);
    const level = (t: number): number => {
        if (t < 0) return -1;
        if (t === 0) return 0;
        if (t <= q1) return 1;
        if (t <= q2) return 2;
        if (t <= q3) return 3;
        return 4;
    };

    const cells = raw.map((row) => row.map(level));

    // Month labels: tag the column whose Sunday begins a new month.
    const monthOf = (c: number): number => {
        const sunday = new Date(firstSunday);
        sunday.setDate(firstSunday.getDate() + c * 7);
        return sunday.getMonth();
    };
    const monthLabels = new Array<string>(weeks).fill("");
    let prevMonth = -1;
    for (let c = 0; c < weeks; c++) {
        const m = monthOf(c);
        if (m !== prevMonth) {
            monthLabels[c] = MONTHS[m];
            prevMonth = m;
        }
    }
    // Drop 1-column partial months at either edge so their 3-char abbrevs don't
    // collide with a neighbor or clip off the grid (matches GitHub's stub-omit).
    if (weeks > 1 && monthLabels[0] && monthOf(0) !== monthOf(1)) monthLabels[0] = "";
    if (weeks > 1 && monthLabels[weeks - 1] && monthOf(weeks - 1) !== monthOf(weeks - 2)) {
        monthLabels[weeks - 1] = "";
    }

    return { totalTokens, weeks, cells, monthLabels, thresholds: [q1, q2, q3] };
}
