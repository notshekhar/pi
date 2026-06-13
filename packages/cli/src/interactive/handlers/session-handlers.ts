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
        searchOnce,
        promptOnce,
    } = deps;

    /** "today 10:49 PM", "yesterday 9:12 AM", else locale date — keeps the
     * list scannable and makes "today"/"yesterday" searchable terms. */
    const formatSessionTime = (mtime: number): string => {
        const d = new Date(mtime);
        const time = d.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
        const startOfDay = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
        const today = startOfDay(new Date());
        if (d.getTime() >= today) return `today ${time}`;
        if (d.getTime() >= today - 86_400_000) return `yesterday ${time}`;
        return d.toLocaleString(undefined, { month: "numeric", day: "numeric", year: "numeric" }) + ` ${time}`;
    };

    // Abort a turn still streaming so /new and /clear don't leave it running
    // against the old session — the agent would keep appending to the cleared
    // session and burning tokens. Mirrors the Esc/Ctrl+C abort in input-handler.
    const abortActiveTurn = () => {
        if (!state.busy) return;
        state.abort.abort();
        state.abort = new AbortController();
        state.busy = false;
        hideWorking();
    };

    return {
        async newSession() {
            abortActiveTurn();
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
            abortActiveTurn();
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

            // Date buckets: first row cycles through them; type-to-search
            // (searchOnce) filters the rest by id/model/date/first message.
            const DAY = 86_400_000;
            const now = new Date();
            const todayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
            const dateFilters: Array<{ label: string; test: (mtime: number) => boolean }> = [
                { label: "all", test: () => true },
                { label: "today", test: (t) => t >= todayStart },
                { label: "yesterday", test: (t) => t >= todayStart - DAY && t < todayStart },
                { label: "last 7 days", test: (t) => t >= todayStart - 6 * DAY },
                { label: "last 30 days", test: (t) => t >= todayStart - 29 * DAY },
            ];
            const FILTER_ROW = "\x00date-filter";
            let filterIndex = 0;

            let pick: SelectItem | null;
            while (true) {
                const filter = dateFilters[filterIndex];
                const filtered = sessions.filter((s) => filter.test(s.mtime));
                const items: SelectItem[] = [
                    {
                        value: FILTER_ROW,
                        label: `⏷ date: ${filter.label}`,
                        description: "Enter cycles · all → today → yesterday → last 7 days → last 30 days",
                    },
                    ...filtered.map((s) => ({
                        value: s.path,
                        label: s.name
                            ? `${s.name}  ·  ${s.id.slice(0, 12)}`
                            : `${s.id.slice(0, 12)}  ${s.model || "?"}`,
                        description: `${formatSessionTime(s.mtime)}  ·  ${s.firstUserMessage?.slice(0, 80) ?? "(no messages)"}${s.source === "pi" ? "  [pi]" : ""}`,
                    })),
                ];
                pick = await searchOnce(items, `Resume session · ${filtered.length}/${sessions.length}`);
                if (!pick) return;
                if (pick.value === FILTER_ROW) {
                    filterIndex = (filterIndex + 1) % dateFilters.length;
                    continue;
                }
                break;
            }
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
            if (state.session?.getName()) history.addSystem(`name         ${state.session.getName()}`);
            history.addSystem(`model        ${state.modelId}`);
            history.addSystem(`provider     ${state.provider}`);
            history.addSystem(`thinking     ${state.thinkingLevel}`);
            history.addSystem(`cwd          ${state.cwd}`);
            history.addSystem(`tokens       in:${s.inputTokens} out:${s.outputTokens} cache:${s.cachedInputTokens}`);
            history.addSystem(`cost (sess)  $${s.usd.toFixed(4)}`);
            tui.requestRender();
        },
        async setSessionName(name) {
            if (!state.session) {
                history.addSystem("session is unsaved — send a message first");
                tui.requestRender();
                return;
            }
            // /name with no arg opens an inline rename prompt prefilled with
            // the current name (diverges from pi-mono, which just prints it).
            let next = name.trim();
            if (!next) {
                next = (await promptOnce("session name", state.session.getName() ?? "")).trim();
                if (!next) return;
            }
            await state.session.setName(next);
            history.addSystem(`session name → ${next}`);
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
