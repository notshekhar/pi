/**
 * Builds caveman's system-prompt instructions from the embedded skill body,
 * filtered to the active intensity. Mode-specific lines are the intensity-table
 * rows and the worked-example bullets (both keyed by a mode name); everything
 * else is kept verbatim. Pure (mode in, string out).
 */
import { CAVEMAN_SKILL } from "./skill-text";

export type Mode = "off" | "lite" | "full" | "ultra" | "wenyan-lite" | "wenyan-full" | "wenyan-ultra";
export const MODES: Mode[] = ["off", "lite", "full", "ultra", "wenyan-lite", "wenyan-full", "wenyan-ultra"];
export const DEFAULT_MODE: Mode = "full";

export function normalizeMode(value: unknown): Mode | null {
    const m = typeof value === "string" ? value.trim().toLowerCase() : "";
    return (MODES as string[]).includes(m) ? (m as Mode) : null;
}

/** "stop caveman" / "normal mode" as a standalone message turns caveman off. */
export function isDeactivationCommand(text: string): boolean {
    const t = text
        .trim()
        .toLowerCase()
        .replace(/[.!?\s]+$/, "");
    return t === "stop caveman" || t === "normal mode";
}

/**
 * Keep only the active mode's intensity-table row and example bullets; drop the
 * other modes'. A line whose leading label isn't a mode name is a normal rule
 * and is kept verbatim.
 */
function filterForMode(body: string, mode: Mode): string {
    const withoutFrontmatter = body.replace(/^---[\s\S]*?---\s*/, "");
    return withoutFrontmatter
        .split(/\r?\n/)
        .filter((line) => {
            const tableLabel = line.match(/^\|\s*\*\*(.+?)\*\*\s*\|/);
            if (tableLabel) {
                const labelMode = normalizeMode(tableLabel[1].trim());
                if (labelMode) return labelMode === mode;
            }
            const exampleLabel = line.match(/^-\s*([^:]+):\s*/);
            if (exampleLabel) {
                const labelMode = normalizeMode(exampleLabel[1].trim());
                if (labelMode) return labelMode === mode;
            }
            return true;
        })
        .join("\n");
}

/** Full instruction block to inject into the system prompt for `mode`. */
export function buildInstructions(mode: Mode): string {
    return `CAVEMAN MODE ACTIVE — level: ${mode}\n\n${filterForMode(CAVEMAN_SKILL, mode)}`;
}
