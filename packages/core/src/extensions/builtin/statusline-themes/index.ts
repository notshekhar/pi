/**
 * statusline-themes — recolor the status line under the input box. Enabling it
 * adds `/statusline`, which opens a menu (via api.ui) to pick from a set of color
 * themes; the choice is persisted (api.settings.setOwn) and applied live through
 * api.statusLine.transform on every repaint. `/statusline <name>` switches
 * directly without the menu. Showcases the status-line + UI extension seams.
 */
import type { LoopAPI } from "../../api";
import { applyTheme, DEFAULT_THEME, getTheme, THEMES, type ThemeId } from "./themes";

export default {
    activate(api: LoopAPI) {
        const currentId = (): ThemeId => getTheme(api.settings.getOwn("theme", DEFAULT_THEME)).id;
        api.extension.setStatus(() => currentId());

        // Live recolor: reads the saved theme each repaint, so switching takes
        // effect immediately (no reload). "default" is a no-op (native colors).
        api.statusLine.transform((lines) => {
            const theme = getTheme(api.settings.getOwn("theme", DEFAULT_THEME));
            if (theme.spec.kind === "off") return; // leave lines untouched
            return lines.map((l) => applyTheme(l, theme));
        });

        const apply = (id: ThemeId, ctx: { emit(event: string, data?: unknown): void }) => {
            api.settings.setOwn("theme", id);
            ctx.emit("help", `status line theme: ${id}`);
        };

        api.commands.register({
            name: "statusline",
            description: "Pick a status line color theme (opens a menu, or /statusline <name>)",
            handler: async (ctx, args) => {
                const arg = args.trim().toLowerCase();
                // Direct switch: /statusline <name>.
                if (arg) {
                    const match = THEMES.find((t) => t.id === arg);
                    if (!match) {
                        ctx.emit("error", `unknown theme "${arg}". options: ${THEMES.map((t) => t.id).join(" | ")}`);
                        return;
                    }
                    apply(match.id, ctx);
                    return;
                }
                // Interactive menu, with the current theme pre-selected.
                const active = currentId();
                const items = THEMES.map((t) => ({
                    value: t.id,
                    label: t.id === active ? `${t.label} (current)` : t.label,
                    description: t.description,
                }));
                const initialIndex = Math.max(
                    0,
                    THEMES.findIndex((t) => t.id === active),
                );
                const pick = await api.ui.search(items, "Status line theme (type to filter, Esc to close)", {
                    initialIndex,
                });
                if (pick) apply(pick.value as ThemeId, ctx);
            },
        });
    },
};
