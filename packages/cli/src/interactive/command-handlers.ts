import { spawn } from "node:child_process";
import { existsSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import {
    CombinedAutocompleteProvider,
    type SelectItem,
    type SlashCommand as TuiSlashCommand,
} from "@notshekhar/pi-tui";
import chalk from "chalk";
import {
    CommandRegistry,
    CompactAbortedError,
    type CommandContext,
    type ProviderId,
    type Session,
    type ThinkingLevel,
    type UsageBlock,
    bustCatalogCache,
    getActiveProvider,
    getCatalog,
    listAuthorizedProviders,
    listCustomProviders,
    parseModelId,
    registerBuiltins,
    registerAgentCommand,
    runCompact,
    runHooks,
    listHooksWithSources,
    addPiUserHook,
    removePiUserHook,
    HOOK_EVENTS,
    type HookEvent,
    agentExists,
    listAgents,
    isValidAgentName,
    saveAgent,
    deleteAgent,
    getAgentPrompt,
    getAgentTools,
    hasBuiltinOverride,
    DEFAULT_AGENT_NAME,
    DEFAULT_BASE_PROMPT,
    TOOL_NAMES,
    setActiveProvider,
    setProjectModel,
    settingsStore,
    THINKING_LEVEL_DESCRIPTIONS,
    THINKING_LEVELS,
} from "@notshekhar/pi-core";
import type { AppDeps } from "./deps";
import type { AppState } from "./state";
import { readClipboardImageToFile } from "./clipboard-image";
import { startLogin, startLogout } from "./login-flow";
import { initTheme } from "./ui/theme";
import { loadChangelogEntries } from "../changelog";

export function createCommandContext(state: AppState, deps: AppDeps): CommandContext {
    const {
        tui,
        history,
        footer,
        tracker,
        editor,
        commands,
        manager,
        queuedMessages,
        refreshFooter,
        renderPending,
        showWorking,
        hideWorking,
        selectOnce,
        toggleOnce,
        promptOnce,
        resolveModelId,
        cleanExit,
        refreshCommands,
    } = deps;

    const loginDeps = { tui, history, selectOnce, promptOnce };

    return {
        get cwd() {
            return state.cwd;
        },
        emit(event, data) {
            if (event === "help" || event === "error") history.addSystem(String(data ?? ""));
            if (event === "inject-prompt") state.pendingInjection = String(data ?? "");
            if (event === "inject-skill") {
                const text = String(data ?? "");
                if (text && editor.onSubmit) void editor.onSubmit(text);
            }
            tui.requestRender();
        },
        async setModel(id) {
            const resolved = await resolveModelId(id);
            if (!resolved) {
                history.addSystem(chalk.red(`unknown model: ${id} — try /model to pick from a list`));
                tui.requestRender();
                return;
            }
            state.modelId = resolved;
            settingsStore.set("defaultModel", resolved);
            setProjectModel(state.cwd, resolved);
            footer.setModel(resolved);
            refreshFooter();
            history.addSystem(`model → ${resolved}`);
            tui.requestRender();
        },
        async setProvider(p) {
            // auth'd providers + zero-login ollama (daemon detected via the catalog)
            // + saved custom providers (gateways like bifrost)
            const authed = listAuthorizedProviders();
            const detectedCat = await getCatalog();
            for (const m of Object.values(detectedCat)) {
                if (m.available && m.provider === "ollama" && !authed.includes(m.provider)) {
                    authed.push(m.provider);
                }
            }
            for (const c of listCustomProviders()) {
                const id = `custom:${c.name}` as ProviderId;
                if (!authed.includes(id)) authed.push(id);
            }
            if (authed.length === 0) {
                history.addSystem(chalk.yellow("no providers authenticated. /login first."));
                tui.requestRender();
                return;
            }
            let target = p;
            if (!target) {
                const items: SelectItem[] = authed.map((id) => ({
                    value: id,
                    label: id,
                    description: id === getActiveProvider() ? "(active)" : "",
                }));
                const pick = await selectOnce(items);
                if (!pick) return;
                target = pick.value;
            }
            if (!authed.includes(target as ProviderId)) {
                history.addSystem(chalk.red(`not authorized: ${target}. /login ${target} first.`));
                tui.requestRender();
                return;
            }
            setActiveProvider(target as ProviderId);
            const cat = await getCatalog();
            const first = Object.values(cat).find((m) => m.provider === target && m.available);
            if (first) {
                state.modelId = first.id;
                settingsStore.set("defaultModel", state.modelId);
                setProjectModel(state.cwd, state.modelId);
                footer.setModel(state.modelId);
            }
            history.addSystem(`provider → ${target}${first ? `, model → ${first.id}` : ""}`);
            tui.requestRender();
        },
        async newSession() {
            state.session = null;
            footer.setSession("unsaved");
            tracker.reset();
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
        async setThinking(level) {
            let target = level as ThinkingLevel | undefined;
            if (!target) {
                const items: SelectItem[] = THINKING_LEVELS.map((lv) => ({
                    value: lv,
                    label: lv,
                    description: THINKING_LEVEL_DESCRIPTIONS[lv] + (lv === state.thinkingLevel ? "  (current)" : ""),
                }));
                const pick = await selectOnce(items, "Thinking level");
                if (!pick) return;
                target = pick.value as ThinkingLevel;
            }
            if (!(THINKING_LEVELS as readonly string[]).includes(target)) {
                history.addSystem(
                    chalk.red(`unknown thinking level: ${target}. options: ${THINKING_LEVELS.join(", ")}`),
                );
                tui.requestRender();
                return;
            }
            state.thinkingLevel = target;
            settingsStore.set("thinkingLevel", target);
            footer.setThinking(target);
            history.addSystem(`thinking → ${target}`);
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
                const usages = state.session
                    .entries()
                    .filter(
                        (e) =>
                            e.type === "message" &&
                            (e as { role?: string }).role === "assistant" &&
                            (e as { usage?: unknown }).usage,
                    )
                    .map((e) => (e as { usage: UsageBlock }).usage);
                state.latestContextTokens = tracker.seedFromEntries(
                    state.session.info.model || state.modelId,
                    usages,
                ).ctxTokens;
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
                const entries = state.session.entries();
                let latestCompact: Extract<ReturnType<Session["entries"]>[number], { type: "compact" }> | undefined;
                for (let i = entries.length - 1; i >= 0; i--) {
                    const entry = entries[i];
                    if (entry?.type === "compact") {
                        latestCompact = entry;
                        break;
                    }
                }
                if (latestCompact)
                    history.addCompactionSummary(latestCompact.summary, latestCompact.tokensBefore, latestCompact.ts);
                let messageIndex = 0;
                for (const e of entries) {
                    if (e.type === "message") {
                        const currentMessageIndex = messageIndex++;
                        if (latestCompact && currentMessageIndex < latestCompact.cutAt) continue;
                        const role = (e as { role: string }).role;
                        const content = String((e as { content: unknown }).content ?? "");
                        if (role === "user") history.addUser(content);
                        else if (role === "assistant") {
                            history.ensureAssistant(parseModelId(state.modelId).provider, state.modelId);
                            history.appendAssistantDelta(content, parseModelId(state.modelId).provider, state.modelId);
                            history.finishAssistant();
                        }
                    } else if (e.type === "compact" && !latestCompact) {
                        history.addCompactionSummary(e.summary, e.tokensBefore, e.ts);
                    }
                }
            } catch (err) {
                history.addError(`open failed: ${(err as Error).message}`);
            }
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
        async openSettings() {
            // Loop so Esc on the value prompt returns to the settings picker
            // instead of bailing out of /settings entirely.
            while (true) {
                const items: SelectItem[] = [
                    { value: "theme", label: `theme: ${settingsStore.get("theme") ?? "dark"}` },
                    {
                        value: "maxSteps",
                        label: `maxSteps: ${(settingsStore.get("maxSteps") as number) || "unlimited"}`,
                    },
                    {
                        value: "autoCompactThreshold",
                        label: `autoCompactThreshold: ${settingsStore.get("autoCompactThreshold") ?? 0.8}`,
                    },
                    { value: "piCompatMode", label: `piCompatMode: ${settingsStore.get("piCompatMode") ?? "direct"}` },
                    {
                        value: "workspaceContext",
                        label: `workspaceContext: ${settingsStore.get("workspaceContext") ?? true}`,
                    },
                ];
                const pick = await selectOnce(items, "Settings (Esc to close)");
                if (!pick) return;
                // Theme gets a picker (built-ins + ~/.pi/agent/themes/*.json) and
                // applies live — the global theme proxy makes themed components
                // re-resolve colors on the next render.
                if (pick.value === "theme") {
                    const customDir = join(process.env.HOME ?? "", ".pi", "agent", "themes");
                    const custom = existsSync(customDir)
                        ? readdirSync(customDir)
                              .filter((f) => f.endsWith(".json"))
                              .map((f) => f.replace(/\.json$/, ""))
                        : [];
                    const cur = (settingsStore.get("theme") as string) ?? "dark";
                    const themeItems: SelectItem[] = ["dark", "light", ...custom].map((n) => ({
                        value: n,
                        label: n,
                        description: n === cur ? "(current)" : "",
                    }));
                    const tPick = await selectOnce(themeItems, "Theme");
                    if (!tPick) continue;
                    settingsStore.set("theme", tPick.value);
                    initTheme(tPick.value);
                    tui.invalidate();
                    history.addSystem(`theme → ${tPick.value}`);
                    tui.requestRender(true);
                    continue;
                }
                history.addSystem(`enter new value for ${pick.value}: (Esc to go back)`);
                tui.requestRender();
                const v = await promptOnce("");
                if (!v) continue;
                const key = pick.value;
                const cur = settingsStore.get(key);
                const parsed = typeof cur === "number" ? Number(v) : typeof cur === "boolean" ? v === "true" : v;
                settingsStore.set(key, parsed);
                history.addSystem(`${key} → ${parsed}`);
                tui.requestRender();
            }
        },
        async openModelPicker() {
            const cat = await getCatalog();
            const active = (getActiveProvider() ?? state.provider) as ProviderId;
            const items: SelectItem[] = Object.values(cat)
                .filter((m) => m.provider === active && m.available)
                .sort((a, b) => a.id.localeCompare(b.id))
                .map((m) => {
                    const label = m.id.slice(active.length + 1);
                    const description = `${m.name}  ·  ctx ${m.contextWindow.toLocaleString()}  ·  $${m.cost.input}/$${m.cost.output}`;
                    return { value: m.id, label, description };
                });
            if (items.length === 0) {
                history.addSystem(chalk.yellow(`no models available for ${active}. Try /login ${active} first.`));
                tui.requestRender();
                return;
            }
            const pick = await selectOnce(items);
            if (!pick) return;
            state.modelId = pick.value;
            settingsStore.set("defaultModel", state.modelId);
            setProjectModel(state.cwd, state.modelId);
            footer.setModel(state.modelId);
            history.addSystem(`model → ${state.modelId}`);
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
        useAgent(name, message) {
            if (!agentExists(name)) {
                history.addSystem(chalk.red(`unknown agent: ${name} — /agents to create one`));
                tui.requestRender();
                return;
            }
            // /<agent> <message> = one-shot: that message runs under this agent's
            // prompt; the session's selected agent is untouched. Switching the
            // session agent happens only via /agents → use.
            if (message?.trim()) {
                state.oneShotAgent = name;
                history.addSystem(chalk.dim(`agent for this message: ${name}`));
                tui.requestRender();
                if (editor.onSubmit) void editor.onSubmit(message);
                return;
            }
            history.addSystem(`usage: /${name} <message> — one message with this agent. Session switch: /agents`);
            tui.requestRender();
        },
        async manageAgents() {
            const toolsLabel = (tools: string[] | undefined) => (tools?.length ? tools.join(", ") : "all tools");
            // Toggle multi-select (cursor stays put, Enter/Space toggles).
            // Returns undefined = all tools, null = cancelled.
            const pickTools = async (initial: string[] | undefined): Promise<string[] | undefined | null> => {
                const all = [...TOOL_NAMES] as string[];
                const picked = await toggleOnce(all, new Set(initial?.length ? initial : all), "Agent tools");
                if (picked === null) return null;
                return picked.length === all.length ? undefined : picked;
            };

            // Loop so Esc in submenus returns to the agent list, like /settings.
            while (true) {
                const agents = listAgents();
                const items: SelectItem[] = [
                    {
                        value: "+new",
                        label: "+ new agent",
                        description: "create an agent with its own tools and system prompt",
                    },
                    ...agents.map((a) => ({
                        value: a.name,
                        label:
                            a.name + (a.name === state.agent ? "  (active)" : "") + (a.builtin ? "  [built-in]" : ""),
                        description: `[${toolsLabel(a.tools)}] ${a.prompt.split("\n")[0].slice(0, 60)}`,
                    })),
                ];
                const pick = await selectOnce(items, "Agents (Esc to close)");
                if (!pick) return;

                if (pick.value === "+new") {
                    const name = (await promptOnce("agent name (e.g. reviewer)")).trim();
                    if (!name) continue;
                    if (!isValidAgentName(name)) {
                        history.addSystem(chalk.red(`invalid name: ${name} (alphanumeric, dashes, ≤32 chars)`));
                        tui.requestRender();
                        continue;
                    }
                    if (agentExists(name) || commands.has(name)) {
                        history.addSystem(chalk.red(`"${name}" already exists (agent or command)`));
                        tui.requestRender();
                        continue;
                    }
                    // Tools first, then the prompt — the prompt can reference what's allowed.
                    const tools = await pickTools(undefined);
                    if (tools === null) continue;
                    const prompt = await promptOnce(
                        `system prompt for "${name}" [${toolsLabel(tools)}]`,
                        DEFAULT_BASE_PROMPT,
                    );
                    if (!prompt.trim()) continue;
                    saveAgent(name, prompt, tools);
                    registerAgentCommand(commands, name);
                    refreshCommands();
                    history.addSystem(
                        `agent "${name}" created [${toolsLabel(tools)}] — /${name} <message> for one message, /agents → use for the session`,
                    );
                    tui.requestRender();
                    continue;
                }

                const name = pick.value;
                const info = agents.find((a) => a.name === name);
                const isBuiltin = info?.builtin ?? false;
                const currentTools = getAgentTools(name);
                const actions: SelectItem[] = [
                    { value: "use", label: "use", description: `switch active agent to "${name}"` },
                    { value: "edit", label: "edit prompt", description: "edit this agent's system prompt" },
                ];
                if (isBuiltin) {
                    // Built-in tool sets are fixed — preview only, no edit.
                    actions.push({
                        value: "tools-view",
                        label: "tools (fixed)",
                        description: toolsLabel(currentTools),
                    });
                    if (hasBuiltinOverride(name)) {
                        actions.push({
                            value: "delete",
                            label: "reset to built-in",
                            description: "remove the prompt override",
                        });
                    }
                } else {
                    actions.push({
                        value: "tools",
                        label: "edit tools",
                        description: `current: ${toolsLabel(currentTools)}`,
                    });
                    actions.push({ value: "delete", label: "delete", description: "remove agent and its /command" });
                }
                const action = await selectOnce(actions, `Agent: ${name} [${toolsLabel(currentTools)}]`);
                if (!action) continue;

                if (action.value === "use") {
                    state.agent = name;
                    settingsStore.set("agent", name);
                    footer.setAgent(name);
                    history.addSystem(`agent → ${name}`);
                    tui.requestRender();
                    return;
                }
                if (action.value === "tools-view") {
                    history.addSystem(`agent "${name}" tools (fixed): ${toolsLabel(currentTools)}`);
                    tui.requestRender();
                    continue;
                }
                if (action.value === "tools") {
                    const tools = await pickTools(currentTools);
                    if (tools === null) continue;
                    saveAgent(name, getAgentPrompt(name) ?? DEFAULT_BASE_PROMPT, tools);
                    history.addSystem(`agent "${name}" tools → ${toolsLabel(tools)}`);
                    tui.requestRender();
                    continue;
                }
                if (action.value === "edit") {
                    const current = getAgentPrompt(name) ?? DEFAULT_BASE_PROMPT;
                    // Built-ins: tools are fixed but still previewed before editing.
                    if (isBuiltin) {
                        history.addSystem(chalk.dim(`tools (fixed): ${toolsLabel(currentTools)}`));
                        tui.requestRender();
                    }
                    const edited = await promptOnce(
                        `system prompt for "${name}" [${toolsLabel(currentTools)}]`,
                        current,
                    );
                    if (!edited.trim() || edited.trim() === current.trim()) continue;
                    saveAgent(name, edited, currentTools);
                    history.addSystem(`agent "${name}" prompt updated`);
                    tui.requestRender();
                    continue;
                }
                if (action.value === "delete") {
                    deleteAgent(name);
                    if (state.agent === name && !isBuiltin) {
                        state.agent = DEFAULT_AGENT_NAME;
                        settingsStore.set("agent", DEFAULT_AGENT_NAME);
                        footer.setAgent(DEFAULT_AGENT_NAME);
                    }
                    if (!isBuiltin) commands.unregister(name);
                    refreshCommands();
                    history.addSystem(isBuiltin ? `"${name}" prompt reset to built-in` : `agent "${name}" deleted`);
                    tui.requestRender();
                    continue;
                }
            }
        },
        async manageHooks() {
            // Loop so Esc in submenus returns to the hook list, like /settings.
            while (true) {
                const entries = listHooksWithSources(state.cwd);
                const items: SelectItem[] = [
                    {
                        value: "+add",
                        label: "+ add hook",
                        description: "register a pi-owned hook in ~/.pi/settings.json",
                    },
                    ...entries.map((e, i) => ({
                        value: String(i),
                        label: `${e.event}${e.matcher ? ` [${e.matcher}]` : ""}${e.async ? " (async)" : ""}`,
                        description: `${e.source} · ${e.command.length > 70 ? `${e.command.slice(0, 67)}…` : e.command}`,
                    })),
                ];
                const pick = await selectOnce(items, `Hooks — ${entries.length} loaded (Esc to close)`);
                if (!pick) return;

                if (pick.value === "+add") {
                    const ev = await selectOnce(
                        HOOK_EVENTS.map((e) => ({ value: e, label: e, description: "" })),
                        "Hook event",
                    );
                    if (!ev) continue;
                    let matcher = "";
                    if (ev.value === "PreToolUse" || ev.value === "PostToolUse") {
                        matcher = (
                            await promptOnce(`matcher for ${ev.value} (tool name or regex, empty = all)`)
                        ).trim();
                    }
                    const command = (await promptOnce("hook command (runs via sh, JSON payload on stdin)")).trim();
                    if (!command) continue;
                    addPiUserHook(ev.value as HookEvent, command, matcher || undefined);
                    history.addSystem(`hook added: ${ev.value}${matcher ? ` [${matcher}]` : ""} → ${command}`);
                    tui.requestRender();
                    continue;
                }

                const e = entries[Number(pick.value)];
                if (!e) continue;
                const label = `${e.event}: ${e.command.length > 50 ? `${e.command.slice(0, 47)}…` : e.command}`;

                if (e.source === "pi-user") {
                    const act = await selectOnce(
                        [{ value: "remove", label: "remove", description: "delete from ~/.pi/settings.json" }],
                        label,
                    );
                    if (act?.value === "remove" && removePiUserHook(e.event, e.command)) {
                        history.addSystem(`hook removed: ${e.event} → ${e.command.slice(0, 60)}`);
                        tui.requestRender();
                    }
                    continue;
                }
                if (e.source.startsWith("claude")) {
                    const act = await selectOnce(
                        [
                            {
                                value: "copy",
                                label: "copy to pi",
                                description: "own it in ~/.pi/settings.json — keeps working without Claude Code",
                            },
                        ],
                        `${label}  (${e.source})`,
                    );
                    if (act?.value === "copy") {
                        addPiUserHook(e.event, e.command, e.matcher, e.async);
                        history.addSystem(
                            `hook copied to ~/.pi: ${e.event} → ${e.command.slice(0, 60)} — adjust claudeHooksFilter if it now fires twice`,
                        );
                        tui.requestRender();
                    }
                    continue;
                }
                // pi-project hooks live in the repo — point there instead of mutating it.
                history.addSystem(`project hook — edit ${state.cwd}/.pi/settings.json: ${e.event} → ${e.command}`);
                tui.requestRender();
            }
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
                "Tab             autocomplete · toggle agent (empty prompt)",
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
            if (!state.session) {
                history.addSystem("no assistant message to copy");
                tui.requestRender();
                return;
            }
            const entries = state.session.entries();
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
        async reload() {
            // Hard reload: every config surface re-read from disk, models
            // re-fetched from the network (blocking, so the result is real).
            showWorking("Reloading");
            tui.requestRender();
            try {
                // Theme (settings may have changed on disk).
                initTheme((settingsStore.get("theme") as string | undefined) ?? "dark");

                // Commands: prompts, skills, agents — rebuilt from disk.
                const fresh = new CommandRegistry();
                await registerBuiltins(fresh, { cwd: state.cwd });
                (commands as unknown as { commands: Map<string, unknown> }).commands = (
                    fresh as unknown as { commands: Map<string, unknown> }
                ).commands;
                refreshCommands();

                // Active agent may have been deleted on disk meanwhile.
                if (!agentExists(state.agent)) {
                    state.agent = DEFAULT_AGENT_NAME;
                    settingsStore.set("agent", DEFAULT_AGENT_NAME);
                }
                footer.setAgent(state.agent);

                // Models: force-refresh availability + model definitions.
                bustCatalogCache();
                const cat = await getCatalog({ refresh: true });
                const available = Object.values(cat).filter((m) => m.available).length;

                tui.invalidate();
                history.addSystem(
                    `reloaded — settings, theme, commands, agents, hooks config, models (${available}/${Object.keys(cat).length} available)`,
                );
            } catch (err) {
                history.addError(`reload failed: ${err instanceof Error ? err.message : String(err)}`);
            } finally {
                hideWorking();
            }
            tui.requestRender(true);
        },
        stub(name) {
            history.addSystem(chalk.yellow(`/${name} not implemented yet`));
            tui.requestRender();
        },
    };
}
