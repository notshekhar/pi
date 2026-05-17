import { Container, Spacer, Text, type TUI } from "@earendil-works/pi-tui";
import {
  AssistantMessageComponent,
  parseSkillBlock,
  SkillInvocationMessageComponent,
  UserMessageComponent,
} from "@earendil-works/pi-coding-agent";
import { ToolBox } from "./tool-box";
import chalk from "chalk";

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
  private toolComponents = new Map<string, ToolBox>();
  private allToolComponents: ToolBox[] = [];
  private skillComponents: SkillInvocationMessageComponent[] = [];
  private assistantTurn: Container | null = null;
  private expanded = false;

  constructor(private tui: TUI, private cwd: string) {
    super();
    void this.tui;
    void this.cwd;
  }

  setToolsExpanded(expanded: boolean): void {
    this.expanded = expanded;
    for (const c of this.allToolComponents) c.setExpanded(expanded);
    for (const c of this.skillComponents) c.setExpanded(expanded);
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
    if (this.liveMsg) {
      this.liveMsg.content.push({ type: "toolCall", id: toolCallId, name: toolName, arguments: args });
      this.liveComponent?.updateContent(this.liveMsg as never);
    }
    this.liveMsg = null;
    this.liveComponent = null;

    const box = new ToolBox(toolName, args);
    if (this.expanded) box.setExpanded(true);
    (this.assistantTurn ?? this).addChild(box);
    this.toolComponents.set(toolCallId, box);
    this.allToolComponents.push(box);
  }

  addToolResult(toolCallId: string, output: unknown, isError = false): void {
    const box = this.toolComponents.get(toolCallId);
    if (!box) return;
    box.updateResult(output, isError);
    this.toolComponents.delete(toolCallId);
  }

  addSystem(text: string): void {
    this.addChild(new Text(chalk.dim(text), 1, 0));
  }

  addError(text: string): void {
    this.addChild(new Text(chalk.red(`error: ${text}`), 1, 0));
  }
}
