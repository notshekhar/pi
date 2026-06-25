/**
 * Self-contained truecolor ANSI helpers shared by the layout + theme code. Core
 * has no chalk dependency, so colors are emitted as raw 24-bit escapes (the same
 * colors the rest of the app produces via chalk.hex). Kept tiny and dependency
 * free so the built-in stays self-contained for `bun --compile`.
 */

export interface RGB {
    r: number;
    g: number;
    b: number;
}

const ANSI_RE = /\x1b\[[0-9;]*m/g;

export function stripAnsi(s: string): string {
    return s.replace(ANSI_RE, "");
}

/** Visible width of a string, ignoring ANSI color escapes. */
export function ansiLen(s: string): number {
    return stripAnsi(s).length;
}

export function fg({ r, g, b }: RGB, text: string): string {
    return `\x1b[38;2;${r};${g};${b}m${text}\x1b[39m`;
}

export function bg({ r, g, b }: RGB, text: string): string {
    return `\x1b[48;2;${r};${g};${b}m${text}\x1b[49m`;
}

export function bold(text: string): string {
    return `\x1b[1m${text}\x1b[22m`;
}

export function dim(text: string): string {
    return `\x1b[2m${text}\x1b[22m`;
}

function lerp(a: number, b: number, t: number): number {
    return Math.round(a + (b - a) * t);
}

export function mix(from: RGB, to: RGB, t: number): RGB {
    return { r: lerp(from.r, to.r, t), g: lerp(from.g, to.g, t), b: lerp(from.b, to.b, t) };
}

/** Sample a multi-stop gradient at t ∈ [0,1]. Two stops = a plain lerp. */
export function sampleStops(stops: RGB[], t: number): RGB {
    if (stops.length === 1) return stops[0];
    const clamped = Math.min(1, Math.max(0, t));
    const span = clamped * (stops.length - 1);
    const i = Math.min(stops.length - 2, Math.floor(span));
    return mix(stops[i], stops[i + 1], span - i);
}

/** HSL→RGB with s=1, l=0.6 — bright, saturated rainbow stops. */
export function hue(h: number): RGB {
    const s = 1;
    const l = 0.6;
    const c = (1 - Math.abs(2 * l - 1)) * s;
    const hp = (((h % 360) + 360) % 360) / 60;
    const x = c * (1 - Math.abs((hp % 2) - 1));
    let r = 0;
    let g = 0;
    let b = 0;
    if (hp < 1) [r, g, b] = [c, x, 0];
    else if (hp < 2) [r, g, b] = [x, c, 0];
    else if (hp < 3) [r, g, b] = [0, c, x];
    else if (hp < 4) [r, g, b] = [0, x, c];
    else if (hp < 5) [r, g, b] = [x, 0, c];
    else [r, g, b] = [c, 0, x];
    const m = l - c / 2;
    return { r: Math.round((r + m) * 255), g: Math.round((g + m) * 255), b: Math.round((b + m) * 255) };
}

/** A small named palette so layouts read declaratively. */
export const COLORS = {
    text: { r: 205, g: 205, b: 205 },
    muted: { r: 130, g: 130, b: 130 },
    faint: { r: 95, g: 95, b: 95 },
    cyan: { r: 56, g: 199, b: 222 },
    green: { r: 80, g: 200, b: 120 },
    yellow: { r: 224, g: 196, b: 64 },
    orange: { r: 224, g: 153, b: 86 },
    red: { r: 224, g: 72, b: 72 },
    magenta: { r: 214, g: 96, b: 184 },
    blue: { r: 92, g: 148, b: 255 },
} satisfies Record<string, RGB>;

/** Green → yellow → red, sampled by a 0..1 ratio. The usage heatmap. */
export function heat(ratio: number): RGB {
    return sampleStops([COLORS.green, COLORS.yellow, COLORS.red], Math.min(1, Math.max(0, ratio)));
}

/**
 * A fixed-width progress bar. `ratio` ∈ [0,1] fills left→right. Returns the
 * plain glyphs (no color); callers wrap segments in color as they like. The last
 * partial cell uses a fractional block so short bars still read smoothly.
 */
export function barCells(ratio: number, width: number): { filled: string; empty: string } {
    const r = Math.min(1, Math.max(0, ratio));
    const exact = r * width;
    const full = Math.floor(exact);
    const partials = " ▏▎▍▌▋▊▉";
    const frac = partials[Math.round((exact - full) * 8)] ?? "";
    let filled = "█".repeat(full);
    let used = full;
    if (full < width && frac && frac !== " ") {
        filled += frac;
        used += 1;
    }
    const empty = "░".repeat(Math.max(0, width - used));
    return { filled, empty };
}
