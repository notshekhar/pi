/**
 * Everything that isn't session/model/agent/hook/settings management:
 * command IO (emit), /cost, /changelog, /hotkeys, /copy, /attach, /cwd,
 * /login, /logout, /quit, and the not-implemented stub.
 */
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import chalk from "chalk";
import { getCatalog, type CommandContext } from "@notshekhar/loop-core";
import type { AppDeps } from "../deps";
import type { AppState } from "../state";
import { readClipboardImageToFile } from "../clipboard-image";
import { startLogin, startLogout } from "../login-flow";
import { loadChangelogEntries } from "../../changelog";
import { resolveAvailableUpdate, runUpgrade } from "../../commands";

type MiscHandlers = Pick<
    CommandContext,
    | "emit"
    | "showCost"
    | "showChangelog"
    | "showHotkeys"
    | "copyLastAssistant"
    | "attachImage"
    | "exit"
    | "setCwd"
    | "startLogin"
    | "startLogout"
    | "updateApp"
    | "stub"
>;

export function createMiscHandlers(state: AppState, deps: AppDeps): MiscHandlers {
    const { tui, history, tracker, editor, selectOnce, promptOnce, cleanExit } = deps;
    const loginDeps = { tui, history, selectOnce, promptOnce };

    return {
        emit(event, data) {
            if (event === "help" || event === "error") history.addSystem(String(data ?? ""));
            if (event === "inject-prompt") state.pendingInjection = String(data ?? "");
            if (event === "inject-skill") {
                const text = String(data ?? "");
                if (text && editor.onSubmit) void editor.onSubmit(text);
            }
            tui.requestRender();
        },
        showCost() {
            const s = tracker.sessionBreakdown();
            const st = tracker.stats(state.cwd);
            const fmtUsd = (v: number) => `$${v.toFixed(4)}`;
            const fmtTok = (n: number) =>
                n >= 1_000_000
                    ? `${(n / 1_000_000).toFixed(1)}M`
                    : n >= 1_000
                      ? `${(n / 1_000).toFixed(1)}k`
                      : String(n);
            const row = (label: string, usd: number, extra = "") =>
                history.addSystem(
                    `  ${chalk.dim(label.padEnd(14))}${chalk.cyan(fmtUsd(usd).padStart(10))}${extra ? `   ${chalk.dim(extra)}` : ""}`,
                );

            history.addSystem(chalk.bold("cost"));
            row(
                "session",
                s.usd,
                `in:${fmtTok(s.inputTokens)} out:${fmtTok(s.outputTokens)} cache:${fmtTok(s.cachedInputTokens)}`,
            );
            row("directory", st.cwdUsd, state.cwd.replace(process.env.HOME ?? "", "~"));
            row("today", st.todayUsd);
            row("last 7 days", st.last7Usd);
            row("this month", st.monthUsd);
            row("lifetime", st.lifetimeUsd);
            const providers = Object.entries(st.byProvider)
                .filter(([, v]) => v > 0)
                .sort((a, b) => b[1] - a[1]);
            for (const [p, v] of providers) row(`  ${p}`, v);
            // Daily/cwd buckets are new — older lifetime spend predates them.
            if (st.lifetimeUsd > 0 && st.monthUsd === 0 && st.cwdUsd === 0) {
                history.addSystem(
                    chalk.dim("  (time/directory tracking starts now — lifetime includes earlier spend)"),
                );
            }
            tui.requestRender();
        },
        showChangelog() {
            const entries = loadChangelogEntries();
            if (entries.length === 0) {
                history.addSystem("no changelog entries found");
                tui.requestRender();
                return;
            }
            history.addMarkdown(entries.map((e) => e.content).join("\n\n"));
            tui.requestRender();
        },
        showHotkeys() {
            const lines = [
                "Enter           submit",
                "Shift+Enter     newline",
                "Tab             cycle agent (applies completion when popup open)",
                "Shift+Tab       cycle agent (always)",
                "@ / #           file completion while typing",
                "Up / Down       history",
                "Esc             abort current turn",
                "Ctrl+C          abort, twice to quit",
                "Ctrl+D          quit (empty)",
                "Ctrl+L          clear screen",
                "Ctrl+P          cycle scoped models",
            ];
            for (const l of lines) history.addSystem(l);
            tui.requestRender();
        },
        async copyLastAssistant() {
            const entries = state.session?.entries() ?? [];
            const last = [...entries]
                .reverse()
                .find((e) => e.type === "message" && (e as { role?: string }).role === "assistant");
            if (!last) {
                history.addSystem("no assistant message to copy");
                tui.requestRender();
                return;
            }
            const text = String((last as { content: unknown }).content ?? "");
            try {
                const child = spawn("pbcopy");
                child.stdin.write(text);
                child.stdin.end();
                history.addSystem(`copied ${text.length} chars to clipboard`);
            } catch {
                history.addSystem(`pbcopy unavailable. content length: ${text.length}`);
            }
            tui.requestRender();
        },
        async attachImage(givenPath) {
            const cat = await getCatalog();
            const info = cat[state.modelId];
            if (info && Array.isArray(info.modalities) && !info.modalities.includes("image")) {
                history.addSystem(
                    chalk.yellow(`${state.modelId} does not accept images. Pick a vision model via /model first.`),
                );
                tui.requestRender();
                return;
            }
            let path = givenPath;
            if (!path) {
                path = readClipboardImageToFile() ?? undefined;
                if (!path) {
                    history.addSystem(
                        chalk.yellow(
                            "no image in clipboard. Copy one (Cmd+C on a Finder file or screenshot), use `/attach <path>`, or press Ctrl+I to pick a file.",
                        ),
                    );
                    tui.requestRender();
                    return;
                }
            }
            if (!existsSync(path)) {
                history.addError(`file not found: ${path}`);
                tui.requestRender();
                return;
            }
            const token = `[image:${path}]`;
            const current = editor.getText?.() ?? "";
            const sep = current && !current.endsWith(" ") ? " " : "";
            editor.setText?.(`${current}${sep}${token} `);
            tui.requestRender();
        },
        exit() {
            cleanExit(0);
        },
        setCwd(p) {
            state.cwd = p;
            history.addSystem(`cwd → ${p}`);
            tui.requestRender();
        },
        startLogin(target) {
            return startLogin(loginDeps, target);
        },
        startLogout(target) {
            return startLogout(loginDeps, target);
        },
        async updateApp() {
            if (state.busy) {
                history.addSystem("busy; finish or abort current turn first");
                tui.requestRender();
                return;
            }
            const version = deps.version ?? "0.0.0";
            deps.showWorking("Checking for updates");
            const latest = await resolveAvailableUpdate(version);
            deps.hideWorking();
            if (!latest) {
                history.addSystem(`already up to date (v${version})`);
                tui.requestRender();
                return;
            }
            history.addSystem(`updating v${version} → ${latest}…`);
            tui.requestRender();
            // Hand the terminal to the installer: stop rendering, restore
            // console, let the platform install script take over. runUpgrade
            // exits the process when the installer finishes.
            tui.stop();
            deps.restoreConsole();
            await runUpgrade(version);
        },
        stub(name) {
            history.addSystem(chalk.yellow(`/${name} not implemented yet`));
            tui.requestRender();
        },
    };
}
