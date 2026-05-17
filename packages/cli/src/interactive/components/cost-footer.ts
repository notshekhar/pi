import type { Component } from "@earendil-works/pi-tui";
import chalk from "chalk";

export class CostFooter implements Component {
  private modelId = "";
  private sessionId = "";
  private cost = "$0.0000";
  private contextInfo = "";

  setModel(id: string) {
    this.modelId = id;
  }
  setSession(id: string) {
    this.sessionId = id;
  }
  setCost(s: string) {
    this.cost = s;
  }
  setContextInfo(s: string) {
    this.contextInfo = s;
  }

  invalidate(): void {}

  render(width: number): string[] {
    const parts = [
      chalk.cyan(this.modelId || "no-model"),
      chalk.dim(`session ${this.sessionId.slice(0, 8)}`),
      chalk.green(this.cost),
    ];
    if (this.contextInfo) parts.push(chalk.dim(this.contextInfo));
    let line = parts.join(chalk.dim(" · "));
    const ansiLen = line.replace(/\x1b\[[0-9;]*m/g, "").length;
    if (ansiLen > width) line = line.slice(0, width);
    return [line];
  }
}
