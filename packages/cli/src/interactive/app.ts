/**
 * Interactive mode orchestrator: builds the TUI layout, wires state + deps
 * into the input handler / turn runner / command context, and kicks off
 * startup work. Behavior lives in the wired modules, not here.
 */
import {
    CombinedAutocompleteProvider,
    Container,
    Editor,
    ProcessTerminal,
    SelectList,
    type SelectItem,
    Spacer,
    Text,
    truncateToWidth,
    TUI,
    type Component,
    type EditorTheme,
    type SlashCommand as TuiSlashCommand,
} from "@notshekhar/loop-tui";
import chalk from "chalk";
import {
    CommandRegistry,
    CostTracker,
    SessionManager,
    registerBuiltins,
    getActiveProvider,
    settingsStore,
    getCatalog,
    parseModelId,
    ensureTool,
    runHooks,
    hookBus,
    closeAllPools,
    getMcpManager,
    getExtensionHost,
    agentExists,
    isBuiltinAgent,
    isHiddenAgent,
    DEFAULT_AGENT_NAME,
    getProjectModel,
    type ThinkingLevel,
    type ProviderId,
    type Session,
} from "@notshekhar/loop-core";
import { getSelectListTheme, initTheme } from "./ui/theme";
import { ChatHistory } from "./components/chat-history";
import { StatusLine } from "./components/status-line";
import {
    selectOnce as selectOnceShared,
    searchSelectOnce as searchSelectOnceShared,
    promptOnce as promptOnceShared,
    toggleSelectOnce,
} from "./selectors";
import { createCommandContext } from "./command-handlers";
import { createInputHandler } from "./input-handler";
import { isEventTraceEnabled, setEventTraceSink, toggleEventTrace } from "./debug-log";
import { createTurnRunner } from "./turn-runner";
import { createStatusLineRefresher } from "./status-line-refresh";
import { createWorkingIndicator } from "./working-indicator";
import { createTicker } from "./ticker";
import { registerAppKeybindings } from "./app-keybindings";
import { installConsoleBridge } from "./console-bridge";
import { runStartupTrustAndHooks, showWhatsNew, showWorkspaceBanners, startUpdateCheck } from "./startup";
import { showWelcomeBanner } from "./welcome";
import { listUsableProviders } from "./provider-availability";
import { openBrowser } from "../open-browser";
import type { AppDeps } from "./deps";
import type { AppState } from "./state";

export interface InteractiveOptions {
    modelId?: string;
    provider?: ProviderId;
    cwd: string;
    sessionId?: string;
    version?: string;
}

/**
 * A single queued user message, rendered on exactly one line: newlines are
 * collapsed to spaces and anything past the viewport width is cut to a trailing
 * "…" (matching the user-message selector). Keeps the pending list compact no
 * matter how long or multi-line the queued input was.
 */
class PendingMessageLine implements Component {
    constructor(
        private readonly prefix: string,
        private readonly message: string,
    ) {}
    invalidate(): void {}
    render(width: number): string[] {
        const singleLine = this.message.replace(/\s+/g, " ").trim();
        const avail = Math.max(1, width - this.prefix.length);
        return [chalk.dim(this.prefix) + chalk.gray(truncateToWidth(singleLine, avail))];
    }
}

const editorTheme: EditorTheme = {
    borderColor: (s) => chalk.cyan(s),
    selectList: getSelectListTheme(),
};

/**
 * No model is selected at startup. Point the user at the right next step: if
 * they have no usable provider at all, they must /login; if a provider is
 * available (logged in or a detected ollama) they just need to pick one with
 * /provider (or /login into another).
 */
async function showNoModelGuidance(history: ChatHistory, tui: TUI): Promise<void> {
    const providers = await listUsableProviders();
    if (providers.length === 0) {
        history.addSystem(chalk.yellow("No model selected and no provider available. Run /login to get started."));
    } else {
        history.addSystem(
            chalk.yellow(
                `No model selected. Run /provider to pick one (${providers.join(", ")}), or /login to add another.`,
            ),
        );
    }
    tui.requestRender();
}

export async function runInteractive(opts: InteractiveOptions): Promise<void> {
    initTheme((settingsStore.get("theme") as string | undefined) ?? "dark");
    registerAppKeybindings();

    // Model precedence: CLI flag > this folder's last pick > global default.
    // No silent provider fallback — if the user never picked a model we leave
    // it empty and guide them to /login or /provider instead of defaulting to
    // some provider they may not even be authenticated for.
    let initialModelId =
        opts.modelId ?? getProjectModel(opts.cwd) ?? (settingsStore.get("defaultModel") as string | undefined) ?? "";

    const manager = new SessionManager();
    const initialSession: Session | null = opts.sessionId ? await manager.open(opts.sessionId) : null;
    const resumedModel = initialSession?.lastModel();
    if (resumedModel) initialModelId = resumedModel;
    // Provider follows the restored model (project/session picks carry it);
    // otherwise the active provider, if any.
    let effectiveProvider = (opts.provider ?? getActiveProvider() ?? "") as ProviderId;
    if (initialModelId) {
        try {
            effectiveProvider = parseModelId(initialModelId).provider as ProviderId;
        } catch {
            // unparseable id — keep the active provider
        }
    }

    const tracker = new CostTracker();
    // Resumed sessions restore their cost/usage/ctx from the transcript's
    // usage entries instead of showing zeros until the next message.
    const seededCtxTokens = initialSession ? tracker.seedFromSession(initialSession).ctxTokens : 0;
    // Load extensions BEFORE building commands so registerBuiltins (which lists
    // agents) sees extension-registered agents and gives them /<name> commands.
    // With nothing installed this is a no-op, so the command set is exactly the
    // builtins.
    // Give extensions a browser opener before activate() runs. The interactive
    // `ui` bridge is injected later, once the TUI selector helpers exist.
    getExtensionHost().setServices({ openExternal: (url) => openBrowser(url) });
    await getExtensionHost().init();
    const commands = new CommandRegistry();
    await registerBuiltins(commands, { cwd: opts.cwd });
    getExtensionHost().applyCommands(commands);

    const terminal = new ProcessTerminal();
    const tui = new TUI(terminal, true);

    const history = new ChatHistory(tui, opts.cwd);
    const statusLine = new StatusLine();
    statusLine.setModel(initialModelId);
    statusLine.setSession(initialSession?.id ?? "unsaved");
    statusLine.setCost(tracker.format());
    statusLine.setCostData(tracker.sessionBreakdown());
    statusLine.setCwd(opts.cwd);
    const initialThinking: ThinkingLevel = (settingsStore.get("thinkingLevel") as ThinkingLevel | undefined) ?? "off";
    statusLine.setThinking(initialThinking);

    // Plan is a per-session mode, not a sticky preference: a new loop always
    // boots in the default agent even if the last session ended in plan.
    const savedAgentRaw = (settingsStore.get("agent") as string | undefined) ?? DEFAULT_AGENT_NAME;
    const savedAgent = savedAgentRaw === "plan" ? DEFAULT_AGENT_NAME : savedAgentRaw;
    const state: AppState = {
        cwd: opts.cwd,
        modelId: initialModelId,
        provider: effectiveProvider,
        thinkingLevel: initialThinking,
        agent: agentExists(savedAgent) ? savedAgent : DEFAULT_AGENT_NAME,
        oneShotAgent: null,
        cycleCustomAgent:
            agentExists(savedAgent) && (!isBuiltinAgent(savedAgent) || isHiddenAgent(savedAgent)) ? savedAgent : null,
        session: initialSession,
        latestContextTokens: seededCtxTokens,
        busy: false,
        abort: new AbortController(),
        pendingInjection: null,
        lastCtrlCAt: 0,
        startupHooksDone: null,
        timerEndsAt: null,
        timerLabel: "",
    };
    statusLine.setAgent(state.agent);

    const { refreshStatusLine, refreshStatusLineCtx } = createStatusLineRefresher(statusLine, tracker, tui, state);
    refreshStatusLineCtx();

    const editor = new Editor(tui, editorTheme, { paddingX: 1 });

    // @-mention fuzzy file search needs the `fd` binary; without it the provider
    // silently returns no file suggestions. Resolve it once (from PATH, else
    // download) and rebuild the provider when ready. `null` until then — slash
    // commands and explicit path completion still work in the meantime.
    let fdPath: string | null = null;
    const refreshCommands = () => {
        const slashItems: TuiSlashCommand[] = commands.list().map((c) => ({
            name: c.name,
            description: c.description,
        }));
        editor.setAutocompleteProvider(new CombinedAutocompleteProvider(slashItems, state.cwd, fdPath));
    };
    refreshCommands();
    void ensureTool("fd", true).then((path) => {
        if (!path) return;
        fdPath = path;
        refreshCommands();
    });

    // Editor lives in its own container so we can swap it out for selectors
    const editorContainer = new Container();
    editorContainer.addChild(editor);

    // Fixed-height status slot above editor so editor never shifts.
    // Loader renders 2 rows (leading blank + spinner line) — the idle spacer
    // must match, or the editor/status line block jumps a row on every turn start.
    const statusContainer = new Container();
    const statusIdleSpacer = new Spacer(2);
    statusContainer.addChild(statusIdleSpacer);

    // queued user messages render between status and editor
    const pendingContainer = new Container();
    const queuedMessages: string[] = [];
    function renderPending(): void {
        pendingContainer.clear();
        for (let i = 0; i < queuedMessages.length; i++) {
            pendingContainer.addChild(
                new PendingMessageLine(` queued ${i + 1}/${queuedMessages.length}: `, queuedMessages[i]),
            );
        }
    }

    const root = new Container();
    root.addChild(history);
    root.addChild(statusContainer);
    root.addChild(pendingContainer);
    root.addChild(editorContainer);
    root.addChild(statusLine);
    // Constant breathing room below the status line — without it the status block
    // sits flush with the terminal's bottom row once the screen fills up.
    root.addChild(new Spacer(1));
    tui.addChild(root);

    showWhatsNew(history, opts.version, Boolean(opts.sessionId));
    // Routes its result to the welcome banner (top), not chat history; safe to
    // kick off before the banner exists — the notice is remembered and applied.
    startUpdateCheck(opts.version);

    // Force a catalog availability refresh on every startup — provider model
    // lists drift (new releases, deprecations, gating) and the cached list is
    // good for up to 1h otherwise. Fire-and-forget; mergedCache rebuilds when
    // this lands so the /model picker reflects today's reality.
    void getCatalog({ refresh: true }).catch(() => {});

    const { showWorking, hideWorking } = createWorkingIndicator(tui, statusContainer, statusIdleSpacer);

    // Open-selector count — timer/reminder prompts wait until the slot is free.
    let selectorDepth = 0;
    function showSelector(component: Container, focusable: Container | SelectList): () => void {
        selectorDepth++;
        editorContainer.clear();
        editorContainer.addChild(component);
        tui.setFocus(focusable as never);
        tui.invalidate();
        tui.requestRender();
        return () => {
            selectorDepth--;
            editorContainer.clear();
            editorContainer.addChild(editor);
            tui.setFocus(editor);
            tui.invalidate();
            tui.requestRender();
        };
    }

    const selectorHost = { tui, showSelector };
    const selectOnce = (items: SelectItem[], title?: string, opts?: { initialIndex?: number }) =>
        selectOnceShared(selectorHost, items, title, opts);
    const searchOnce = (items: SelectItem[], title?: string, opts?: { initialIndex?: number }) =>
        searchSelectOnceShared(selectorHost, items, title, opts);
    const promptOnce = (label?: string, initial?: string) =>
        promptOnceShared(selectorHost, editorTheme, label, initial);
    const toggleOnce = (values: string[], initial: Set<string>, title?: string) =>
        toggleSelectOnce(selectorHost, values, initial, title);

    // Expose the interactive menus/prompts to extensions (api.ui). SelectItem is
    // structurally identical to the extension API's UiSelectItem, so the lists
    // pass straight through. Used by extension command handlers, which only run
    // once the app is interactive — so wiring it here (after init) is fine.
    getExtensionHost().setServices({
        ui: {
            select: (items, title, opts) => selectOnce(items, title, opts),
            search: (items, title, opts) => searchOnce(items, title, opts),
            prompt: (label, initial) => promptOnce(label, initial),
            note: (text) => {
                history.addSystem(text);
                tui.requestRender();
            },
            error: (text) => {
                history.addError(text);
                tui.requestRender();
            },
        },
    });

    async function ensureSession(): Promise<Session> {
        if (state.session) return state.session;
        state.session = await manager.create({ cwd: state.cwd, provider: state.provider, model: state.modelId });
        statusLine.setSession(state.session.id);
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

    // Shared 1s ticker (status line clock, /timer countdown, reminder scheduler);
    // owns its own timer/reminder/notice state and runs only while needed.
    const { syncTicker, stopTicker } = createTicker({
        state,
        statusLine,
        tui,
        getSelectorDepth: () => selectorDepth,
        selectOnce,
    });

    const restoreConsole = installConsoleBridge(history, tui);

    // Event tracer: dim trace lines in the chat + ~/.loop/events-debug.log.
    // Off by default; LOOP_DEBUG_EVENTS=1 enables at startup, Shift+Ctrl+D
    // toggles at runtime.
    setEventTraceSink((line) => {
        history.addSystem(chalk.dim(`· ${line}`));
        tui.requestRender();
    });
    tui.onDebug = () => {
        const on = toggleEventTrace();
        history.addSystem(chalk.dim(`· event trace ${on ? "ON" : "off"} → ~/.loop/events-debug.log`));
        tui.requestRender();
    };
    if (isEventTraceEnabled()) {
        history.addSystem(chalk.dim("· event trace ON (LOOP_DEBUG_EVENTS) — Shift+Ctrl+D to toggle"));
    }

    // (Active-extensions banner is shown by showWorkspaceBanners, grouped with
    // the workspace-context lines — see below.)
    // Surface any extension load failures (version mismatch, throw in activate),
    // so a broken extension is visible instead of silently missing.
    for (const w of getExtensionHost().getWarnings()) history.addError(`extension: ${w}`);

    const cleanExit = (code = 0) => {
        stopTicker();
        tui.stop();
        // Tear down MCP transports (stdio subprocesses, sockets) on the way out.
        void getMcpManager().close();
        // Run extensions' deactivate() so they can release resources.
        void getExtensionHost().close();
        // Close any open datasource connection pools.
        void closeAllPools();
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
        statusLine,
        tracker,
        editor,
        commands,
        manager,
        queuedMessages,
        refreshStatusLine,
        refreshStatusLineCtx,
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
        version: opts.version,
        restoreConsole,
        syncTicker,
    };
    syncTicker();

    showWelcomeBanner(history, state, deps);
    await showWorkspaceBanners(history, state.cwd);
    if (!state.modelId) await showNoModelGuidance(history, tui);

    const ctx = createCommandContext(state, deps);
    tui.addInputListener(createInputHandler(state, deps, ctx));
    editor.onSubmit = createTurnRunner(state, deps, ctx);

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
