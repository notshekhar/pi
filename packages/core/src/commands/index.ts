import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { getPiDir } from "../auth/storage";

export interface CommandContext {
  emit(event: string, data?: unknown): void;
  setModel(modelId: string): Promise<void> | void;
  setProvider(p?: string): Promise<void> | void;
  newSession(): Promise<void>;
  manualCompact(): Promise<void>;
  showCost(): void;
  showSessions(): Promise<void>;
  exit(): void;
  cwd: string;
  setCwd(p: string): void;
  startLogin(provider?: string): Promise<void>;
  startLogout(provider?: string): Promise<void> | void;
  openSettings(): Promise<void>;
  openModelPicker(): Promise<void>;
  showSessionInfo(): void;
  showHotkeys(): void;
  copyLastAssistant(): Promise<void>;
  setSessionName(name: string): void;
  exportSession(target?: string): Promise<void>;
  importSession(path: string): Promise<void>;
  reload(): Promise<void>;
  stub(name: string): void;
  clearScreen(): void;
}

export interface SlashCommand {
  name: string;
  description: string;
  handler: (ctx: CommandContext, rawArgs: string) => Promise<void> | void;
}

export class CommandRegistry {
  private commands = new Map<string, SlashCommand>();

  register(cmd: SlashCommand): void {
    this.commands.set(cmd.name, cmd);
  }

  has(name: string): boolean {
    return this.commands.has(name);
  }

  list(): SlashCommand[] {
    return [...this.commands.values()];
  }

  async run(input: string, ctx: CommandContext): Promise<boolean> {
    if (!input.startsWith("/")) return false;
    const space = input.indexOf(" ");
    const name = (space < 0 ? input.slice(1) : input.slice(1, space)).trim();
    const args = space < 0 ? "" : input.slice(space + 1).trim();
    const cmd = this.commands.get(name);
    if (!cmd) return false;
    await cmd.handler(ctx, args);
    return true;
  }
}

export function registerBuiltins(reg: CommandRegistry): void {
  const cmds: SlashCommand[] = [
    { name: "help", description: "Show available commands", handler: (ctx) => {
      const lines = reg.list().map((c) => `/${c.name} — ${c.description}`);
      ctx.emit("help", lines.join("\n"));
    } },
    { name: "login", description: "Configure provider authentication", handler: async (ctx, args) => {
      await ctx.startLogin(args || undefined);
    } },
    { name: "logout", description: "Remove provider authentication (opens picker)", handler: async (ctx, args) => {
      await ctx.startLogout(args || undefined);
    } },
    { name: "model", description: "Select model (opens picker, or /model provider/id)", handler: async (ctx, args) => {
      if (args) await ctx.setModel(args);
      else await ctx.openModelPicker();
    } },
    { name: "provider", description: "Switch active provider (opens picker, or /provider <id>)", handler: async (ctx, args) => {
      await ctx.setProvider(args || undefined);
    } },
    { name: "new", description: "Start a new session", handler: async (ctx) => {
      await ctx.newSession();
    } },
    { name: "clear", description: "Clear screen scrollback", handler: (ctx) => {
      ctx.clearScreen();
    } },
    { name: "compact", description: "Manually compact the session context", handler: async (ctx) => {
      await ctx.manualCompact();
    } },
    { name: "resume", description: "Resume a different session", handler: async (ctx) => {
      await ctx.showSessions();
    } },
    { name: "sessions", description: "Alias for /resume", handler: async (ctx) => {
      await ctx.showSessions();
    } },
    { name: "session", description: "Show session info and stats", handler: (ctx) => {
      ctx.showSessionInfo();
    } },
    { name: "name", description: "Set session display name", handler: (ctx, args) => {
      if (!args) return ctx.emit("error", "usage: /name <name>");
      ctx.setSessionName(args);
    } },
    { name: "cost", description: "Show session + lifetime cost", handler: (ctx) => ctx.showCost() },
    { name: "cwd", description: "Change working directory", handler: (ctx, args) => {
      if (!args) return ctx.emit("error", "usage: /cwd <path>");
      ctx.setCwd(args);
    } },
    { name: "copy", description: "Copy last assistant message to clipboard", handler: async (ctx) => {
      await ctx.copyLastAssistant();
    } },
    { name: "export", description: "Export session (path optional, .jsonl/.html)", handler: async (ctx, args) => {
      await ctx.exportSession(args || undefined);
    } },
    { name: "import", description: "Import a session from a JSONL file", handler: async (ctx, args) => {
      if (!args) return ctx.emit("error", "usage: /import <path>");
      await ctx.importSession(args);
    } },
    { name: "settings", description: "Open settings menu", handler: async (ctx) => {
      await ctx.openSettings();
    } },
    { name: "hotkeys", description: "Show all keyboard shortcuts", handler: (ctx) => {
      ctx.showHotkeys();
    } },
    { name: "reload", description: "Reload prompts, keybindings, settings", handler: async (ctx) => {
      await ctx.reload();
    } },
    { name: "changelog", description: "Show changelog entries", handler: (ctx) => ctx.stub("changelog") },
    { name: "fork", description: "Create a new fork from a previous user message", handler: (ctx) => ctx.stub("fork") },
    { name: "clone", description: "Duplicate the current session at current position", handler: (ctx) => ctx.stub("clone") },
    { name: "tree", description: "Navigate session tree (switch branches)", handler: (ctx) => ctx.stub("tree") },
    { name: "share", description: "Share session as a secret GitHub gist", handler: (ctx) => ctx.stub("share") },
    { name: "scoped-models", description: "Enable/disable models for Ctrl+P cycling", handler: (ctx) => ctx.stub("scoped-models") },
    { name: "quit", description: "Quit pi-agent", handler: (ctx) => ctx.exit() },
    { name: "exit", description: "Alias for /quit", handler: (ctx) => ctx.exit() },
  ];

  for (const c of cmds) reg.register(c);

  // user prompts as commands
  const promptsDir = join(getPiDir(), "agent", "prompts");
  if (existsSync(promptsDir)) {
    for (const file of readdirSync(promptsDir)) {
      if (!file.endsWith(".md")) continue;
      const name = file.replace(/\.md$/, "");
      const path = join(promptsDir, file);
      reg.register({
        name,
        description: `Custom prompt from ${file}`,
        handler: (ctx) => {
          const body = readFileSync(path, "utf8");
          ctx.emit("inject-prompt", body);
        },
      });
    }
  }
}
