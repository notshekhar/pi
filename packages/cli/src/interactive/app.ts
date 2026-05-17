import { EventEmitter } from "node:events";
import {
  Box,
  CombinedAutocompleteProvider,
  Container,
  Editor,
  KeybindingsManager,
  Loader,
  matchesKey,
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
import { DynamicBorder, getMarkdownTheme, getSelectListTheme, initTheme } from "@earendil-works/pi-coding-agent";
import chalk from "chalk";
import {
  CommandRegistry,
  CostTracker,
  SessionManager,
  registerBuiltins,
  runCompact,
  runTurn,
  getActiveProvider,
  setActiveProvider,
  loginApiKey,
  loginXaiOAuth,
  logout,
  saveCustomProvider,
  listAuthorizedProviders,
  listCustomProviders,
  fallbackModelsForSdk,
  settingsStore,
  PROVIDER_IDS,
  getCatalog,
  parseModelId,
  loadWorkspaceContext,
  loadProjectSkills,
  type CustomProviderConfig,
  type CustomProviderSdk,
  type ProviderId,
  type Session,
} from "@pi/core";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { spawn } from "node:child_process";
import { ChatHistory } from "./components/chat-history";
import { CostFooter } from "./components/cost-footer";
import { pickImageFile, readClipboardImageToFile } from "./clipboard-image";

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

const CTRL_C = "\x03";
const CTRL_D = "\x04";
const ESC = "\x1b";

function buildSelectorWrapper(items: SelectItem[], title: string | undefined, list: SelectList): Container {
  const wrapper = new Container();
  if (title) wrapper.addChild(new Text(chalk.bold.cyan(` ${title}`), 0, 0));
  wrapper.addChild(new DynamicBorder());
  wrapper.addChild(list);
  wrapper.addChild(new DynamicBorder());
  wrapper.addChild(new Text(chalk.dim(" ↑↓ navigate · Enter select · Esc cancel"), 0, 0));
  return wrapper;
}

export async function runInteractive(opts: InteractiveOptions): Promise<void> {
  initTheme();

  // Register pi-style app keybindings so keyText("app.tools.expand") resolves to "Ctrl+O"
  // in tool / skill collapsed labels. Combine pi-tui defaults with our app actions.
  const APP_KEYBINDINGS = {
    "app.tools.expand": { defaultKeys: "ctrl+o", description: "Toggle tool output" },
    "app.interrupt": { defaultKeys: "escape", description: "Interrupt agent" },
    "app.clear": { defaultKeys: "ctrl+c", description: "Clear / exit" },
  } as const;
  setKeybindings(new KeybindingsManager({ ...TUI_KEYBINDINGS, ...APP_KEYBINDINGS } as never));

  const provider = (opts.provider ?? getActiveProvider() ?? "xai") as ProviderId;
  let modelId = opts.modelId ?? (settingsStore.get("defaultModel") as string) ?? `${provider}/grok-4`;
  let cwd = opts.cwd;

  const manager = new SessionManager();
  let session: Session = opts.sessionId
    ? await manager.open(opts.sessionId)
    : await manager.create({ cwd, provider, model: modelId });

  const tracker = new CostTracker();
  const commands = new CommandRegistry();
  registerBuiltins(commands, { cwd });

  const terminal = new ProcessTerminal();
  const tui = new TUI(terminal, true);

  const history = new ChatHistory(tui, cwd);
  const footer = new CostFooter();
  footer.setModel(modelId);
  footer.setSession(session.id);
  footer.setCost(tracker.format());

  async function refreshFooterCtx(): Promise<void> {
    const cat = await getCatalog();
    const info = cat[modelId];
    const used = tracker.sessionBreakdown().inputTokens + tracker.sessionBreakdown().cachedInputTokens;
    footer.setContext(used, info?.contextWindow ?? 0);
  }
  void refreshFooterCtx();

  const editor = new Editor(tui, editorTheme, { paddingX: 1 });

  // slash command autocomplete
  const slashItems: TuiSlashCommand[] = commands.list().map((c) => ({
    name: c.name,
    description: c.description,
  }));
  editor.setAutocompleteProvider(new CombinedAutocompleteProvider(slashItems, cwd));

  // pi pattern: editor lives in its own container so we can swap it out for selectors
  const editorContainer = new Container();
  editorContainer.addChild(editor);

  // pi pattern: status container between chat and editor for the "Working..." spinner.
  // We keep it at a fixed height (1 row spacer when idle) so editor never shifts.
  const statusContainer = new Container();
  const statusIdleSpacer = new Spacer(1);
  statusContainer.addChild(statusIdleSpacer);

  // pi-mono parity: pending messages queue shown above editor while agent runs
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

  // Pi-style selector swap: replaces editor area with selector component
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

  const selectOnce = (items: SelectItem[], title?: string): Promise<SelectItem | null> =>
    new Promise((resolve) => {
      if (!items.length) {
        resolve(null);
        return;
      }
      const visible = Math.min(items.length, 10);
      const list = new SelectList(items, visible, getSelectListTheme());
      const wrapper = buildSelectorWrapper(items, title, list);
      const close = showSelector(wrapper, list);
      let done = false;
      const finish = (v: SelectItem | null) => {
        if (done) return;
        done = true;
        close();
        resolve(v);
      };
      list.onSelect = (item) => finish(item);
      list.onCancel = () => finish(null);
    });

  const promptOnceLocal = (label?: string): Promise<string> =>
    new Promise((resolve) => {
      const tempEditor = new Editor(tui, editorTheme, { paddingX: 1 });
      const wrapper = new Container();
      if (label) wrapper.addChild(new Text(chalk.cyan(` ${label}`), 0, 0));
      wrapper.addChild(new DynamicBorder());
      wrapper.addChild(tempEditor);
      wrapper.addChild(new DynamicBorder());
      wrapper.addChild(new Text(chalk.dim(" Enter to submit · Esc to cancel"), 0, 0));
      const close = showSelector(wrapper, tempEditor as never);
      tempEditor.onSubmit = (text) => {
        close();
        resolve(text.trim());
      };
    });

  history.addSystem(`pi · ${modelId} · session ${session.id}`);
  history.addSystem(`Type /help for commands. Ctrl+C twice to quit.`);

  // Show workspace context + skills loaded on boot
  if ((settingsStore.get("workspaceContext") as boolean) !== false) {
    const ws = loadWorkspaceContext(cwd);
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
    const sk = loadProjectSkills(cwd);
    if (sk.skills.length > 0) {
      history.addSystem(chalk.dim(`skills (${sk.skills.length}):`));
      for (const s of sk.skills) {
        history.addSystem(chalk.dim(`  • ${s.name} — ${s.description.slice(0, 80)}`));
      }
    }
  }

  let abort = new AbortController();
  let busy = false;
  let pendingInjection: string | null = null;
  let lastCtrlCAt = 0;

  const cleanExit = (code = 0) => {
    tui.stop();
    process.exit(code);
  };

  const ctx = {
    cwd,
    emit: (event: string, data?: unknown) => {
      if (event === "help" || event === "error") history.addSystem(String(data ?? ""));
      if (event === "inject-prompt") pendingInjection = String(data ?? "");
      if (event === "inject-skill") {
        // Skill invocations fire a turn immediately (pi-mono behavior)
        const text = String(data ?? "");
        if (text && editor.onSubmit) void editor.onSubmit(text);
      }
      tui.requestRender();
    },
    setModel: async (id: string) => {
      const resolved = await resolveModelId(id);
      if (!resolved) {
        history.addSystem(chalk.red(`unknown model: ${id} — try /model to pick from a list`));
        tui.requestRender();
        return;
      }
      modelId = resolved;
      settingsStore.set("defaultModel", resolved);
      footer.setModel(resolved);
      await refreshFooterCtx();
      history.addSystem(`model → ${resolved}`);
      tui.requestRender();
    },
    setProvider: async (p?: string) => {
      const authed = listAuthorizedProviders();
      const all = [...authed];
      if (all.length === 0) {
        history.addSystem(chalk.yellow("no providers authenticated. /login first."));
        tui.requestRender();
        return;
      }
      let target = p;
      if (!target) {
        const items: SelectItem[] = all.map((id) => ({
          value: id,
          label: id,
          description: id === getActiveProvider() ? "(active)" : "",
        }));
        const pick = await selectOnce(items);
        if (!pick) return;
        target = pick.value;
      }
      if (!all.includes(target)) {
        history.addSystem(chalk.red(`not authorized: ${target}. /login ${target} first.`));
        tui.requestRender();
        return;
      }
      setActiveProvider(target);
      // pick a default model for this provider
      const cat = await getCatalog();
      const first = Object.values(cat).find((m) => m.provider === target && m.available);
      if (first) {
        modelId = first.id;
        settingsStore.set("defaultModel", modelId);
        footer.setModel(modelId);
      }
      history.addSystem(`provider → ${target}${first ? `, model → ${first.id}` : ""}`);
      tui.requestRender();
    },
    newSession: async () => {
      session = await manager.create({ cwd, provider, model: modelId });
      footer.setSession(session.id);
      tracker.reset();
      footer.setCost(tracker.format());
      await refreshFooterCtx();
      queuedMessages.length = 0;
      renderPending();
      history.reset();
      history.addSystem(`new session ${session.id}`);
      tui.requestRender();
    },
    clearScreen: () => {
      // ANSI: clear scrollback + screen, then full redraw
      process.stdout.write("\x1b[3J\x1b[2J\x1b[H");
      tracker.reset();
      footer.setCost(tracker.format());
      void refreshFooterCtx();
      history.reset();
      tui.invalidate();
      tui.requestRender(true);
    },
    manualCompact: async () => {
      const result = await runCompact({ session, modelId });
      history.addSystem(`compacted ${result.cutAt} turns: ${result.tokensBefore} → ${result.tokensAfter} tokens`);
      tui.requestRender();
    },
    showCost: () => {
      const s = tracker.sessionBreakdown();
      const l = tracker.lifetimeBreakdown();
      history.addSystem(`session: $${s.usd.toFixed(4)}  lifetime: $${l.usd.toFixed(4)}`);
      tui.requestRender();
    },
    showSessions: async () => {
      const sessions = manager.list(cwd);
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
        session = await manager.open(pick.value);
        footer.setSession(session.id);
        history.reset();
        history.addSystem(`resumed session ${session.id}`);
        // replay messages briefly
        for (const e of session.entries()) {
          if (e.type === "message") {
            const role = (e as { role: string }).role;
            const content = String((e as { content: unknown }).content ?? "");
            if (role === "user") history.addUser(content);
            else if (role === "assistant") {
              history.ensureAssistant(parseModelId(modelId).provider, modelId);
              history.appendAssistantDelta(content, parseModelId(modelId).provider, modelId);
              history.finishAssistant();
            }
          }
        }
      } catch (err) {
        history.addError(`open failed: ${(err as Error).message}`);
      }
      tui.requestRender();
    },
    exit: () => cleanExit(0),
    setCwd: (p: string) => {
      cwd = p;
      history.addSystem(`cwd → ${cwd}`);
      tui.requestRender();
    },
    startLogin: async (target?: string) => {
      let p: ProviderId | null = null;
      if (target) {
        if (!(PROVIDER_IDS as readonly string[]).includes(target)) {
          history.addSystem(`unknown provider: ${target}. options: ${PROVIDER_IDS.join(", ")}`);
          tui.requestRender();
          return;
        }
        p = target as ProviderId;
      } else {
        const items: SelectItem[] = PROVIDER_IDS.map((id) => ({
          value: id,
          label: id,
          description: providerLabel(id),
        }));
        const pick = await selectOnce(items, "Sign in to provider");
        if (!pick) return;
        p = pick.value as ProviderId;
      }

      if (p === "xai") {
        // ask subscription vs api key
        const mode = await selectOnce([
          {
            value: "oauth",
            label: "Sign in with xAI (SuperGrok subscription)",
            description: "OAuth, opens browser",
          },
          { value: "apikey", label: "Use API key", description: "Paste your XAI_API_KEY" },
        ]);
        if (!mode) return;
        if (mode.value === "oauth") {
          history.addSystem("xAI login: launching OAuth in your browser…");
          tui.requestRender();
          try {
            await loginXaiOAuth(({ url, instructions }) => {
              history.addSystem(instructions);
              history.addSystem(chalk.cyan(url));
              tui.requestRender();
            });
            setActiveProvider("xai");
            history.addSystem(chalk.green("✓ xAI subscription connected."));
          } catch (err) {
            history.addError(`xAI login failed: ${(err as Error).message}`);
          }
          tui.requestRender();
          return;
        }
      }
      history.addSystem(`${p}: paste API key, then press Enter.`);
      tui.requestRender();
      const key = await promptOnceLocal(`${p.toUpperCase()}_API_KEY: `);
      if (key) {
        loginApiKey(p, key);
        setActiveProvider(p);
        history.addSystem(chalk.green(`✓ ${p} key saved.`));
        tui.requestRender();
      }
    },
    startLogout: async (target?: string) => {
      let pick = target;
      if (!pick) {
        const authed = listAuthorizedProviders();
        if (authed.length === 0) {
          history.addSystem("no providers to sign out from");
          tui.requestRender();
          return;
        }
        const items: SelectItem[] = [
          ...authed.map((id) => ({ value: id, label: id, description: id === getActiveProvider() ? "(active)" : "" })),
          { value: "__all__", label: chalk.red("all providers"), description: "Sign out from every provider" },
        ];
        const sel = await selectOnce(items);
        if (!sel) return;
        pick = sel.value;
      }
      if (pick === "__all__") {
        logout();
        history.addSystem("signed out of all providers");
      } else {
        logout(pick as ProviderId);
        history.addSystem(`signed out of ${pick}`);
      }
      tui.requestRender();
    },
    openSettings: async () => {
      const items: SelectItem[] = [
        { value: "theme", label: `theme: ${settingsStore.get("theme") ?? "dark"}` },
        { value: "maxSteps", label: `maxSteps: ${settingsStore.get("maxSteps") ?? 32}` },
        { value: "autoCompactThreshold", label: `autoCompactThreshold: ${settingsStore.get("autoCompactThreshold") ?? 0.8}` },
        { value: "piCompatMode", label: `piCompatMode: ${settingsStore.get("piCompatMode") ?? "fork"}` },
        { value: "workspaceContext", label: `workspaceContext: ${settingsStore.get("workspaceContext") ?? true}` },
      ];
      const pick = await selectOnce(items);
      if (!pick) return;
      history.addSystem(`enter new value for ${pick.value}:`);
      tui.requestRender();
      const v = await promptOnceLocal("");
      if (!v) return;
      const key = pick.value;
      const cur = settingsStore.get(key);
      const parsed = typeof cur === "number" ? Number(v) : typeof cur === "boolean" ? v === "true" : v;
      settingsStore.set(key, parsed);
      history.addSystem(`${key} → ${parsed}`);
      tui.requestRender();
    },
    openModelPicker: async () => {
      const cat = await getCatalog();
      const active = (getActiveProvider() ?? provider) as ProviderId;
      const items: SelectItem[] = Object.values(cat)
        .filter((m) => m.provider === active && m.available)
        .sort((a, b) => a.id.localeCompare(b.id))
        .map((m) => ({
          value: m.id,
          label: m.id.slice(active.length + 1),
          description: `${m.name}  ·  ctx ${m.contextWindow.toLocaleString()}  ·  $${m.cost.input}/$${m.cost.output}`,
        }));
      if (items.length === 0) {
        history.addSystem(chalk.yellow(`no models available for ${active}. Try /login ${active} first.`));
        tui.requestRender();
        return;
      }
      const pick = await selectOnce(items);
      if (!pick) return;
      modelId = pick.value;
      settingsStore.set("defaultModel", modelId);
      footer.setModel(modelId);
      history.addSystem(`model → ${modelId}`);
      tui.requestRender();
    },
    showSessionInfo: () => {
      const s = tracker.sessionBreakdown();
      history.addSystem(`session id   ${session.id}`);
      history.addSystem(`model        ${modelId}`);
      history.addSystem(`provider     ${provider}`);
      history.addSystem(`cwd          ${cwd}`);
      history.addSystem(`tokens       in:${s.inputTokens} out:${s.outputTokens} cache:${s.cachedInputTokens}`);
      history.addSystem(`cost (sess)  $${s.usd.toFixed(4)}`);
      tui.requestRender();
    },
    showHotkeys: () => {
      const lines = [
        "Enter           submit",
        "Shift+Enter     newline",
        "Tab             autocomplete",
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
    copyLastAssistant: async () => {
      const entries = session.entries();
      const last = [...entries].reverse().find((e) => e.type === "message" && (e as { role?: string }).role === "assistant");
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
    attachImage: async (givenPath?: string) => {
      // Capability gate — only attach if active model accepts images
      const cat = await getCatalog();
      const info = cat[modelId];
      if (info && Array.isArray(info.modalities) && !info.modalities.includes("image")) {
        history.addSystem(
          chalk.yellow(`${modelId} does not accept images. Pick a vision model via /model first.`),
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
      // Wrap path in [image:...] so a leading "/" path doesn't get mistaken
      // for a slash command on submit, while keeping the path readable in
      // the transcript.
      const token = `[image:${path}]`;
      const current = editor.getText?.() ?? "";
      const sep = current && !current.endsWith(" ") ? " " : "";
      editor.setText?.(`${current}${sep}${token} `);
      tui.requestRender();
    },
    setSessionName: (name: string) => {
      settingsStore.set(`sessionName.${session.id}`, name);
      history.addSystem(`session name → ${name}`);
      tui.requestRender();
    },
    exportSession: async (target?: string) => {
      const out = target ?? `${session.id}.jsonl`;
      const entries = session.entries();
      const content = entries.map((e) => JSON.stringify(e)).join("\n");
      writeFileSync(out, content);
      history.addSystem(`exported to ${out}`);
      tui.requestRender();
    },
    importSession: async (path: string) => {
      try {
        const lines = readFileSync(path, "utf8").split("\n").filter(Boolean);
        const ns = await manager.create({ cwd, provider, model: modelId });
        for (const line of lines) {
          try {
            await ns.append(JSON.parse(line));
          } catch {}
        }
        session = ns;
        footer.setSession(session.id);
        history.addSystem(`imported ${lines.length} entries → session ${session.id}`);
      } catch (err) {
        history.addError(`import failed: ${(err as Error).message}`);
      }
      tui.requestRender();
    },
    reload: async () => {
      commands.list().forEach(() => {});
      // re-register builtins + user prompts from disk
      const fresh = new CommandRegistry();
      registerBuiltins(fresh, { cwd });
      // swap into existing registry
      (commands as unknown as { commands: Map<string, unknown> }).commands = (
        fresh as unknown as { commands: Map<string, unknown> }
      ).commands;
      const items: TuiSlashCommand[] = commands.list().map((c) => ({ name: c.name, description: c.description }));
      editor.setAutocompleteProvider(new CombinedAutocompleteProvider(items, cwd));
      history.addSystem("reloaded prompts + commands");
      tui.requestRender();
    },
    stub: (name: string) => {
      history.addSystem(chalk.yellow(`/${name} not implemented yet`));
      tui.requestRender();
    },
  };

  async function resolveModelId(input: string): Promise<string | null> {
    const cat = await getCatalog();
    if (cat[input]) return input;
    if (!input.includes("/")) {
      const active = (getActiveProvider() ?? provider) as ProviderId;
      const candidate = `${active}/${input}`;
      if (cat[candidate]) return candidate;
      // search across providers
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

  const isCtrlC = (d: string) => d === CTRL_C || matchesKey(d, "ctrl+c");
  const isCtrlD = (d: string) => d === CTRL_D || matchesKey(d, "ctrl+d");
  const isCtrlL = (d: string) => d === "\x0c" || matchesKey(d, "ctrl+l");
  const isCtrlO = (d: string) => d === "\x0f" || matchesKey(d, "ctrl+o");
  const isCtrlV = (d: string) => d === "\x16" || matchesKey(d, "ctrl+v");
  // NOTE: \x09 is TAB which legacy terminals share with Ctrl+I — we only match
  // the Kitty-protocol Ctrl+I so autocomplete (Tab) keeps working.
  const isCtrlI = (d: string) => matchesKey(d, "ctrl+i");
  const isEsc = (d: string) => d === ESC || matchesKey(d, "escape");

  // Input listeners (run before editor)
  tui.addInputListener((data) => {
    if (isCtrlL(data)) {
      ctx.clearScreen();
      return { consume: true };
    }
    if (isCtrlO(data)) {
      const now = history.toggleToolsExpanded();
      history.addSystem(`tools ${now ? "expanded" : "collapsed"}`);
      tui.requestRender();
      return { consume: true };
    }
    if (isCtrlV(data)) {
      // Try clipboard image. If found, stage it. If no image, let Ctrl+V flow
      // through to the editor for normal text paste.
      const path = readClipboardImageToFile();
      if (path) {
        void ctx.attachImage(path);
        return { consume: true };
      }
    }
    if (isCtrlI(data)) {
      const path = pickImageFile();
      if (path) void ctx.attachImage(path);
      return { consume: true };
    }
    if (isCtrlD(data) && !busy) {
      cleanExit(0);
      return { consume: true };
    }
    if (isCtrlC(data)) {
      if (busy) {
        abort.abort();
        abort = new AbortController();
        busy = false;
        hideWorking();
        queuedMessages.length = 0;
        renderPending();
        tui.requestRender();
        return { consume: true };
      }
      const now = Date.now();
      if (now - lastCtrlCAt < 1000) {
        cleanExit(130);
        return { consume: true };
      }
      lastCtrlCAt = now;
      history.addSystem("Press Ctrl+C again to quit.");
      tui.requestRender();
      return { consume: true };
    }
    if (isEsc(data) && busy) {
      abort.abort();
      abort = new AbortController();
      busy = false;
      hideWorking();
      tui.requestRender();
      return { consume: true };
    }
    return undefined;
  });

  editor.onSubmit = async (text: string) => {
    text = text.trim();
    if (!text) return;

    // Slash commands always run inline (no queueing)
    if (text.startsWith("/")) {
      const handled = await commands.run(text, ctx);
      if (!handled) {
        history.addSystem(`unknown command: ${text}`);
        tui.requestRender();
      }
      return;
    }

    // If agent is busy, queue this message and run it after the current turn ends
    if (busy) {
      queuedMessages.push(text);
      renderPending();
      tui.requestRender();
      return;
    }

    const finalInput = pendingInjection ? `${pendingInjection}\n\n${text}` : text;
    pendingInjection = null;

    busy = true;
    history.addUser(finalInput);
    showWorking("Generating");
    tui.requestRender();

    const { provider: turnProvider } = parseModelId(modelId);
    history.ensureAssistant(turnProvider, modelId);
    const emitter = new EventEmitter();
    emitter.on("text-delta", (t: string) => {
      history.appendAssistantDelta(t, turnProvider, modelId);
      tui.requestRender();
    });
    emitter.on("tool-call", (part: { toolName?: string; input?: unknown; toolCallId?: string }) => {
      const id = part.toolCallId ?? `${part.toolName}-${Date.now()}`;
      history.addToolCall(part.toolName ?? "tool", id, (part.input ?? {}) as Record<string, unknown>);
      showWorking(`Running ${part.toolName}…`);
      tui.requestRender();
    });
    emitter.on("tool-result", (part: { output?: unknown; toolCallId?: string }) => {
      history.addToolResult(part.toolCallId ?? "", part.output);
      showWorking("Generating");
      tui.requestRender();
    });
    emitter.on("compact-start", () => {
      history.addSystem("[compacting…]");
      tui.requestRender();
    });
    emitter.on("compact-end", (r: { tokensBefore: number; tokensAfter: number }) => {
      history.addSystem(`[compacted ${r.tokensBefore} → ${r.tokensAfter} tokens]`);
      tui.requestRender();
    });
    emitter.on("finish", () => {
      history.finishAssistant();
      footer.setCost(tracker.format());
      void refreshFooterCtx();
      tui.requestRender();
    });
    emitter.on("error", (err: unknown) => {
      history.addError(String(err));
      tui.requestRender();
    });

    try {
      await runTurn({
        session,
        modelId,
        userInput: finalInput,
        cwd,
        abortSignal: abort.signal,
        tracker,
        emitter,
      });
    } catch (err) {
      history.addError((err as Error).message);
    } finally {
      busy = false;
      history.finishAssistant();
      hideWorking();
      tui.requestRender();
      // Drain queued follow-up messages (FIFO). Each runs as a fresh turn.
      const next = queuedMessages.shift();
      if (next !== undefined) {
        renderPending();
        if (editor.onSubmit) void editor.onSubmit(next);
      }
    }
  };

  tui.setFocus(editor);
  tui.start();
  tui.requestRender();
}

function providerLabel(id: string): string {
  switch (id) {
    case "xai":
      return "xAI (Grok) — OAuth subscription or API key";
    case "anthropic":
      return "Anthropic — API key";
    case "openai":
      return "OpenAI — API key";
    case "google":
      return "Google — API key";
    case "openrouter":
      return "OpenRouter — API key";
    default:
      return "";
  }
}
