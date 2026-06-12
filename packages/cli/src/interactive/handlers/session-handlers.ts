/**
 * Session lifecycle: /new, /clear, /compact, /resume, /session, /name,
 * /export, /import.
 */
import { readFileSync, writeFileSync } from "node:fs";
import type { SelectItem } from "@notshekhar/pi-tui";
import chalk from "chalk";
import {
    CompactAbortedError,
    clearReadRegistry,
    runCompact,
    runHooks,
    setProjectModel,
    settingsStore,
    type CommandContext,
} from "@notshekhar/pi-core";
import type { AppDeps } from "../deps";
import type { AppState } from "../state";
import { renderSessionBranch } from "../replay";

type SessionHandlers = Pick<
    CommandContext,
    | "newSession"
    | "clearScreen"
    | "manualCompact"
    | "showSessions"
    | "showSessionInfo"
    | "setSessionName"
    | "exportSession"
    | "importSession"
>;

export function createSessionHandlers(state: AppState, deps: AppDeps): SessionHandlers {
    const {
        tui,
        history,
        footer,
        tracker,
        manager,
        queuedMessages,
        refreshFooter,
        renderPending,
        showWorking,
        hideWorking,
        selectOnce,
    } = deps;

    return {
        async newSession() {
            state.session = null;
            footer.setSession("unsaved");
            tracker.reset();
            clearReadRegistry();
            state.latestContextTokens = 0;
            refreshFooter();
            queuedMessages.length = 0;
            renderPending();
            history.reset();
            history.addSystem("new session unsaved");
            tui.requestRender();
        },
        clearScreen() {
            process.stdout.write("\x1b[3J\x1b[2J\x1b[H");
            tracker.reset();
            state.latestContextTokens = 0;
            refreshFooter();
            history.reset();
            tui.invalidate();
            tui.requestRender(true);
        },
        async manualCompact() {
            if (!state.session) {
                history.addSystem("nothing to compact");
                tui.requestRender();
                return;
            }
            if (state.busy) {
                history.addSystem("busy; finish or abort current turn first");
                tui.requestRender();
                return;
            }
            state.busy = true;
            showWorking("Compacting");
            tui.requestRender();
            try {
                // PreCompact is informational for watchers — block is ignored.
                await runHooks(
                    "PreCompact",
                    "manual",
                    { session_id: state.session.id, transcript_path: state.session.path, trigger: "manual" },
                    state.cwd,
                );
                const result = await runCompact({
                    session: state.session,
                    modelId: state.modelId,
                    keepTurns: 0,
                    abortSignal: state.abort.signal,
                });
                if (result.summary) {
                    history.addCompactionSummary(result.summary, result.tokensBefore);
                } else {
                    history.addSystem("nothing to compact");
                }
            } catch (err) {
                if (err instanceof CompactAbortedError || state.abort.signal.aborted) {
                    history.addSystem("compact aborted");
                } else {
                    history.addError((err as Error).message);
                }
            } finally {
                state.busy = false;
                hideWorking();
            }
            tui.requestRender();
        },
        async showSessions() {
            const sessions = manager.list(state.cwd);
            if (sessions.length === 0) {
                history.addSystem("no sessions in this cwd");
                tui.requestRender();
                return;
            }
            const items: SelectItem[] = sessions.map((s) => ({
                value: s.path,
                label: `${s.id.slice(0, 12)}  ${s.model || "?"}`,
                description: `${new Date(s.mtime).toLocaleString()}  ·  ${s.firstUserMessage?.slice(0, 80) ?? "(no messages)"}${s.source === "pi" ? "  [pi]" : ""}`,
            }));
            const pick = await selectOnce(items);
            if (!pick) return;
            try {
                const selectedPath = pick.value;
                state.session = await manager.open(pick.value);
                if (state.session.info.model) {
                    state.modelId = state.session.info.model;
                    settingsStore.set("defaultModel", state.modelId);
                    setProjectModel(state.cwd, state.modelId);
                    footer.setModel(state.modelId);
                }
                footer.setSession(state.session.id);
                // Restore cost/usage/ctx from the resumed transcript.
                state.latestContextTokens = tracker.seedFromSession(state.session).ctxTokens;
                refreshFooter();
                history.reset();
                if (state.session.path !== selectedPath) {
                    history.addSystem(`resumed fork ${state.session.id}`);
                    history.addSystem(
                        chalk.dim(
                            "selected legacy session was forked; new messages and compactions save to this session",
                        ),
                    );
                } else {
                    history.addSystem(`resumed session ${state.session.id}`);
                }
                renderSessionBranch(state.session, history, state.modelId);
            } catch (err) {
                history.addError(`open failed: ${(err as Error).message}`);
            }
            tui.requestRender();
        },
        showSessionInfo() {
            const s = tracker.sessionBreakdown();
            history.addSystem(`session id   ${state.session?.id ?? "unsaved"}`);
            history.addSystem(`model        ${state.modelId}`);
            history.addSystem(`provider     ${state.provider}`);
            history.addSystem(`thinking     ${state.thinkingLevel}`);
            history.addSystem(`cwd          ${state.cwd}`);
            history.addSystem(`tokens       in:${s.inputTokens} out:${s.outputTokens} cache:${s.cachedInputTokens}`);
            history.addSystem(`cost (sess)  $${s.usd.toFixed(4)}`);
            tui.requestRender();
        },
        setSessionName(name) {
            if (!state.session) {
                history.addSystem("session is unsaved");
                tui.requestRender();
                return;
            }
            settingsStore.set(`sessionName.${state.session.id}`, name);
            history.addSystem(`session name → ${name}`);
            tui.requestRender();
        },
        async exportSession(target) {
            if (!state.session) {
                history.addSystem("session is unsaved");
                tui.requestRender();
                return;
            }
            const out = target ?? `${state.session.id}.jsonl`;
            const entries = state.session.entries();
            const content = entries.map((e) => JSON.stringify(e)).join("\n");
            writeFileSync(out, content);
            history.addSystem(`exported to ${out}`);
            tui.requestRender();
        },
        async importSession(path) {
            try {
                const lines = readFileSync(path, "utf8").split("\n").filter(Boolean);
                const ns = await manager.create({ cwd: state.cwd, provider: state.provider, model: state.modelId });
                for (const line of lines) {
                    try {
                        await ns.append(JSON.parse(line));
                    } catch {}
                }
                state.session = ns;
                footer.setSession(state.session.id);
                history.addSystem(`imported ${lines.length} entries → session ${state.session.id}`);
            } catch (err) {
                history.addError(`import failed: ${(err as Error).message}`);
            }
            tui.requestRender();
        },
    };
}
