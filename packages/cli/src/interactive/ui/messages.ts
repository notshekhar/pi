/**
 * Message components — replace @earendil-works/pi-coding-agent's
 * UserMessageComponent / AssistantMessageComponent /
 * SkillInvocationMessageComponent / CompactionSummaryMessageComponent /
 * parseSkillBlock / DynamicBorder.
 * Ported from pi-mono, trimmed to what our chat history actually uses.
 */
import { Box, type Component, Container, Markdown, type MarkdownTheme, Spacer, Text } from "@earendil-works/pi-tui";
import { getMarkdownTheme, theme } from "./theme";

// OSC 133 shell-integration zones — let terminals jump between messages
const ZONE_START = "\x1b]133;A\x07";
const ZONE_END = "\x1b]133;B\x07";
const ZONE_FINAL = "\x1b]133;C\x07";

const EXPAND_HINT = "ctrl+e";

export class DynamicBorder implements Component {
  private color: (str: string) => string;

  constructor(color: (str: string) => string = (str) => theme.fg("border", str)) {
    this.color = color;
  }

  invalidate(): void {}

  render(width: number): string[] {
    return [this.color("─".repeat(Math.max(1, width)))];
  }
}

export class UserMessageComponent extends Container {
  constructor(text: string, markdownTheme: MarkdownTheme = getMarkdownTheme()) {
    super();
    const box = new Box(1, 1, (content: string) => theme.bg("userMessageBg", content));
    box.addChild(
      new Markdown(text, 0, 0, markdownTheme, {
        color: (content: string) => theme.fg("userMessageText", content),
      }),
    );
    this.addChild(box);
  }

  override render(width: number): string[] {
    const lines = super.render(width);
    if (lines.length === 0) return lines;
    lines[0] = ZONE_START + lines[0];
    lines[lines.length - 1] = ZONE_END + ZONE_FINAL + lines[lines.length - 1];
    return lines;
  }
}

export interface AssistantMessageLike {
  content: Array<
    | { type: "text"; text: string }
    | { type: "thinking"; thinking: string }
    | { type: "toolCall"; id: string; name: string; arguments: Record<string, unknown> }
  >;
  stopReason: "stop" | "length" | "toolUse" | "error" | "aborted";
  errorMessage?: string;
}

export class AssistantMessageComponent extends Container {
  private contentContainer: Container;
  private markdownTheme: MarkdownTheme;
  private lastMessage?: AssistantMessageLike;
  private hasToolCalls = false;

  constructor(message?: AssistantMessageLike, markdownTheme: MarkdownTheme = getMarkdownTheme()) {
    super();
    this.markdownTheme = markdownTheme;
    this.contentContainer = new Container();
    this.addChild(this.contentContainer);
    if (message) this.updateContent(message);
  }

  override invalidate(): void {
    super.invalidate();
    if (this.lastMessage) this.updateContent(this.lastMessage);
  }

  override render(width: number): string[] {
    const lines = super.render(width);
    if (this.hasToolCalls || lines.length === 0) return lines;
    lines[0] = ZONE_START + lines[0];
    lines[lines.length - 1] = ZONE_END + ZONE_FINAL + lines[lines.length - 1];
    return lines;
  }

  updateContent(message: AssistantMessageLike): void {
    this.lastMessage = message;
    this.contentContainer.clear();

    const hasVisibleContent = message.content.some(
      (c) => (c.type === "text" && c.text.trim()) || (c.type === "thinking" && c.thinking.trim()),
    );
    if (hasVisibleContent) this.contentContainer.addChild(new Spacer(1));

    for (let i = 0; i < message.content.length; i++) {
      const content = message.content[i];
      if (content.type === "text" && content.text.trim()) {
        this.contentContainer.addChild(new Markdown(content.text.trim(), 1, 0, this.markdownTheme));
      } else if (content.type === "thinking" && content.thinking.trim()) {
        this.contentContainer.addChild(
          new Markdown(content.thinking.trim(), 1, 0, this.markdownTheme, {
            color: (text: string) => theme.fg("thinkingText", text),
            italic: true,
          }),
        );
        const hasVisibleContentAfter = message.content
          .slice(i + 1)
          .some((c) => (c.type === "text" && c.text.trim()) || (c.type === "thinking" && c.thinking.trim()));
        if (hasVisibleContentAfter) this.contentContainer.addChild(new Spacer(1));
      }
    }

    this.hasToolCalls = message.content.some((c) => c.type === "toolCall");
    if (!this.hasToolCalls) {
      if (message.stopReason === "aborted") {
        const abortMessage =
          message.errorMessage && message.errorMessage !== "Request was aborted"
            ? message.errorMessage
            : "Operation aborted";
        this.contentContainer.addChild(new Spacer(1));
        this.contentContainer.addChild(new Text(theme.fg("error", abortMessage), 1, 0));
      } else if (message.stopReason === "error") {
        this.contentContainer.addChild(new Spacer(1));
        this.contentContainer.addChild(new Text(theme.fg("error", `Error: ${message.errorMessage || "Unknown error"}`), 1, 0));
      }
    }
  }
}

export interface ParsedSkillBlock {
  name: string;
  location: string;
  content: string;
  userMessage: string | undefined;
}

export function parseSkillBlock(text: string): ParsedSkillBlock | null {
  const match = text.match(/^<skill name="([^"]+)" location="([^"]+)">\n([\s\S]*?)\n<\/skill>(?:\n\n([\s\S]+))?$/);
  if (!match) return null;
  return {
    name: match[1],
    location: match[2],
    content: match[3],
    userMessage: match[4]?.trim() || undefined,
  };
}

export class SkillInvocationMessageComponent extends Box {
  private expanded = false;

  constructor(
    private skillBlock: ParsedSkillBlock,
    private markdownTheme: MarkdownTheme = getMarkdownTheme(),
  ) {
    super(1, 1, (t) => theme.bg("customMessageBg", t));
    this.updateDisplay();
  }

  setExpanded(expanded: boolean): void {
    this.expanded = expanded;
    this.updateDisplay();
  }

  override invalidate(): void {
    super.invalidate();
    this.updateDisplay();
  }

  private updateDisplay(): void {
    this.clear();
    if (this.expanded) {
      this.addChild(new Text(theme.fg("customMessageLabel", "\x1b[1m[skill]\x1b[22m"), 0, 0));
      this.addChild(
        new Markdown(`**${this.skillBlock.name}**\n\n${this.skillBlock.content}`, 0, 0, this.markdownTheme, {
          color: (text: string) => theme.fg("customMessageText", text),
        }),
      );
    } else {
      this.addChild(
        new Text(
          theme.fg("customMessageLabel", "\x1b[1m[skill]\x1b[22m ") +
            theme.fg("customMessageText", this.skillBlock.name) +
            theme.fg("dim", ` (${EXPAND_HINT} to expand)`),
          0,
          0,
        ),
      );
    }
  }
}

export interface CompactionSummaryLike {
  summary: string;
  tokensBefore: number;
  timestamp?: number;
}

export class CompactionSummaryMessageComponent extends Box {
  private expanded = false;

  constructor(
    private message: CompactionSummaryLike,
    private markdownTheme: MarkdownTheme = getMarkdownTheme(),
  ) {
    super(1, 1, (t) => theme.bg("customMessageBg", t));
    this.updateDisplay();
  }

  setExpanded(expanded: boolean): void {
    this.expanded = expanded;
    this.updateDisplay();
  }

  override invalidate(): void {
    super.invalidate();
    this.updateDisplay();
  }

  private updateDisplay(): void {
    this.clear();
    const tokenStr = this.message.tokensBefore.toLocaleString();
    this.addChild(new Text(theme.fg("customMessageLabel", "\x1b[1m[compaction]\x1b[22m"), 0, 0));
    this.addChild(new Spacer(1));
    if (this.expanded) {
      this.addChild(
        new Markdown(`**Compacted from ${tokenStr} tokens**\n\n${this.message.summary}`, 0, 0, this.markdownTheme, {
          color: (text: string) => theme.fg("customMessageText", text),
        }),
      );
    } else {
      this.addChild(
        new Text(
          theme.fg("customMessageText", `Compacted from ${tokenStr} tokens (`) +
            theme.fg("dim", EXPAND_HINT) +
            theme.fg("customMessageText", " to expand)"),
          0,
          0,
        ),
      );
    }
  }
}
