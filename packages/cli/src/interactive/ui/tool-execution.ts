/**
 * Own tool execution renderer — replaces pi-coding-agent's
 * ToolExecutionComponent. pi's version dispatches to per-tool render
 * definitions deep inside pi-mono's core; ours renders our 7 tools directly:
 * status-colored box, bold title with a per-tool arg summary, output that
 * collapses to a preview and expands with ctrl+e, diff coloring for edit/write,
 * syntax highlighting for read.
 */
import { Box, Container, Spacer, Text, type TUI } from "@notshekhar/pi-tui";
import { getLanguageFromPath, highlightCode, theme } from "./theme";

const COLLAPSED_LINES = 6;
// Hard ceiling on output height even when expanded — a single 2000-line bash
// dump or subagent report would otherwise push the whole conversation off
// screen. Expanding lifts the 6-line preview to this, not to "everything".
// Overridable via PI_TOOL_MAX_LINES.
const MAX_EXPANDED_LINES = Number(process.env.PI_TOOL_MAX_LINES) || 40;
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
    /** Live status shown in the title while partial (subagent: current tool). */
    private statusText = "";

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

    /** PreToolUse hooks may rewrite the input (e.g. rtk) — show what actually ran. */
    updateArgs(args: Record<string, unknown>): void {
        this.args = args;
        this.updateDisplay();
        this.tui.requestRender();
    }

    updateResult(result: ToolResultLike, isPartial = false): void {
        this.result = result;
        this.isPartial = isPartial;
        if (!isPartial) this.statusText = "";
        this.updateDisplay();
        this.tui.requestRender();
    }

    updateStatus(status: string): void {
        this.statusText = status;
        this.updateDisplay();
        this.tui.requestRender();
    }

    override invalidate(): void {
        super.invalidate();
        this.updateDisplay();
    }

    private updateDisplay(): void {
        // Subagents (task tool) keep the purple custom-message background as
        // their identity — pending and done alike; errors still go red.
        const isTask = this.toolName === "task";
        this.box.setBgFn(
            this.result?.isError && !this.isPartial
                ? (text: string) => theme.bg("toolErrorBg", text)
                : isTask
                  ? (text: string) => theme.bg("customMessageBg", text)
                  : this.isPartial
                    ? (text: string) => theme.bg("toolPendingBg", text)
                    : (text: string) => theme.bg("toolSuccessBg", text),
        );
        this.box.clear();
        this.box.addChild(new Text(this.titleLine(), 0, 0));

        // sql: render the query as a highlighted SQL block instead of leaving it
        // as a raw JSON arg blob.
        const inputLines = this.inputPreview();
        if (inputLines) {
            this.box.addChild(new Spacer(1));
            this.box.addChild(new Text(inputLines.join("\n"), 0, 0));
        }

        const output = this.outputText();
        if (!output) return;

        const lines = this.colorOutput(output.split("\n"));
        // Collapsed → short preview; expanded → a taller but still bounded view.
        // Either way the block can't exceed its cap, so tall output never eats
        // the conversation.
        const cap = this.expanded ? MAX_EXPANDED_LINES : COLLAPSED_LINES;
        const hidden = lines.length - cap;
        const shown = hidden > 0 ? lines.slice(0, cap) : lines;

        this.box.addChild(new Spacer(1));
        this.box.addChild(new Text(shown.join("\n"), 0, 0));
        if (hidden > 0) {
            // Only offer the expand hint while collapsed — once expanded, the
            // remainder stays capped (the full text is in the transcript).
            const hint = this.expanded ? `… +${hidden} more lines` : `… +${hidden} lines (${EXPAND_HINT} to expand)`;
            this.box.addChild(new Text(theme.fg("dim", hint), 0, 0));
        }
    }

    /** Title color by state: pending/stale grey, failed vivid red, done normal. */
    private titleColor(): "muted" | "toolError" | "toolTitle" {
        if (this.isPartial) return "muted";
        if (this.result?.isError) return "toolError";
        return "toolTitle";
    }

    /** `toolname summary` — bold name, muted single-line arg summary. */
    private titleLine(): string {
        // Subagent header: `task <agent> · <state> · <prompt snippet>` where
        // state is the live tool while running, then done/failed.
        if (this.toolName === "task") {
            const agent = typeof this.args.agent === "string" ? this.args.agent : "default";
            const state = this.isPartial ? this.statusText || "running" : this.result?.isError ? "failed" : "done";
            const snippet = typeof this.args.prompt === "string" ? this.args.prompt.split("\n")[0].slice(0, 50) : "";
            const title = theme.fg(this.titleColor(), theme.bold(`task ${agent}`));
            return `${title} ${theme.fg("muted", snippet ? `${state} · ${snippet}` : state)}`;
        }
        const title = theme.fg(this.titleColor(), theme.bold(this.toolName));
        const summary = this.argsSummary();
        if (!summary) return title;
        // `read` appends its offset/limit as a warning-colored `:start-end`
        // suffix (mirrors pi-mono); the range sits outside the muted wrap so it
        // keeps its own color.
        const range = this.toolName === "read" ? this.readLineRange() : "";
        return `${title} ${theme.fg("muted", summary)}${range}`;
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
            case "sql": {
                const conn = typeof a.connectionId === "string" ? a.connectionId : "";
                const q = typeof a.query === "string" ? a.query.replace(/\s+/g, " ").trim() : "";
                const qShort = q.length > 60 ? `${q.slice(0, 57)}…` : q;
                return [conn, qShort].filter(Boolean).join(" · ");
            }
            default: {
                const json = JSON.stringify(a);
                return json.length > 80 ? `${json.slice(0, 77)}…` : json;
            }
        }
    }

    /**
     * `read` line range — `:start` or `:start-end` from offset/limit, empty
     * when neither is set. Mirrors pi-mono's formatReadLineRange.
     */
    private readLineRange(): string {
        const offset = typeof this.args.offset === "number" ? this.args.offset : undefined;
        const limit = typeof this.args.limit === "number" ? this.args.limit : undefined;
        if (offset === undefined && limit === undefined) return "";
        const start = offset ?? 1;
        const end = limit !== undefined ? start + limit - 1 : "";
        return theme.fg("warning", `:${start}${end ? `-${end}` : ""}`);
    }

    /** sql: the query, highlighted as a SQL block under the title. */
    private inputPreview(): string[] | null {
        if (this.toolName !== "sql") return null;
        const query = typeof this.args.query === "string" ? this.args.query.trim() : "";
        if (!query) return null;
        return highlightCode(query, "sql");
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
            return lines.map((l) => theme.fg("toolError", l));
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
