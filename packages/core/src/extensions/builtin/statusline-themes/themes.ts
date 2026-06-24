/**
 * Theme definitions + self-contained ANSI helpers for the statusline-themes
 * extension. Core has no chalk dependency, so colors are emitted as raw
 * truecolor escapes (the same 24-bit colors the rest of the app uses via
 * chalk.hex). A theme recolors the *already-rendered* status-line rows: we strip
 * the built-in's own ANSI first, then repaint the plain text our way.
 */

export type ThemeId =
    | "default"
    | "mono"
    | "matrix"
    | "ocean"
    | "sunset"
    | "synthwave"
    | "fire"
    | "rainbow"
    | "heat"
    | "neon"
    | "gold"
    | "cyber";

interface RGB {
    r: number;
    g: number;
    b: number;
}

type ThemeKind =
    | { kind: "off" } // leave the native colored render untouched
    | { kind: "solid"; color: RGB } // one flat color
    | { kind: "gradient"; stops: RGB[] } // left→right interpolation across 2+ stops
    | { kind: "rainbow" }; // per-character hue sweep

export interface Theme {
    id: ThemeId;
    label: string;
    description: string;
    spec: ThemeKind;
}

const ANSI_RE = /\x1b\[[0-9;]*m/g;
function stripAnsi(s: string): string {
    return s.replace(ANSI_RE, "");
}

function fg({ r, g, b }: RGB, text: string): string {
    return `\x1b[38;2;${r};${g};${b}m${text}\x1b[39m`;
}

function lerp(a: number, b: number, t: number): number {
    return Math.round(a + (b - a) * t);
}

function mix(from: RGB, to: RGB, t: number): RGB {
    return { r: lerp(from.r, to.r, t), g: lerp(from.g, to.g, t), b: lerp(from.b, to.b, t) };
}

/** Sample a multi-stop gradient at t ∈ [0,1]. Two stops = a plain lerp. */
function sampleStops(stops: RGB[], t: number): RGB {
    if (stops.length === 1) return stops[0];
    const clamped = Math.min(1, Math.max(0, t));
    const span = clamped * (stops.length - 1);
    const i = Math.min(stops.length - 2, Math.floor(span));
    return mix(stops[i], stops[i + 1], span - i);
}

/** HSL→RGB with s=1, l=0.6 — bright, saturated rainbow stops. */
function hue(h: number): RGB {
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

export const THEMES: Theme[] = [
    {
        id: "default",
        label: "default",
        description: "the native colors (agent orange, model cyan, cost green)",
        spec: { kind: "off" },
    },
    {
        id: "mono",
        label: "mono",
        description: "no color — plain monochrome text",
        spec: { kind: "solid", color: { r: 170, g: 170, b: 170 } },
    },
    {
        id: "matrix",
        label: "matrix",
        description: "all green, terminal-hacker vibes",
        spec: { kind: "solid", color: { r: 0, g: 255, b: 102 } },
    },
    {
        id: "ocean",
        label: "ocean",
        description: "blue → cyan gradient",
        spec: {
            kind: "gradient",
            stops: [
                { r: 36, g: 92, b: 255 },
                { r: 0, g: 224, b: 224 },
            ],
        },
    },
    {
        id: "sunset",
        label: "sunset",
        description: "orange → magenta gradient",
        spec: {
            kind: "gradient",
            stops: [
                { r: 255, g: 153, b: 51 },
                { r: 224, g: 32, b: 160 },
            ],
        },
    },
    {
        id: "synthwave",
        label: "synthwave",
        description: "magenta → cyan gradient (retro neon)",
        spec: {
            kind: "gradient",
            stops: [
                { r: 255, g: 41, b: 184 },
                { r: 41, g: 224, b: 255 },
            ],
        },
    },
    {
        id: "fire",
        label: "fire",
        description: "red → yellow gradient",
        spec: {
            kind: "gradient",
            stops: [
                { r: 255, g: 40, b: 0 },
                { r: 255, g: 214, b: 0 },
            ],
        },
    },
    {
        id: "rainbow",
        label: "rainbow",
        description: "every character a different hue 🌈",
        spec: { kind: "rainbow" },
    },
    {
        id: "heat",
        label: "heat",
        description: "green → yellow → red, the context-bar heatmap",
        spec: {
            kind: "gradient",
            // AKCodez context-bar palette (gist ffb420ba…): 0-50% green→yellow, 50-100% yellow→red.
            stops: [
                { r: 0, g: 200, b: 80 },
                { r: 220, g: 200, b: 0 },
                { r: 220, g: 40, b: 20 },
            ],
        },
    },
    {
        id: "neon",
        label: "neon",
        description: "yellow → magenta → cyan (AKCodez daily-driver)",
        spec: {
            kind: "gradient",
            // The daily-driver element colors: yellow repo, magenta model, cyan branch.
            stops: [
                { r: 255, g: 255, b: 0 },
                { r: 255, g: 0, b: 255 },
                { r: 0, g: 184, b: 220 },
            ],
        },
    },
    {
        id: "gold",
        label: "gold",
        description: "solid gold (#FFFF00)",
        spec: { kind: "solid", color: { r: 255, g: 255, b: 0 } },
    },
    {
        id: "cyber",
        label: "cyber",
        description: "solid cyan (#00B8DC)",
        spec: { kind: "solid", color: { r: 0, g: 184, b: 220 } },
    },
];

export const DEFAULT_THEME: ThemeId = "default";

export function getTheme(id: string | undefined): Theme {
    return THEMES.find((t) => t.id === id) ?? THEMES[0];
}

/**
 * Recolor one rendered row according to a theme. `off` returns the row
 * unchanged (keep native colors). Everything else strips the existing ANSI and
 * repaints the plain text. Width is preserved (ANSI adds no visible columns).
 */
export function applyTheme(line: string, theme: Theme): string {
    if (theme.spec.kind === "off") return line;
    const plain = stripAnsi(line);
    if (plain.length === 0) return line;

    if (theme.spec.kind === "solid") {
        return fg(theme.spec.color, plain);
    }

    // Per-character coloring for gradient / rainbow.
    const chars = [...plain];
    const n = Math.max(1, chars.length - 1);
    let out = "";
    for (let i = 0; i < chars.length; i++) {
        const t = i / n;
        const color = theme.spec.kind === "rainbow" ? hue(t * 320) : sampleStops(theme.spec.stops, t);
        out += fg(color, chars[i]);
    }
    return out;
}
