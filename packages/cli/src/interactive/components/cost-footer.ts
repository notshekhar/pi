import type { Component } from "@earendil-works/pi-tui";
import chalk from "chalk";

function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, "")}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1).replace(/\.0$/, "")}K`;
  return String(n);
}

export class CostFooter implements Component {
  private modelId = "";
  private sessionId = "";
  private cost = "$0.0000";
  private ctxUsed = 0;
  private ctxMax = 0;

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

  invalidate(): void {}

  render(width: number): string[] {
    const ctxStr = this.ctxMax
      ? `ctx ${fmtTokens(this.ctxUsed)}/${fmtTokens(this.ctxMax)}`
      : `ctx ${fmtTokens(this.ctxUsed)}`;
    const parts = [
      chalk.cyan(this.modelId || "no-model"),
      chalk.dim(`session ${this.sessionId.slice(0, 8)}`),
      chalk.green(this.cost),
      chalk.dim(ctxStr),
    ];
    let line = parts.join(chalk.dim(" · "));
    const ansiLen = line.replace(/\x1b\[[0-9;]*m/g, "").length;
    if (ansiLen > width) line = line.slice(0, width);
    return [line];
  }
}
