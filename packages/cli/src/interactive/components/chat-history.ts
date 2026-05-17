import { Container, Spacer, Text, type TUI } from "@earendil-works/pi-tui";
import {
  AssistantMessageComponent,
  createAllToolDefinitions,
  parseSkillBlock,
  SkillInvocationMessageComponent,
  ToolExecutionComponent,
  UserMessageComponent,
} from "@earendil-works/pi-coding-agent";
import chalk from "chalk";

interface PiAssistantMessage {
  role: "assistant";
  content: Array<{ type: "text"; text: string } | { type: "thinking"; thinking: string } | { type: "toolCall"; id: string; name: string; arguments: Record<string, unknown> }>;
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
  private assistantTurn: Container | null = null;
  private expanded = false;

  setToolsExpanded(expanded: boolean): void {
    this.expanded = expanded;
    for (const c of this.allToolComponents) c.setExpanded(expanded);
    for (const c of this.skillComponents) c.setExpanded(expanded);
  }
  toggleToolsExpanded(): boolean {
    this.setToolsExpanded(!this.expanded);
    return this.expanded;
  }

  private toolDefs: ReturnType<typeof createAllToolDefinitions>;

  constructor(private tui: TUI, private cwd: string) {
    super();
    this.toolDefs = createAllToolDefinitions(cwd);
  }

  reset(): void {
    this.clear();
    this.liveMsg = null;
    this.liveComponent = null;
    this.toolComponents.clear();
    this.allToolComponents = [];
    this.skillComponents = [];
    this.assistantTurn = null;
  }

  addUser(text: string): void {
    this.addChild(new Spacer(1));
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
    this.assistantTurn = null; // next assistant gets its own turn container
  }

  ensureAssistant(provider: string, model: string): void {
    if (!this.assistantTurn) {
      this.assistantTurn = new Container();
      this.addChild(new Spacer(1));
      this.addChild(this.assistantTurn);
    }
    if (this.liveComponent) return;
    this.liveMsg = emptyAssistantMessage(provider, model);
    this.liveComponent = new AssistantMessageComponent(this.liveMsg as never);
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
    this.liveComponent!.updateContent(this.liveMsg as never);
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
    this.liveComponent!.updateContent(this.liveMsg as never);
  }

  finishAssistant(stopReason: PiAssistantMessage["stopReason"] = "stop"): void {
    if (this.liveMsg) {
      this.liveMsg.stopReason = stopReason;
      this.liveComponent?.updateContent(this.liveMsg as never);
    }
    this.liveMsg = null;
    this.liveComponent = null;
  }

  addToolCall(toolName: string, toolCallId: string, args: Record<string, unknown>): void {
    // Push toolCall into the live assistant content so streaming text resumes AFTER it
    if (this.liveMsg) {
      this.liveMsg.content.push({ type: "toolCall", id: toolCallId, name: toolName, arguments: args });
      this.liveComponent?.updateContent(this.liveMsg as never);
    }
    // Finalize current assistant component; next delta creates a fresh one below the tool
    this.liveMsg = null;
    this.liveComponent = null;

    const def = (this.toolDefs as Record<string, unknown>)[toolName] as never;
    const comp = new ToolExecutionComponent(
      toolName,
      toolCallId,
      args,
      { showImages: false },
      def,
      this.tui,
      this.cwd,
    );
    comp.markExecutionStarted();
    comp.setArgsComplete();
    if (this.expanded) comp.setExpanded(true);
    (this.assistantTurn ?? this).addChild(comp);
    this.toolComponents.set(toolCallId, comp);
    this.allToolComponents.push(comp);
  }

  addToolResult(toolCallId: string, output: unknown, isError = false): void {
    const comp = this.toolComponents.get(toolCallId);
    if (!comp) return;
    const text = stringifyOutput(output);
    comp.updateResult({ content: [{ type: "text", text }], isError, details: undefined }, false);
    this.toolComponents.delete(toolCallId);
  }

  addSystem(text: string): void {
    this.addChild(new Text(chalk.dim(text), 1, 0));
  }

  addError(text: string): void {
    this.addChild(new Text(chalk.red(`error: ${text}`), 1, 0));
  }
}

function stringifyOutput(output: unknown): string {
  if (output == null) return "";
  if (typeof output === "string") return output;
  const o = output as Record<string, unknown>;
  if (typeof o.stdout === "string" || typeof o.stderr === "string") {
    return `${o.stdout ?? ""}${o.stderr ? `\n[stderr]\n${o.stderr}` : ""}`.trim();
  }
  if (typeof o.content === "string") return o.content;
  if (typeof o.matches === "string") return o.matches;
  if (Array.isArray((o as { paths?: unknown }).paths)) return ((o as { paths: string[] }).paths).join("\n");
  if (Array.isArray((o as { entries?: unknown }).entries)) {
    return ((o as { entries: { name: string; type: string }[] }).entries)
      .map((e) => (e.type === "dir" ? `${e.name}/` : e.name))
      .join("\n");
  }
  return JSON.stringify(output, null, 2);
}
