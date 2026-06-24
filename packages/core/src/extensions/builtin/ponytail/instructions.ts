/**
 * Builds the ponytail system-prompt instructions from the embedded skill body,
 * filtered to the active intensity. Mirrors ponytail's own
 * `filterSkillBodyForMode`: the only mode-specific lines are the intensity-table
 * rows (keyed lite/full/ultra); every other line is a rule kept verbatim. Pure
 * (body in, string out) so it's trivial to reason about and reuse.
 */
import { PONYTAIL_SKILL } from "./skill-text";

export type Mode = "off" | "lite" | "full" | "ultra";
export const MODES: Mode[] = ["off", "lite", "full", "ultra"];
export const DEFAULT_MODE: Mode = "full";

export function normalizeMode(value: unknown): Mode | null {
    const m = typeof value === "string" ? value.trim().toLowerCase() : "";
    return (MODES as string[]).includes(m) ? (m as Mode) : null;
}

/** "stop ponytail" / "normal mode" as a standalone message turns ponytail off. */
export function isDeactivationCommand(text: string): boolean {
    const t = text
        .trim()
        .toLowerCase()
        .replace(/[.!?\s]+$/, "");
    return t === "stop ponytail" || t === "normal mode";
}

/** Drop intensity-table rows that aren't the active mode; keep everything else. */
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
            return true;
        })
        .join("\n");
}

/** Full instruction block to inject into the system prompt for `mode`. */
export function buildInstructions(mode: Mode): string {
    return `PONYTAIL MODE ACTIVE — level: ${mode}\n\n${filterForMode(PONYTAIL_SKILL, mode)}`;
}
