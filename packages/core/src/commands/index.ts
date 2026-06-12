import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { getPiDir } from "../auth/storage";
import { loadProjectSkills } from "../agent/skills";
import { listAgents } from "../agent/agents";

export interface CommandContext {
    emit(event: string, data?: unknown): void;
    setModel(modelId: string): Promise<void> | void;
    setProvider(p?: string): Promise<void> | void;
    newSession(): Promise<void>;
    manualCompact(): Promise<void>;
    setThinking(level?: string): Promise<void> | void;
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
    attachImage(path?: string): Promise<void> | void;
    exportSession(target?: string): Promise<void>;
    importSession(path: string): Promise<void>;
    reload(): Promise<void>;
    showChangelog(): void;
    manageAgents(): Promise<void>;
    manageHooks(): Promise<void>;
    /** With message: run that one message under this agent's prompt (one-shot). */
    useAgent(name: string, message?: string): Promise<void> | void;
    stub(name: string): void;
    clearScreen(): void;
    /** /fork — pick a previous user message, branch it into a new session. */
    forkFromMessage(): void;
    /** /clone — duplicate the current branch into a new session. */
    cloneSession(): Promise<void> | void;
    /** /tree — navigate the session tree (switch branches). */
    showTree(): void;
    /** /update — self-update to the latest release (exits on success). */
    updateApp(): Promise<void>;
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

    unregister(name: string): boolean {
        return this.commands.delete(name);
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

export async function registerBuiltins(reg: CommandRegistry, opts: { cwd?: string } = {}): Promise<void> {
    const cwd = opts.cwd ?? process.cwd();
    const cmds: SlashCommand[] = [
        {
            name: "help",
            description: "Show available commands",
            handler: (ctx) => {
                const lines = reg.list().map((c) => `/${c.name} — ${c.description}`);
                ctx.emit("help", lines.join("\n"));
            },
        },
        {
            name: "login",
            description: "Configure provider authentication",
            handler: async (ctx, args) => {
                await ctx.startLogin(args || undefined);
            },
        },
        {
            name: "logout",
            description: "Remove provider authentication (opens picker)",
            handler: async (ctx, args) => {
                await ctx.startLogout(args || undefined);
            },
        },
        {
            name: "model",
            description: "Select model (opens picker, or /model provider/id)",
            handler: async (ctx, args) => {
                if (args) await ctx.setModel(args);
                else await ctx.openModelPicker();
            },
        },
        {
            name: "provider",
            description: "Switch active provider (opens picker, or /provider <id>)",
            handler: async (ctx, args) => {
                await ctx.setProvider(args || undefined);
            },
        },
        {
            name: "new",
            description: "Start a new session",
            handler: async (ctx) => {
                await ctx.newSession();
            },
        },
        {
            name: "clear",
            description: "Start a new session (clears screen)",
            handler: async (ctx) => {
                ctx.clearScreen();
                await ctx.newSession();
            },
        },
        {
            name: "compact",
            description: "Manually compact the session context",
            handler: async (ctx) => {
                await ctx.manualCompact();
            },
        },
        {
            name: "thinking",
            description: "Set reasoning/thinking level (off|minimal|low|medium|high|xhigh)",
            handler: async (ctx, args) => {
                await ctx.setThinking(args || undefined);
            },
        },
        {
            name: "resume",
            description: "Resume a different session",
            handler: async (ctx) => {
                await ctx.showSessions();
            },
        },
        {
            name: "sessions",
            description: "Alias for /resume",
            handler: async (ctx) => {
                await ctx.showSessions();
            },
        },
        {
            name: "session",
            description: "Show session info and stats",
            handler: (ctx) => {
                ctx.showSessionInfo();
            },
        },
        {
            name: "name",
            description: "Set session display name (no arg = rename prompt)",
            handler: (ctx, args) => ctx.setSessionName(args ?? ""),
        },
        {
            name: "rename",
            description: "Alias for /name",
            handler: (ctx, args) => ctx.setSessionName(args ?? ""),
        },
        {
            name: "cost",
            description: "Show cost breakdown (session, directory, today, 7d, month, lifetime)",
            handler: (ctx) => ctx.showCost(),
        },
        {
            name: "attach",
            description: "Attach an image (paste from clipboard, or /attach <path>)",
            handler: async (ctx, args) => {
                await ctx.attachImage(args || undefined);
            },
        },
        {
            name: "paste",
            description: "Alias for /attach (paste image from clipboard)",
            handler: async (ctx) => {
                await ctx.attachImage();
            },
        },
        {
            name: "cwd",
            description: "Change working directory",
            handler: (ctx, args) => {
                if (!args) return ctx.emit("error", "usage: /cwd <path>");
                ctx.setCwd(args);
            },
        },
        {
            name: "copy",
            description: "Copy last assistant message to clipboard",
            handler: async (ctx) => {
                await ctx.copyLastAssistant();
            },
        },
        {
            name: "export",
            description: "Export session (path optional, .jsonl/.html)",
            handler: async (ctx, args) => {
                await ctx.exportSession(args || undefined);
            },
        },
        {
            name: "import",
            description: "Import a session from a JSONL file",
            handler: async (ctx, args) => {
                if (!args) return ctx.emit("error", "usage: /import <path>");
                await ctx.importSession(args);
            },
        },
        {
            name: "settings",
            description: "Open settings menu",
            handler: async (ctx) => {
                await ctx.openSettings();
            },
        },
        {
            name: "hotkeys",
            description: "Show all keyboard shortcuts",
            handler: (ctx) => {
                ctx.showHotkeys();
            },
        },
        {
            name: "reload",
            description: "Reload prompts, keybindings, settings",
            handler: async (ctx) => {
                await ctx.reload();
            },
        },
        {
            name: "update",
            description: "Update pi to the latest release",
            handler: async (ctx) => {
                await ctx.updateApp();
            },
        },
        { name: "changelog", description: "Show changelog entries", handler: (ctx) => ctx.showChangelog() },
        {
            name: "agents",
            description: "Create, select, or edit agents (custom system prompts)",
            handler: async (ctx) => {
                await ctx.manageAgents();
            },
        },
        {
            name: "hooks",
            description: "List, add, and remove lifecycle hooks (pi-owned and imported)",
            handler: async (ctx) => {
                await ctx.manageHooks();
            },
        },
        {
            name: "fork",
            description: "Create a new fork from a previous user message",
            handler: (ctx) => ctx.forkFromMessage(),
        },
        {
            name: "clone",
            description: "Duplicate the current session at current position",
            handler: async (ctx) => {
                await ctx.cloneSession();
            },
        },
        { name: "tree", description: "Navigate session tree (switch branches)", handler: (ctx) => ctx.showTree() },
        { name: "share", description: "Share session as a secret GitHub gist", handler: (ctx) => ctx.stub("share") },
        {
            name: "scoped-models",
            description: "Enable/disable models for Ctrl+P cycling",
            handler: (ctx) => ctx.stub("scoped-models"),
        },
        { name: "quit", description: "Quit pi-agent", handler: (ctx) => ctx.exit() },
        { name: "exit", description: "Alias for /quit", handler: (ctx) => ctx.exit() },
    ];

    for (const c of cmds) reg.register(c);

    // skills as /skill:name commands (pi-mono parity)
    try {
        const sk = await loadProjectSkills(cwd);
        for (const skill of sk.skills) {
            const name = `skill:${skill.name}`;
            reg.register({
                name,
                description: skill.description.slice(0, 100),
                handler: (ctx, args) => {
                    let content: string;
                    try {
                        content = readFileSync(skill.filePath, "utf8");
                        content = content.replace(/^---[\s\S]*?---\s*\n/, "").trim();
                    } catch (e) {
                        ctx.emit("error", `failed reading skill ${skill.name}: ${(e as Error).message}`);
                        return;
                    }
                    // pi-mono skill block format — must match parseSkillBlock regex exactly
                    const block = `<skill name="${skill.name}" location="${skill.filePath}">\nReferences are relative to ${skill.baseDir}.\n\n${content}\n</skill>`;
                    const text = args ? `${block}\n\n${args}` : block;
                    ctx.emit("inject-skill", text);
                },
            });
        }
    } catch {}

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

    // agents as commands: /<name> <message> runs one message under that agent.
    // "default" gets no command — it's what plain messages already use.
    for (const agent of listAgents()) {
        if (agent.name === "default") continue;
        registerAgentCommand(reg, agent.name);
    }
}

/** Register /<name> for an agent. Skipped when the name collides with an existing command. */
export function registerAgentCommand(reg: CommandRegistry, name: string): boolean {
    if (reg.has(name)) return false;
    reg.register({
        name,
        description: `Run one message with agent "${name}": /${name} <message>`,
        handler: (ctx, args) => ctx.useAgent(name, args || undefined),
    });
    return true;
}
