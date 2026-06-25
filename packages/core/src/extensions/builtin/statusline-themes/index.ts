/**
 * statusline-themes — fully customize the status line under the input box along
 * two independent axes:
 *   • layout (`/statusline`) — the *structure*: which info appears and how it's
 *     arranged (native, compact bar, vitals dashboard, powerline, minimal, bar).
 *   • color  (`/statuscolor`) — a theme that recolors whatever the layout drew
 *     (matrix/ocean/sunset/heat/neon/rainbow…).
 * Both persist via api.settings.setOwn and apply live on every repaint through
 * api.statusLine.transform — the layout transform runs first, then the color
 * transform recolors its output, so the two compose freely. The "vitals" layout
 * reads CPU/mem/battery from a background sampler that only runs while it's the
 * active layout.
 */
import type { LoopAPI } from "../../api";
import { DEFAULT_LAYOUT, getLayout, LAYOUTS, type LayoutId } from "./layouts";
import { SystemSampler } from "./system";
import { applyTheme, DEFAULT_THEME, getTheme, THEMES, type ThemeId } from "./themes";

// Handle to the running vitals sampler, shared between activate/deactivate (the
// two methods don't share a closure). Only one instance of a built-in runs at a
// time, so a module-level handle is safe.
let sampler: SystemSampler | null = null;

export default {
    activate(api: LoopAPI) {
        const sys = new SystemSampler();
        sampler = sys;
        const layoutId = (): LayoutId => getLayout(api.settings.getOwn("layout", DEFAULT_LAYOUT)).id;
        const themeId = (): ThemeId => getTheme(api.settings.getOwn("theme", DEFAULT_THEME)).id;

        // Start/stop the vitals sampler to match the active layout, so we never
        // probe the OS unless a layout actually shows CPU/mem/battery. While it
        // runs, each 1s tick repaints the status line so the clock and CPU/mem
        // stay live (they change with no user action, which otherwise wouldn't
        // trigger a render).
        const syncSampler = () => {
            if (getLayout(layoutId()).needsVitals) sys.start(() => api.statusLine.refresh());
            else sys.stop();
        };
        syncSampler();

        api.extension.setStatus(() => {
            const l = layoutId();
            const t = themeId();
            return t === "default" ? l : `${l} · ${t}`;
        });

        // 1) Layout: replace the rendered rows with the active preset's output.
        //    "native" (render === null) returns void, leaving the built-in render.
        api.statusLine.transform((lines, ctx) => {
            const layout = getLayout(layoutId());
            if (!layout.render) return; // native — untouched
            try {
                return layout.render(ctx, sys.get()) ?? undefined;
            } catch {
                return; // a broken layout must never blank the status line
            }
        });

        // 2) Color: recolor whatever rows axis 1 produced. "default" is a no-op.
        api.statusLine.transform((lines) => {
            const theme = getTheme(api.settings.getOwn("theme", DEFAULT_THEME));
            if (theme.spec.kind === "off") return;
            return lines.map((l) => applyTheme(l, theme));
        });

        api.commands.register({
            name: "statusline",
            description: "Pick a status line layout (opens a menu, or /statusline <name>)",
            handler: async (ctx, args) => {
                const arg = args.trim().toLowerCase();
                if (arg) {
                    const match = LAYOUTS.find((l) => l.id === arg);
                    if (!match) {
                        ctx.emit("error", `unknown layout "${arg}". options: ${LAYOUTS.map((l) => l.id).join(" | ")}`);
                        return;
                    }
                    api.settings.setOwn("layout", match.id);
                    syncSampler();
                    ctx.emit("help", `status line layout: ${match.id}`);
                    return;
                }
                const active = layoutId();
                const items = LAYOUTS.map((l) => ({
                    value: l.id,
                    label: l.id === active ? `${l.label} (current)` : l.label,
                    description: `${l.description}  →  ${l.sample}`,
                }));
                const pick = await api.ui.search(items, "Status line layout (type to filter, Esc to close)", {
                    initialIndex: Math.max(
                        0,
                        LAYOUTS.findIndex((l) => l.id === active),
                    ),
                });
                if (pick) {
                    api.settings.setOwn("layout", pick.value as LayoutId);
                    syncSampler();
                    ctx.emit("help", `status line layout: ${pick.value}`);
                }
            },
        });

        api.commands.register({
            name: "statuscolor",
            description: "Pick a status line color theme (opens a menu, or /statuscolor <name>)",
            handler: async (ctx, args) => {
                const arg = args.trim().toLowerCase();
                if (arg) {
                    const match = THEMES.find((t) => t.id === arg);
                    if (!match) {
                        ctx.emit("error", `unknown theme "${arg}". options: ${THEMES.map((t) => t.id).join(" | ")}`);
                        return;
                    }
                    api.settings.setOwn("theme", match.id);
                    ctx.emit("help", `status line color: ${match.id}`);
                    return;
                }
                const active = themeId();
                const items = THEMES.map((t) => ({
                    value: t.id,
                    label: t.id === active ? `${t.label} (current)` : t.label,
                    description: t.description,
                }));
                const pick = await api.ui.search(items, "Status line color (type to filter, Esc to close)", {
                    initialIndex: Math.max(
                        0,
                        THEMES.findIndex((t) => t.id === active),
                    ),
                });
                if (pick) {
                    api.settings.setOwn("theme", pick.value as ThemeId);
                    ctx.emit("help", `status line color: ${pick.value}`);
                }
            },
        });
    },

    deactivate() {
        sampler?.stop();
        sampler = null;
    },
};
