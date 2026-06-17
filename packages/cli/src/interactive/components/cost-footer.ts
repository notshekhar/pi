import { getModelSync } from "@notshekhar/pi-core";
import type { Component } from "@notshekhar/pi-tui";
import chalk from "chalk";
import { formatClock, formatCountdown } from "../time";

function fmtTokens(n: number): string {
    if (n < 1000) return String(n);
    if (n < 10_000) return `${(n / 1000).toFixed(1)}k`;
    if (n < 1_000_000) return `${Math.round(n / 1000)}k`;
    if (n < 10_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
    return `${Math.round(n / 1_000_000)}M`;
}

const ANSI_RE = /\x1b\[[0-9;]*m/g;
function ansiLen(s: string): number {
    return s.replace(ANSI_RE, "").length;
}

// Slice keeping ANSI escapes intact, counting only visible chars toward width.
function ansiSlice(s: string, width: number): string {
    let out = "";
    let visible = 0;
    let i = 0;
    while (i < s.length && visible < width) {
        if (s[i] === "\x1b" && s[i + 1] === "[") {
            const end = s.indexOf("m", i);
            if (end >= 0) {
                out += s.slice(i, end + 1);
                i = end + 1;
                continue;
            }
        }
        out += s[i];
        visible++;
        i++;
    }
    // append trailing reset/closing ANSI sequences so colors don't bleed
    while (i < s.length && s[i] === "\x1b" && s[i + 1] === "[") {
        const end = s.indexOf("m", i);
        if (end < 0) break;
        out += s.slice(i, end + 1);
        i = end + 1;
    }
    return out;
}

export class CostFooter implements Component {
    private modelId = "";
    private sessionId = "";
    private cost = "$0.0000";
    private ctxUsed = 0;
    private ctxMax = 0;
    private thinking = "off";
    // Whether the current model reasons. Mirrors pi-mono's footer, which only
    // shows the thinking level when state.model?.reasoning is truthy.
    private modelReasoning = true;
    private agent = "default";
    private timerEndsAt: number | null = null;
    private clockEnabled = false;

    setModel(id: string) {
        this.modelId = id;
        this.modelReasoning = Boolean(getModelSync(id)?.reasoning);
    }
    setAgent(name: string) {
        this.agent = name;
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
    setTimer(endsAt: number | null) {
        this.timerEndsAt = endsAt;
    }
    setClockEnabled(enabled: boolean) {
        this.clockEnabled = enabled;
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
        const showThinking = this.modelReasoning && this.thinking && this.thinking !== "off";
        const modelLabel = showThinking
            ? `${this.modelId || "no-model"} • ${this.thinking}`
            : this.modelId || "no-model";
        // Two rows: agent/model identity on top, usage below.
        const agentStr =
            (this.agent && this.agent !== "default"
                ? chalk.hex("#e09956")(`agent ${this.agent}`)
                : chalk.dim("agent default")) + chalk.dim(" (shift+tab)");
        const identity = [agentStr, chalk.cyan(modelLabel)];
        const usage = [chalk.dim(`session ${sid}`), chalk.green(this.cost), ctxStr];
        if (this.timerEndsAt !== null) {
            const remaining = this.timerEndsAt - Date.now();
            const body = `timer ${formatCountdown(remaining)}`;
            usage.push(remaining < 60_000 ? chalk.yellow(body) : chalk.dim(body));
        }
        if (this.clockEnabled) usage.push(chalk.dim(formatClock()));
        const lines = [...wrapParts(identity, width), ...wrapParts(usage, width)];
        // Hard-clip any single part that still exceeds width (rare).
        return lines.map((l) => (ansiLen(l) > width ? ansiSlice(l, width) : l));
    }
}

function wrapParts(parts: string[], width: number): string[] {
    const sep = chalk.dim(" · ");
    const sepLen = ansiLen(sep);
    const lines: string[] = [];
    let cur = "";
    let curLen = 0;
    for (const p of parts) {
        const pLen = ansiLen(p);
        if (cur === "") {
            cur = p;
            curLen = pLen;
            continue;
        }
        if (curLen + sepLen + pLen <= width) {
            cur += sep + p;
            curLen += sepLen + pLen;
        } else {
            lines.push(cur);
            cur = p;
            curLen = pLen;
        }
    }
    if (cur) lines.push(cur);
    return lines;
}
