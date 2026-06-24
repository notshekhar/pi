/**
 * Ponytail — the "lazy senior dev" skill (github.com/DietrichGebert/ponytail)
 * as a loop extension. It injects a write-less-code persona into the system
 * prompt every turn, with intensity levels (lite / full / ultra) and an off
 * switch. Faithful port: same ladder, same rules, same SKILL.md text.
 *
 * Surfaces it exercises: a turn middleware (`onSystemPrompt`) to shape the
 * prompt, a slash command (`/ponytail`) to switch modes, persisted settings
 * (`getOwn`/`setOwn`), and `onBeforeTurn` for the "stop ponytail" phrase.
 */
import type { LoopAPI } from "../../api";
import {
    buildInstructions,
    DEFAULT_MODE,
    isDeactivationCommand,
    MODES,
    normalizeMode,
    type Mode,
} from "./instructions";

export default {
    activate(api: LoopAPI) {
        const getMode = (): Mode => normalizeMode(api.settings.getOwn("mode", DEFAULT_MODE)) ?? DEFAULT_MODE;
        // Surface the active mode in the startup banner / panel so the
        // system-prompt injection is visible, not silent.
        api.extension.setStatus(() => getMode());

        // Inject the persona while active. Scoped per-turn; default agent and any
        // custom agent get it (a planning/analyst agent that wants it can too).
        api.turn.use({
            onSystemPrompt(prompt) {
                const mode = getMode();
                if (mode === "off") return;
                return `${prompt}\n\n${buildInstructions(mode)}`;
            },
            // "stop ponytail" / "normal mode" as a standalone message turns it off.
            onBeforeTurn(ctx) {
                if (isDeactivationCommand(ctx.input)) api.settings.setOwn("mode", "off");
            },
        });

        // /ponytail [lite|full|ultra|off|status] — switch intensity or show state.
        api.commands.register({
            name: "ponytail",
            description: "Lazy-senior-dev mode: /ponytail lite|full|ultra|off (no arg shows status)",
            handler: (ctx, args) => {
                const arg = args.trim().toLowerCase();
                if (!arg || arg === "status") {
                    ctx.emit("help", `ponytail mode: ${getMode()} (options: ${MODES.join(" | ")})`);
                    return;
                }
                const mode = normalizeMode(arg);
                if (!mode) {
                    ctx.emit("error", `unknown ponytail mode "${arg}". options: ${MODES.join(" | ")}`);
                    return;
                }
                api.settings.setOwn("mode", mode);
                ctx.emit(
                    "help",
                    mode === "off" ? "ponytail off — normal mode." : `ponytail ${mode} — writing the lazy version.`,
                );
            },
        });
    },
};
