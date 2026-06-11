import { Container, Markdown, Spacer, Text, type TUI } from "@notshekhar/pi-tui";
import { getMarkdownTheme } from "../ui/theme";
import {
    AssistantMessageComponent,
    CompactionSummaryMessageComponent,
    parseSkillBlock,
    SkillInvocationMessageComponent,
    UserMessageComponent,
} from "../ui/messages";
import { ToolExecutionComponent } from "../ui/tool-execution";
import chalk from "chalk";

const HOOK_ORANGE = chalk.hex("#e09956");

interface PiAssistantMessage {
    role: "assistant";
    content: Array<
        | { type: "text"; text: string }
        | { type: "thinking"; thinking: string }
        | { type: "toolCall"; id: string; name: string; arguments: Record<string, unknown> }
    >;
    api: string;
    provider: string;
    model: string;
    usage: {
        input: number;
        output: number;
        cacheRead: number;
        cacheWrite: number;
        totalTokens: number;
        cost: { input: number; output: number; cacheRead: number; cacheWrite: number; total: number };
    };
    stopReason: "stop" | "length" | "toolUse" | "error" | "aborted";
    timestamp: number;
}

function emptyAssistantMessage(provider: string, model: string): PiAssistantMessage {
    return {
        role: "assistant",
        content: [],
        api: "openai",
        provider,
        model,
        usage: {
            input: 0,
            output: 0,
            cacheRead: 0,
            cacheWrite: 0,
            totalTokens: 0,
            cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
        },
        stopReason: "stop",
        timestamp: Date.now(),
    };
}

export class ChatHistory extends Container {
    private liveMsg: PiAssistantMessage | null = null;
    private liveComponent: AssistantMessageComponent | null = null;
    private toolComponents = new Map<string, ToolExecutionComponent>();
    private allToolComponents: ToolExecutionComponent[] = [];
    private skillComponents: SkillInvocationMessageComponent[] = [];
    private compactionComponents: CompactionSummaryMessageComponent[] = [];
    private assistantTurn: Container | null = null;
    private expanded = false;

    constructor(
        private tui: TUI,
        private cwd: string,
    ) {
        super();
        void this.tui;
        void this.cwd;
    }

    setToolsExpanded(expanded: boolean): void {
        this.expanded = expanded;
        for (const c of this.allToolComponents) c.setExpanded(expanded);
        for (const c of this.skillComponents) c.setExpanded(expanded);
        for (const c of this.compactionComponents) c.setExpanded(expanded);
    }
    toggleToolsExpanded(): boolean {
        this.setToolsExpanded(!this.expanded);
        return this.expanded;
    }

    reset(): void {
        this.clear();
        this.liveMsg = null;
        this.liveComponent = null;
        this.toolComponents.clear();
        this.allToolComponents = [];
        this.skillComponents = [];
        this.compactionComponents = [];
        this.assistantTurn = null;
    }

    addUser(text: string): void {
        this.addChild(new Spacer(1));
        // SessionStart hook context is model-facing — collapse it to a dim notice
        // instead of rendering it as part of what the user typed. Applies to live
        // turns and to transcript replay on resume alike.
        const hookCtx = text.match(/^<session-start-hook-context>\n([\s\S]*?)\n<\/session-start-hook-context>\n*/);
        if (hookCtx) {
            const lines = hookCtx[1].split("\n").length;
            this.addChild(new Text(HOOK_ORANGE(`session-start hook context attached (${lines} lines)`), 1, 0));
            text = text.slice(hookCtx[0].length);
            if (!text) {
                this.assistantTurn = null;
                return;
            }
            this.addChild(new Spacer(1));
        }
        const skill = parseSkillBlock(text);
        if (skill) {
            const comp = new SkillInvocationMessageComponent(skill);
            comp.setExpanded(this.expanded);
            this.addChild(comp);
            this.skillComponents.push(comp);
            if (skill.userMessage) {
                this.addChild(new UserMessageComponent(skill.userMessage));
            }
        } else {
            this.addChild(new UserMessageComponent(text));
        }
        this.assistantTurn = null;
    }

    ensureAssistant(provider: string, model: string): void {
        if (!this.assistantTurn) {
            this.assistantTurn = new Container();
            this.addChild(new Spacer(1));
            this.addChild(this.assistantTurn);
        }
        if (this.liveComponent) return;
        this.liveMsg = emptyAssistantMessage(provider, model);
        this.liveComponent = new AssistantMessageComponent(this.liveMsg);
        this.assistantTurn.addChild(this.liveComponent);
    }

    appendAssistantDelta(text: string, provider: string, model: string): void {
        this.ensureAssistant(provider, model);
        const msg = this.liveMsg!;
        const last = msg.content[msg.content.length - 1];
        if (last && last.type === "text") {
            last.text += text;
        } else {
            msg.content.push({ type: "text", text });
        }
        this.liveComponent!.updateContent(msg);
    }

    appendAssistantThinking(text: string, provider: string, model: string): void {
        this.ensureAssistant(provider, model);
        const msg = this.liveMsg!;
        const last = msg.content[msg.content.length - 1];
        if (last && last.type === "thinking") {
            last.thinking += text;
        } else {
            msg.content.push({ type: "thinking", thinking: text });
        }
        this.liveComponent!.updateContent(msg);
    }

    finishAssistant(stopReason: PiAssistantMessage["stopReason"] = "stop"): void {
        if (this.liveMsg) {
            this.liveMsg.stopReason = stopReason;
            this.liveComponent?.updateContent(this.liveMsg);
        }
        this.liveMsg = null;
        this.liveComponent = null;
    }

    updateToolCallInput(toolCallId: string, args: Record<string, unknown>): void {
        this.toolComponents.get(toolCallId)?.updateArgs(args);
    }

    addToolCall(toolName: string, toolCallId: string, args: Record<string, unknown>): void {
        if (this.liveMsg) {
            this.liveMsg.content.push({ type: "toolCall", id: toolCallId, name: toolName, arguments: args });
            this.liveComponent?.updateContent(this.liveMsg);
        }
        this.liveMsg = null;
        this.liveComponent = null;

        const comp = new ToolExecutionComponent(toolName, args, this.tui, this.cwd);
        if (this.expanded) comp.setExpanded(true);
        (this.assistantTurn ?? this).addChild(comp);
        this.toolComponents.set(toolCallId, comp);
        this.allToolComponents.push(comp);
    }

    /** Live status line in the tool title (subagent: current tool name). */
    setToolStatus(toolCallId: string, status: string): void {
        this.toolComponents.get(toolCallId)?.updateStatus(status);
    }

    /** Live partial output (subagent streaming) — keeps the component pending. */
    updateToolProgress(toolCallId: string, text: string): void {
        this.toolComponents.get(toolCallId)?.updateResult({ content: [{ type: "text", text }], isError: false }, true);
    }

    addToolResult(toolCallId: string, output: unknown, isError = false): void {
        const comp = this.toolComponents.get(toolCallId);
        if (!comp) return;
        const text = stringifyResult(output);
        comp.updateResult({ content: [{ type: "text", text }], isError }, false);
        this.toolComponents.delete(toolCallId);
    }

    addSystem(text: string): void {
        this.addChild(new Text(chalk.dim(text), 1, 0));
    }

    /** Hook-related lines get their own orange accent, like tools get grey/green. */
    addHook(text: string): void {
        this.addChild(new Text(HOOK_ORANGE(`⚙ ${text}`), 1, 0));
    }

    /** Echo an executed slash command: highlighted /name, dim args. */
    addCommand(text: string): void {
        this.addChild(new Spacer(1));
        const space = text.indexOf(" ");
        const cmd = space < 0 ? text : text.slice(0, space);
        const rest = space < 0 ? "" : text.slice(space);
        this.addChild(new Text(chalk.bold.cyan(cmd) + (rest ? chalk.dim(rest) : ""), 1, 0));
        this.assistantTurn = null;
    }

    /** Themed markdown block (changelog, release notes). */
    addMarkdown(md: string): void {
        this.addChild(new Spacer(1));
        this.addChild(new Markdown(md, 1, 0, getMarkdownTheme()));
    }

    addCompactionSummary(summary: string, tokensBefore: number, timestamp = Date.now()): void {
        const comp = new CompactionSummaryMessageComponent({ summary, tokensBefore, timestamp });
        comp.setExpanded(this.expanded);
        this.addChild(new Spacer(1));
        this.addChild(comp);
        this.compactionComponents.push(comp);
        this.assistantTurn = null;
    }

    addError(text: string): void {
        this.addChild(new Text(chalk.red(`error: ${text}`), 1, 0));
    }
}

function stringifyResult(output: unknown): string {
    if (output == null) return "";
    if (typeof output === "string") return output;
    const o = output as Record<string, unknown>;
    if (typeof o.stdout === "string" || typeof o.stderr === "string") {
        return `${o.stdout ?? ""}${o.stderr ? `\n[stderr]\n${o.stderr}` : ""}`.trim();
    }
    if (typeof o.content === "string") return o.content;
    if (typeof o.matches === "string") return o.matches;
    if (Array.isArray((o as { paths?: unknown }).paths)) return (o as { paths: string[] }).paths.join("\n");
    if (Array.isArray((o as { entries?: unknown }).entries)) {
        return (o as { entries: { name: string; type: string }[] }).entries
            .map((e) => (e.type === "dir" ? `${e.name}/` : e.name))
            .join("\n");
    }
    return JSON.stringify(output, null, 2);
}
