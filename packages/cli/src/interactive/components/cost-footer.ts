import type { Component } from "@earendil-works/pi-tui";
import chalk from "chalk";

function fmtTokens(n: number): string {
  if (n < 1000) return String(n);
  if (n < 10_000) return `${(n / 1000).toFixed(1)}k`;
  if (n < 1_000_000) return `${Math.round(n / 1000)}k`;
  if (n < 10_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  return `${Math.round(n / 1_000_000)}M`;
}

function ansiLen(s: string): number {
  return s.replace(/\x1b\[[0-9;]*m/g, "").length;
}

export class CostFooter implements Component {
  private modelId = "";
  private sessionId = "";
  private cost = "$0.0000";
  private ctxUsed = 0;
  private ctxMax = 0;
  private thinking = "off";

  setModel(id: string) {
    this.modelId = id;
  }
  setSession(id: string) {
    this.sessionId = id;
  }
  setCost(s: string) {
    this.cost = s;
  }
  setContext(used: number, max: number) {
    this.ctxUsed = used;
    this.ctxMax = max;
  }
  setThinking(level: string) {
    this.thinking = level;
  }

  invalidate(): void {}

  render(width: number): string[] {
    let ctxStr: string;
    if (this.ctxMax > 0) {
      const pct = (this.ctxUsed / this.ctxMax) * 100;
      const body = `ctx ${fmtTokens(this.ctxUsed)}/${fmtTokens(this.ctxMax)} (${pct.toFixed(1)}%)`;
      ctxStr = pct > 90 ? chalk.red(body) : pct > 70 ? chalk.yellow(body) : chalk.dim(body);
    } else {
      ctxStr = chalk.dim(`ctx ${fmtTokens(this.ctxUsed)}`);
    }
    const sid = this.sessionId ? this.sessionId.slice(0, 8) : "unsaved";
    const modelLabel = this.thinking && this.thinking !== "off"
      ? `${this.modelId || "no-model"} • ${this.thinking}`
      : (this.modelId || "no-model");
    const parts = [
      chalk.cyan(modelLabel),
      chalk.dim(`session ${sid}`),
      chalk.green(this.cost),
      ctxStr,
    ];
    const sep = chalk.dim(" · ");
    let line = parts.join(sep);
    if (ansiLen(line) > width) line = line.slice(0, width);
    return [line];
  }
}
