/**
 * Interactive mode orchestrator: builds the TUI layout, wires state + deps
 * into the input handler / turn runner / command context, and kicks off
 * startup work. Behavior lives in the wired modules, not here.
 */
import {
    CombinedAutocompleteProvider,
    Container,
    Editor,
    Loader,
    ProcessTerminal,
    SelectList,
    type SelectItem,
    Spacer,
    Text,
    TUI,
    type EditorTheme,
    type SlashCommand as TuiSlashCommand,
} from "@notshekhar/pi-tui";
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
    runHooks,
    hookBus,
    agentExists,
    isBuiltinAgent,
    DEFAULT_AGENT_NAME,
    getProjectModel,
    type ThinkingLevel,
    type ProviderId,
    type Session,
    type UsageBlock,
} from "@notshekhar/pi-core";
import { getSelectListTheme, initTheme } from "./ui/theme";
import { ChatHistory } from "./components/chat-history";
import { CostFooter } from "./components/cost-footer";
import {
    selectOnce as selectOnceShared,
    searchSelectOnce as searchSelectOnceShared,
    promptOnce as promptOnceShared,
    toggleSelectOnce,
} from "./selectors";
import { createCommandContext } from "./command-handlers";
import { createInputHandler } from "./input-handler";
import { createTurnRunner } from "./turn-runner";
import { registerAppKeybindings } from "./app-keybindings";
import { installConsoleBridge } from "./console-bridge";
import { runStartupTrustAndHooks, showWhatsNew, showWorkspaceBanners, startUpdateCheck } from "./startup";
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

const PROVIDER_DEFAULT_MODEL: Record<string, string> = {
    xai: "xai/grok-build-0.1",
    anthropic: "anthropic/claude-sonnet-4-6",
    openai: "openai/gpt-5",
    google: "google/gemini-3.1-pro",
    openrouter: "openrouter/anthropic/claude-sonnet-4-6",
    "github-copilot": "github-copilot/gpt-5",
};

export async function runInteractive(opts: InteractiveOptions): Promise<void> {
    initTheme((settingsStore.get("theme") as string | undefined) ?? "dark");
    registerAppKeybindings();

    const initialProvider = (opts.provider ?? getActiveProvider() ?? "xai") as ProviderId;
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

    showWhatsNew(history, opts.version, Boolean(opts.sessionId));
    startUpdateCheck(history, tui, opts.version);

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
    const searchOnce = (items: SelectItem[], title?: string) => searchSelectOnceShared(selectorHost, items, title);
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
        searchOnce,
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
    history.addSystem(`Type /help for commands. Shift+Tab cycles agents. Ctrl+C twice to quit.`);
    await showWorkspaceBanners(history, state.cwd);

    const ctx = createCommandContext(state, deps);
    tui.addInputListener(createInputHandler(state, deps, ctx));
    editor.onSubmit = createTurnRunner(state, deps, ctx);

    installConsoleBridge(history, tui);

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

    // Catalog warm-up: models change between releases — kick the
    // stale-while-revalidate refresh now (background; serves cache instantly)
    // so the model list and availability are fresh for this session.
    void getCatalog().catch(() => {});

    // Trust prompt + SessionStart hooks; the first turn awaits this so
    // hook-injected context isn't lost to a fast first prompt.
    state.startupHooksDone = runStartupTrustAndHooks(state, deps);
}
