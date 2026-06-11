import {
    CombinedAutocompleteProvider,
    Container,
    Editor,
    KeybindingsManager,
    Loader,
    ProcessTerminal,
    SelectList,
    type SelectItem,
    setKeybindings,
    Spacer,
    Text,
    TUI,
    TUI_KEYBINDINGS,
    type EditorTheme,
    type SlashCommand as TuiSlashCommand,
} from "@notshekhar/pi-tui";
import { DynamicBorder } from "./ui/messages";
import { getSelectListTheme, initTheme } from "./ui/theme";
import chalk from "chalk";
import {
    CommandRegistry,
    CostTracker,
    SessionManager,
    registerBuiltins,
    getActiveProvider,
    settingsStore,
    getCatalog,
    getModelSync,
    parseModelId,
    loadWorkspaceContext,
    loadProjectSkills,
    runHooks,
    loadHooksConfig,
    hookBus,
    agentExists,
    isBuiltinAgent,
    DEFAULT_AGENT_NAME,
    getProjectModel,
    hasProjectTrustInputs,
    getTrustDecision,
    getTrustOptions,
    setTrust,
    trustForSession,
    type ThinkingLevel,
    type ProviderId,
    type Session,
    type UsageBlock,
} from "@notshekhar/pi-core";
import { ChatHistory } from "./components/chat-history";
import { CostFooter } from "./components/cost-footer";
import { selectOnce as selectOnceShared, promptOnce as promptOnceShared, toggleSelectOnce } from "./selectors";
import { createCommandContext } from "./command-handlers";
import { createInputHandler } from "./input-handler";
import { checkForUpdate } from "../commands";
import { getNewEntries, loadChangelogEntries } from "../changelog";
import { createTurnRunner } from "./turn-runner";
import type { AppDeps } from "./deps";
import type { AppState } from "./state";

export interface InteractiveOptions {
    modelId?: string;
    provider?: ProviderId;
    cwd: string;
    sessionId?: string;
    version?: string;
}

const editorTheme: EditorTheme = {
    borderColor: (s) => chalk.cyan(s),
    selectList: getSelectListTheme(),
};

export async function runInteractive(opts: InteractiveOptions): Promise<void> {
    initTheme((settingsStore.get("theme") as string | undefined) ?? "dark");

    const APP_KEYBINDINGS = {
        "app.tools.expand": { defaultKeys: "ctrl+e", description: "Toggle tool output" },
        "app.interrupt": { defaultKeys: "escape", description: "Interrupt agent" },
        "app.clear": { defaultKeys: "ctrl+c", description: "Clear / exit" },
    } as const;
    setKeybindings(new KeybindingsManager({ ...TUI_KEYBINDINGS, ...APP_KEYBINDINGS } as never));

    const initialProvider = (opts.provider ?? getActiveProvider() ?? "xai") as ProviderId;
    const PROVIDER_DEFAULT_MODEL: Record<string, string> = {
        xai: "xai/grok-build-0.1",
        anthropic: "anthropic/claude-sonnet-4-6",
        openai: "openai/gpt-5",
        google: "google/gemini-3.1-pro",
        openrouter: "openrouter/anthropic/claude-sonnet-4-6",
        "github-copilot": "github-copilot/gpt-5",
    };
    // Model precedence: CLI flag > this folder's last pick > global default.
    let initialModelId =
        opts.modelId ??
        getProjectModel(opts.cwd) ??
        (settingsStore.get("defaultModel") as string) ??
        PROVIDER_DEFAULT_MODEL[initialProvider] ??
        `${initialProvider}/grok-build-0.1`;

    const manager = new SessionManager();
    const initialSession: Session | null = opts.sessionId ? await manager.open(opts.sessionId) : null;
    if (initialSession?.info.model) initialModelId = initialSession.info.model;
    // Provider follows the restored model (project/session picks carry it).
    let effectiveProvider = initialProvider;
    try {
        effectiveProvider = parseModelId(initialModelId).provider as ProviderId;
    } catch {
        // unparseable id — keep the active provider
    }

    const tracker = new CostTracker();
    // Resumed sessions restore their cost/usage/ctx from the transcript's
    // usage entries instead of showing zeros until the next message.
    const seededCtxTokens = initialSession ? tracker.seedFromSession(initialSession).ctxTokens : 0;
    const commands = new CommandRegistry();
    await registerBuiltins(commands, { cwd: opts.cwd });

    const terminal = new ProcessTerminal();
    const tui = new TUI(terminal, true);

    const history = new ChatHistory(tui, opts.cwd);
    const footer = new CostFooter();
    footer.setModel(initialModelId);
    footer.setSession(initialSession?.id ?? "unsaved");
    footer.setCost(tracker.format());
    const initialThinking: ThinkingLevel = (settingsStore.get("thinkingLevel") as ThinkingLevel | undefined) ?? "off";
    footer.setThinking(initialThinking);

    const savedAgent = (settingsStore.get("agent") as string | undefined) ?? DEFAULT_AGENT_NAME;
    const state: AppState = {
        cwd: opts.cwd,
        modelId: initialModelId,
        provider: effectiveProvider,
        thinkingLevel: initialThinking,
        agent: agentExists(savedAgent) ? savedAgent : DEFAULT_AGENT_NAME,
        oneShotAgent: null,
        cycleCustomAgent: agentExists(savedAgent) && !isBuiltinAgent(savedAgent) ? savedAgent : null,
        session: initialSession,
        latestContextTokens: seededCtxTokens,
        busy: false,
        abort: new AbortController(),
        pendingInjection: null,
        lastCtrlCAt: 0,
        startupHooksDone: null,
    };
    footer.setAgent(state.agent);

    function ctxTokensFromUsage(u: UsageBlock): number {
        if (typeof u.totalTokens === "number" && u.totalTokens > 0) return u.totalTokens;
        return (u.inputTokens ?? 0) + (u.outputTokens ?? 0) + (u.cachedInputTokens ?? 0);
    }

    function refreshFooterCtx(usage?: UsageBlock): void {
        if (usage) state.latestContextTokens = ctxTokensFromUsage(usage);
        const info = getModelSync(state.modelId);
        footer.setContext(state.latestContextTokens, info?.contextWindow ?? 0);
    }

    function refreshFooter(usage?: UsageBlock): void {
        footer.setCost(tracker.format());
        refreshFooterCtx(usage);
        tui.requestRender();
    }
    refreshFooterCtx();

    const editor = new Editor(tui, editorTheme, { paddingX: 1 });

    const refreshCommands = () => {
        const slashItems: TuiSlashCommand[] = commands.list().map((c) => ({
            name: c.name,
            description: c.description,
        }));
        editor.setAutocompleteProvider(new CombinedAutocompleteProvider(slashItems, state.cwd));
    };
    refreshCommands();

    // pi pattern: editor lives in its own container so we can swap it out for selectors
    const editorContainer = new Container();
    editorContainer.addChild(editor);

    // pi pattern: fixed-height status slot above editor so editor never shifts
    const statusContainer = new Container();
    const statusIdleSpacer = new Spacer(1);
    statusContainer.addChild(statusIdleSpacer);

    // pi-mono parity: queued user messages render between status and editor
    const pendingContainer = new Container();
    const queuedMessages: string[] = [];
    function renderPending(): void {
        pendingContainer.clear();
        for (let i = 0; i < queuedMessages.length; i++) {
            pendingContainer.addChild(
                new Text(
                    chalk.dim(` queued ${i + 1}/${queuedMessages.length}: `) + chalk.gray(queuedMessages[i]),
                    0,
                    0,
                ),
            );
        }
    }

    const root = new Container();
    root.addChild(history);
    root.addChild(statusContainer);
    root.addChild(pendingContainer);
    root.addChild(editorContainer);
    root.addChild(footer);
    tui.addChild(root);

    // What's-new: show changelog entries the user hasn't seen yet (pi-mono
    // parity: fresh installs just record the version; resumed sessions skip).
    if (opts.version && !opts.sessionId) {
        const lastSeen = settingsStore.get("lastChangelogVersion") as string | undefined;
        if (!lastSeen) {
            settingsStore.set("lastChangelogVersion", opts.version);
        } else if (lastSeen !== opts.version) {
            const fresh = getNewEntries(loadChangelogEntries(), lastSeen);
            if (fresh.length > 0) {
                history.addSystem(chalk.dim(`Updated to v${opts.version} — what's new:`));
                history.addMarkdown(fresh.map((e) => e.content).join("\n\n"));
            }
            settingsStore.set("lastChangelogVersion", opts.version);
        }
    }

    // Silent background update check; suggest upgrade if a newer release exists.
    // Fire-and-forget so startup never blocks on the network.
    if (opts.version) {
        void checkForUpdate(opts.version).then((latest) => {
            if (latest) {
                history.addSystem(`Update available: v${opts.version} → ${latest}. Run \`pi update\` to upgrade.`);
                tui.requestRender();
            }
        });
    }

    // Force a catalog availability refresh on every startup — provider model
    // lists drift (new releases, deprecations, gating) and the cached list is
    // good for up to 1h otherwise. Fire-and-forget; mergedCache rebuilds when
    // this lands so the /model picker reflects today's reality.
    void getCatalog({ refresh: true }).catch(() => {});

    let workingLoader: Loader | null = null;
    function showWorking(message = "Generating…"): void {
        const fullMsg = `${message} ${chalk.dim("(Esc to interrupt)")}`;
        if (workingLoader) {
            workingLoader.setMessage(fullMsg);
            return;
        }
        workingLoader = new Loader(
            tui,
            (s) => chalk.cyan(s),
            (s) => chalk.dim(s),
            fullMsg,
        );
        statusContainer.clear();
        statusContainer.addChild(workingLoader);
        workingLoader.start();
        tui.requestRender();
    }
    function hideWorking(): void {
        if (!workingLoader) return;
        workingLoader.stop();
        statusContainer.clear();
        statusContainer.addChild(statusIdleSpacer);
        workingLoader = null;
        tui.requestRender();
    }

    function showSelector(component: Container, focusable: Container | SelectList): () => void {
        editorContainer.clear();
        editorContainer.addChild(component);
        tui.setFocus(focusable as never);
        tui.invalidate();
        tui.requestRender();
        return () => {
            editorContainer.clear();
            editorContainer.addChild(editor);
            tui.setFocus(editor);
            tui.invalidate();
            tui.requestRender();
        };
    }

    const selectorHost = { tui, showSelector };
    const selectOnce = (items: SelectItem[], title?: string) => selectOnceShared(selectorHost, items, title);
    const promptOnce = (label?: string, initial?: string) =>
        promptOnceShared(selectorHost, editorTheme, label, initial);
    const toggleOnce = (values: string[], initial: Set<string>, title?: string) =>
        toggleSelectOnce(selectorHost, values, initial, title);

    async function ensureSession(): Promise<Session> {
        if (state.session) return state.session;
        state.session = await manager.create({ cwd: state.cwd, provider: state.provider, model: state.modelId });
        footer.setSession(state.session.id);
        return state.session;
    }

    async function resolveModelId(input: string): Promise<string | null> {
        const cat = await getCatalog();
        if (cat[input]) return input;
        if (!input.includes("/")) {
            const active = (getActiveProvider() ?? state.provider) as ProviderId;
            const candidate = `${active}/${input}`;
            if (cat[candidate]) return candidate;
            const matches = Object.keys(cat).filter((k) => k.endsWith(`/${input}`));
            if (matches.length === 1) return matches[0];
        }
        try {
            parseModelId(input);
            return input;
        } catch {
            return null;
        }
    }

    const cleanExit = (code = 0) => {
        tui.stop();
        // SessionEnd hooks: give them a moment, then exit regardless.
        void Promise.race([
            runHooks(
                "SessionEnd",
                undefined,
                { session_id: state.session?.id, transcript_path: state.session?.path, reason: "exit" },
                state.cwd,
            ),
            new Promise((r) => setTimeout(r, 3_000)),
        ]).finally(() => process.exit(code));
    };

    const deps: AppDeps = {
        tui,
        history,
        footer,
        tracker,
        editor,
        commands,
        manager,
        queuedMessages,
        refreshFooter,
        refreshFooterCtx,
        renderPending,
        showWorking,
        hideWorking,
        showSelector,
        selectOnce,
        toggleOnce,
        promptOnce,
        resolveModelId,
        ensureSession,
        cleanExit,
        refreshCommands,
    };

    history.addSystem(
        `pi · ${state.modelId} · session ${state.session?.id ?? "unsaved"}` +
            (state.agent !== DEFAULT_AGENT_NAME ? ` · agent ${state.agent}` : ""),
    );
    history.addSystem(`Type /help for commands. Tab cycles agents. Ctrl+C twice to quit.`);

    if ((settingsStore.get("workspaceContext") as boolean) !== false) {
        const ws = loadWorkspaceContext(state.cwd);
        if (ws.files.length > 0) {
            history.addSystem(chalk.dim(`workspace context (${ws.files.length}):`));
            for (const f of ws.files) {
                history.addSystem(chalk.dim(`  • ${f.replace(process.env.HOME ?? "", "~")}`));
            }
        } else {
            history.addSystem(chalk.dim("workspace context: none (AGENTS.md, CLAUDE.md not found)"));
        }
    }
    if ((settingsStore.get("skills") as boolean) !== false) {
        const sk = await loadProjectSkills(state.cwd);
        if (sk.skills.length > 0) {
            history.addSystem(chalk.dim(`skills (${sk.skills.length}):`));
            for (const s of sk.skills) {
                history.addSystem(chalk.dim(`  • ${s.name} — ${s.description.slice(0, 80)}`));
            }
        }
    }

    const ctx = createCommandContext(state, deps);
    tui.addInputListener(createInputHandler(state, deps, ctx));
    editor.onSubmit = createTurnRunner(state, deps, ctx);

    // Stray console output from libraries (warnings, deprecation notices)
    // bypasses the renderer and tears frames. Route it into the chat as
    // messages instead — errors red, the rest dim. stdout/stderr writes from
    // native code still bypass this, but console.* covers the practical cases.
    const origConsole = { log: console.log, warn: console.warn, error: console.error };
    const fmt = (args: unknown[]) => args.map((a) => (typeof a === "string" ? a : JSON.stringify(a))).join(" ");
    console.log = (...args: unknown[]) => {
        history.addSystem(fmt(args));
        tui.requestRender();
    };
    console.warn = (...args: unknown[]) => {
        history.addSystem(fmt(args));
        tui.requestRender();
    };
    console.error = (...args: unknown[]) => {
        history.addError(fmt(args));
        tui.requestRender();
    };
    const restoreConsole = () => Object.assign(console, origConsole);
    process.once("exit", restoreConsole);

    // Last-resort error surfacing: anything that escapes a handler renders in
    // chat instead of tearing the TUI via stderr or killing the process.
    // Display only — errors are never written to the session transcript.
    const surfaceError = (prefix: string) => (err: unknown) => {
        const msg = err instanceof Error ? err.message : String(err);
        history.addError(`${prefix}: ${msg}`);
        tui.requestRender();
    };
    process.on("uncaughtException", surfaceError("uncaught"));
    process.on("unhandledRejection", surfaceError("unhandled"));

    // Plugin hooks ship statusMessage ("Loading caveman mode…") — transient
    // "while running" text, so it rides the loader, never the chat (a chat line
    // per prompt would spam every turn). Loader restores when the hook ends.
    let hookStatusDepth = 0;
    hookBus.on("start", (e: { statusMessage?: string }) => {
        if (!e.statusMessage) return;
        hookStatusDepth++;
        showWorking(e.statusMessage);
    });
    hookBus.on("end", (e: { statusMessage?: string }) => {
        if (!e.statusMessage) return;
        hookStatusDepth = Math.max(0, hookStatusDepth - 1);
        if (hookStatusDepth === 0) {
            if (state.busy) showWorking("Generating");
            else hideWorking();
        }
    });

    tui.setFocus(editor);
    tui.start();
    tui.requestRender();

    // Project trust → SessionStart hooks. First open of a folder that ships
    // .pi/.claude resources prompts before any project hook/skill can run; the
    // decision gates project resource loading (executable hooks, project skills).
    // Catalog warm-up: models change between releases — kick the
    // stale-while-revalidate refresh now (background; serves cache instantly)
    // so the model list and availability are fresh for this session.
    void getCatalog().catch(() => {});

    state.startupHooksDone = (async () => {
        if (hasProjectTrustInputs(state.cwd) && getTrustDecision(state.cwd) === null) {
            const opts = getTrustOptions(state.cwd);
            history.addSystem(
                chalk.yellow(`Trust this project folder?\n${state.cwd}`) +
                    chalk.dim("\nTrusting lets pi load this repo's .pi/.claude settings, hooks, and skills."),
            );
            tui.requestRender();
            const items: SelectItem[] = opts.map((o) => ({ value: o.label, label: o.label, description: "" }));
            const pick = await selectOnce(items, "Project trust");
            const chosen = opts.find((o) => o.label === pick?.value);
            if (chosen) {
                if (chosen.remember) setTrust(chosen.savePath, chosen.trusted);
                else if (chosen.trusted) trustForSession(state.cwd); // session-only: in-memory, not persisted
                history.addSystem(
                    chalk.dim(
                        chosen.trusted ? "✓ project trusted" : "✗ project not trusted — project hooks/skills disabled",
                    ),
                );
            } else {
                history.addSystem(chalk.dim("trust prompt dismissed — treating project as untrusted for now"));
            }
            tui.requestRender();
        }

        // Active hooks summary (after trust is resolved so project hooks count).
        // Display command shortened to its script basename — full plugin paths
        // are too long for the startup banner.
        const shortCmd = (cmd: unknown): string => {
            // Malformed config entries can lack `command` — banner must not throw.
            if (typeof cmd !== "string" || !cmd) return "(invalid hook entry)";
            const script = cmd.match(/[^\s"']+\.(?:sh|js|ts|py|cmd|mjs|cjs)\b/)?.[0];
            if (script) return script.split("/").pop()!;
            return cmd.length > 48 ? `${cmd.slice(0, 45)}…` : cmd;
        };
        const hooksCfg = loadHooksConfig(state.cwd);
        const hookEvents = Object.entries(hooksCfg).filter(([, groups]) => groups?.length);
        if (hookEvents.length > 0) {
            const total = hookEvents.reduce(
                (n, [, groups]) => n + groups!.reduce((m, g) => m + (g.hooks?.length ?? 0), 0),
                0,
            );
            history.addHook(`hooks (${total}):`);
            for (const [ev, groups] of hookEvents) {
                const cmds = groups!.flatMap((g) => g.hooks ?? []).map((h) => shortCmd(h.command));
                history.addSystem(chalk.dim(`    • ${ev}: ${cmds.join(", ")}`));
            }
            tui.requestRender();
        }

        // SessionStart hooks (now that trust is resolved): messages render in chat;
        // additionalContext rides the first user prompt.
        const h = await runHooks(
            "SessionStart",
            "startup",
            { session_id: state.session?.id, transcript_path: state.session?.path, source: "startup" },
            state.cwd,
        );
        for (const m of h.messages) history.addHook(m);
        for (const s of h.terminalSequences) process.stdout.write(s);
        if (h.additionalContext) {
            state.pendingInjection = state.pendingInjection
                ? `${state.pendingInjection}\n\n${h.additionalContext}`
                : h.additionalContext;
        }
        if (h.messages.length || h.additionalContext) tui.requestRender();
    })();
}
