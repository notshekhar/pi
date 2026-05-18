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
} from "@earendil-works/pi-tui";
import { DynamicBorder, getSelectListTheme, initTheme } from "@earendil-works/pi-coding-agent";
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
  type ThinkingLevel,
  type ProviderId,
  type Session,
  type UsageBlock,
} from "@pi/core";
import { ChatHistory } from "./components/chat-history";
import { CostFooter } from "./components/cost-footer";
import { selectOnce as selectOnceShared, promptOnce as promptOnceShared } from "./selectors";
import { createCommandContext } from "./command-handlers";
import { createInputHandler } from "./input-handler";
import { createTurnRunner } from "./turn-runner";
import type { AppDeps } from "./deps";
import type { AppState } from "./state";

export interface InteractiveOptions {
  modelId?: string;
  provider?: ProviderId;
  cwd: string;
  sessionId?: string;
}

const editorTheme: EditorTheme = {
  borderColor: (s) => chalk.cyan(s),
  selectList: getSelectListTheme(),
};

export async function runInteractive(opts: InteractiveOptions): Promise<void> {
  initTheme();

  const APP_KEYBINDINGS = {
    "app.tools.expand": { defaultKeys: "ctrl+o", description: "Toggle tool output" },
    "app.interrupt": { defaultKeys: "escape", description: "Interrupt agent" },
    "app.clear": { defaultKeys: "ctrl+c", description: "Clear / exit" },
  } as const;
  setKeybindings(new KeybindingsManager({ ...TUI_KEYBINDINGS, ...APP_KEYBINDINGS } as never));

  const initialProvider = (opts.provider ?? getActiveProvider() ?? "xai") as ProviderId;
  let initialModelId =
    opts.modelId ?? (settingsStore.get("defaultModel") as string) ?? `${initialProvider}/grok-4`;

  const manager = new SessionManager();
  const initialSession: Session | null = opts.sessionId ? await manager.open(opts.sessionId) : null;
  if (initialSession?.info.model) initialModelId = initialSession.info.model;

  const tracker = new CostTracker();
  const commands = new CommandRegistry();
  registerBuiltins(commands, { cwd: opts.cwd });

  const terminal = new ProcessTerminal();
  const tui = new TUI(terminal, true);

  const history = new ChatHistory(tui, opts.cwd);
  const footer = new CostFooter();
  footer.setModel(initialModelId);
  footer.setSession(initialSession?.id ?? "unsaved");
  footer.setCost(tracker.format());
  const initialThinking: ThinkingLevel =
    (settingsStore.get("thinkingLevel") as ThinkingLevel | undefined) ?? "off";
  footer.setThinking(initialThinking);

  const state: AppState = {
    cwd: opts.cwd,
    modelId: initialModelId,
    provider: initialProvider,
    thinkingLevel: initialThinking,
    session: initialSession,
    latestContextTokens: 0,
    busy: false,
    abort: new AbortController(),
    pendingInjection: null,
    lastCtrlCAt: 0,
  };

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

  const slashItems: TuiSlashCommand[] = commands.list().map((c) => ({
    name: c.name,
    description: c.description,
  }));
  editor.setAutocompleteProvider(new CombinedAutocompleteProvider(slashItems, state.cwd));

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
        new Text(chalk.dim(` queued ${i + 1}/${queuedMessages.length}: `) + chalk.gray(queuedMessages[i]), 0, 0),
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

  let workingLoader: Loader | null = null;
  function showWorking(message = "Generating…"): void {
    const fullMsg = `${message} ${chalk.dim("(Esc to interrupt)")}`;
    if (workingLoader) {
      workingLoader.setMessage(fullMsg);
      return;
    }
    workingLoader = new Loader(tui, (s) => chalk.cyan(s), (s) => chalk.dim(s), fullMsg);
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
  const selectOnce = (items: SelectItem[], title?: string) =>
    selectOnceShared(selectorHost, items, title);
  const promptOnce = (label?: string) => promptOnceShared(selectorHost, editorTheme, label);

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
    process.exit(code);
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
    promptOnce,
    resolveModelId,
    ensureSession,
    cleanExit,
  };

  history.addSystem(`pi · ${state.modelId} · session ${state.session?.id ?? "unsaved"}`);
  history.addSystem(`Type /help for commands. Ctrl+C twice to quit.`);

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
    const sk = loadProjectSkills(state.cwd);
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

  tui.setFocus(editor);
  tui.start();
  tui.requestRender();
}
