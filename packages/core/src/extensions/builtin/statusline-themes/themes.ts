/**
 * Theme definitions for the statusline-themes extension. A theme recolors the
 * *already-rendered* status-line rows (whatever the active layout produced): we
 * strip the existing ANSI first, then repaint the plain text our way. ANSI
 * helpers live in ./ansi so the layout code can share them.
 */
import { fg, hue, type RGB, sampleStops, stripAnsi } from "./ansi";

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
