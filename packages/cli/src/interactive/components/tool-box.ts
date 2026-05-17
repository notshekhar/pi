/**
 * Pi-style tool execution box with collapse/expand support.
 *
 * Collapsed:  ● tool args-summary           (Ctrl+O to expand)
 * Expanded:   ● tool args-summary           (Ctrl+O to collapse)
 *               <full JSON args>
 *               <full result output>
 *
 * Mirrors pi-mono's ToolExecutionComponent visual but uses our own data shape
 * (no toolDefinition required) so it works with arbitrary ai-sdk tool calls.
 */
import { Container, Box, Text } from "@earendil-works/pi-tui";
import chalk from "chalk";

interface ToolResult {
  output: unknown;
  isError?: boolean;
}

export class ToolBox extends Container {
  private toolName: string;
  private args: Record<string, unknown>;
  private result?: ToolResult;
  private running = true;
  private expanded = false;
  private container = new Box(1, 0);

  constructor(toolName: string, args: Record<string, unknown>) {
    super();
    this.toolName = toolName;
    this.args = args;
    this.addChild(this.container);
    this.render_();
  }

  setExpanded(expanded: boolean): void {
    this.expanded = expanded;
    this.render_();
  }

  isExpanded(): boolean {
    return this.expanded;
  }

  updateResult(output: unknown, isError = false): void {
    this.running = false;
    this.result = { output, isError };
    this.render_();
  }

  override invalidate(): void {
    super.invalidate();
    this.render_();
  }

  private render_(): void {
    this.container.clear();

    const glyph = this.running ? chalk.cyan("●") : this.result?.isError ? chalk.red("✗") : chalk.green("✓");
    const hint = chalk.dim(this.expanded ? "(Ctrl+O to collapse)" : "(Ctrl+O to expand)");
    const summary = chalk.dim(formatArgsSummary(this.toolName, this.args));
    const header = `${glyph} ${chalk.bold(this.toolName)}${summary ? ` ${summary}` : ""}  ${hint}`;
    this.container.addChild(new Text(header, 0, 0));

    if (!this.expanded) return;

    // Full args
    const argsStr = JSON.stringify(this.args, null, 2);
    if (argsStr && argsStr !== "{}") {
      this.container.addChild(new Text(chalk.dim(indent(argsStr, "  ")), 0, 0));
    }

    if (this.running) {
      this.container.addChild(new Text(chalk.dim("  running…"), 0, 0));
      return;
    }

    if (this.result) {
      const text = stringifyOutput(this.result.output);
      const color = this.result.isError ? chalk.red : chalk.dim;
      this.container.addChild(new Text(color(indent(text, "  ")), 0, 0));
    }
  }
}

function formatArgsSummary(name: string, input: Record<string, unknown>): string {
  if (!input) return "";
  switch (name) {
    case "read":
    case "write":
    case "edit":
      return String(input.path ?? "");
    case "bash":
      return truncate(String(input.command ?? ""), 80);
    case "grep":
      return `${input.pattern ?? ""} ${input.path ?? ""}`.trim();
    case "find":
      return `${input.pattern ?? ""} ${input.path ?? ""}`.trim();
    case "ls":
      return String(input.path ?? ".");
    default: {
      const json = JSON.stringify(input);
      return truncate(json === "{}" ? "" : json, 80);
    }
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

function indent(text: string, prefix: string): string {
  return text.split("\n").map((l) => prefix + l).join("\n");
}

function truncate(s: string, n: number): string {
  s = s.replace(/\n/g, " ⏎ ");
  return s.length > n ? s.slice(0, n) + "…" : s;
}
