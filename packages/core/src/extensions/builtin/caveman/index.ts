/**
 * Caveman — the ultra-compressed communication skill
 * (github.com/juliusbrussee/caveman) as a loop extension. It injects a "respond
 * terse like smart caveman" persona into the system prompt, cutting token usage
 * while keeping technical substance. Intensity levels: lite / full / ultra plus
 * the classical-Chinese wenyan variants, and an off switch.
 *
 * Upstream attaches this as a SessionStart message; in loop the native seam is
 * `onSystemPrompt` (a persistent per-turn persona), with a `/caveman` command to
 * switch modes and the "stop caveman" phrase to disable.
 */
import type { LoopAPI } from "../../api";
import { buildInstructions, DEFAULT_MODE, isDeactivationCommand, MODES, normalizeMode, type Mode } from "./instructions";

export default {
    activate(api: LoopAPI) {
        const getMode = (): Mode => normalizeMode(api.settings.getOwn("mode", DEFAULT_MODE)) ?? DEFAULT_MODE;

        api.turn.use({
            onSystemPrompt(prompt) {
                const mode = getMode();
                if (mode === "off") return;
                return `${prompt}\n\n${buildInstructions(mode)}`;
            },
            onBeforeTurn(ctx) {
                if (isDeactivationCommand(ctx.input)) api.settings.setOwn("mode", "off");
            },
        });

        api.commands.register({
            name: "caveman",
            description: "Terse caveman mode: /caveman lite|full|ultra|wenyan-full|off (no arg shows status)",
            handler: (ctx, args) => {
                const arg = args.trim().toLowerCase();
                if (!arg || arg === "status") {
                    ctx.emit("help", `caveman mode: ${getMode()} (options: ${MODES.join(" | ")})`);
                    return;
                }
                const mode = normalizeMode(arg);
                if (!mode) {
                    ctx.emit("error", `unknown caveman mode "${arg}". options: ${MODES.join(" | ")}`);
                    return;
                }
                api.settings.setOwn("mode", mode);
                ctx.emit("help", mode === "off" ? "caveman off — normal mode." : `caveman ${mode} — me talk short now.`);
            },
        });
    },
};
