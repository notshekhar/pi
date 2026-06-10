/**
 * Own tool execution renderer — replaces pi-coding-agent's
 * ToolExecutionComponent. pi's version dispatches to per-tool render
 * definitions deep inside pi-mono's core; ours renders our 7 tools directly:
 * status-colored box, bold title with a per-tool arg summary, output that
 * collapses to a preview and expands with ctrl+e, diff coloring for edit/write,
 * syntax highlighting for read.
 */
import { Box, Container, Spacer, Text, type TUI } from "@earendil-works/pi-tui";
import { getLanguageFromPath, highlightCode, theme } from "./theme";

const COLLAPSED_LINES = 6;
const EXPAND_HINT = "ctrl+e";

export interface ToolResultLike {
  content: Array<{ type: string; text?: string }>;
  isError: boolean;
}

export class ToolExecutionComponent extends Container {
  private box: Box;
  private expanded = false;
  private isPartial = true;
  private result?: ToolResultLike;

  constructor(
    private toolName: string,
    private args: Record<string, unknown>,
    private tui: TUI,
    private cwd: string,
  ) {
    super();
    this.addChild(new Spacer(1));
    this.box = new Box(1, 1, (text: string) => theme.bg("toolPendingBg", text));
    this.addChild(this.box);
    this.updateDisplay();
  }

  setExpanded(expanded: boolean): void {
    this.expanded = expanded;
    this.updateDisplay();
  }

  updateResult(result: ToolResultLike, isPartial = false): void {
    this.result = result;
    this.isPartial = isPartial;
    this.updateDisplay();
    this.tui.requestRender();
  }

  override invalidate(): void {
    super.invalidate();
    this.updateDisplay();
  }

  private updateDisplay(): void {
    this.box.setBgFn(
      this.isPartial
        ? (text: string) => theme.bg("toolPendingBg", text)
        : this.result?.isError
          ? (text: string) => theme.bg("toolErrorBg", text)
          : (text: string) => theme.bg("toolSuccessBg", text),
    );
    this.box.clear();
    this.box.addChild(new Text(this.titleLine(), 0, 0));

    const output = this.outputText();
    if (!output) return;

    const lines = this.colorOutput(output.split("\n"));
    const truncated = !this.expanded && lines.length > COLLAPSED_LINES;
    const shown = truncated ? lines.slice(0, COLLAPSED_LINES) : lines;

    this.box.addChild(new Spacer(1));
    this.box.addChild(new Text(shown.join("\n"), 0, 0));
    if (truncated) {
      this.box.addChild(
        new Text(theme.fg("dim", `… +${lines.length - COLLAPSED_LINES} lines (${EXPAND_HINT} to expand)`), 0, 0),
      );
    }
  }

  /** `toolname summary` — bold name, muted single-line arg summary. */
  private titleLine(): string {
    const title = theme.fg("toolTitle", theme.bold(this.toolName));
    const summary = this.argsSummary();
    return summary ? `${title} ${theme.fg("muted", summary)}` : title;
  }

  private argsSummary(): string {
    const a = this.args;
    const rel = (p: unknown): string => {
      if (typeof p !== "string") return "";
      return p.startsWith(this.cwd) ? p.slice(this.cwd.length).replace(/^\//, "") || "." : p;
    };
    switch (this.toolName) {
      case "read":
      case "write":
      case "edit":
      case "ls":
        return rel(a.path ?? a.file_path ?? a.filePath);
      case "bash": {
        const cmd = typeof a.command === "string" ? a.command : "";
        const firstLine = cmd.split("\n")[0];
        return firstLine.length > 80 ? `${firstLine.slice(0, 77)}…` : firstLine;
      }
      case "grep":
        return [a.pattern, rel(a.path)].filter(Boolean).join(" in ");
      case "find":
        return typeof a.pattern === "string" ? a.pattern : "";
      default: {
        const json = JSON.stringify(a);
        return json.length > 80 ? `${json.slice(0, 77)}…` : json;
      }
    }
  }

  private outputText(): string {
    if (!this.result) return "";
    return this.result.content
      .filter((c) => c.type === "text" && c.text)
      .map((c) => c.text)
      .join("\n")
      .trimEnd();
  }

  private colorOutput(lines: string[]): string[] {
    if (this.result?.isError) {
      return lines.map((l) => theme.fg("error", l));
    }
    // edit/write results contain unified diffs — color +/- lines
    if (this.toolName === "edit" || this.toolName === "write") {
      return lines.map((l) => {
        if (l.startsWith("+")) return theme.fg("toolDiffAdded", l);
        if (l.startsWith("-")) return theme.fg("toolDiffRemoved", l);
        return theme.fg("toolDiffContext", l);
      });
    }
    if (this.toolName === "read") {
      const lang = getLanguageFromPath(String(this.args.path ?? this.args.file_path ?? ""));
      if (lang) return highlightCode(lines.join("\n"), lang);
    }
    return lines.map((l) => theme.fg("toolOutput", l));
  }
}
