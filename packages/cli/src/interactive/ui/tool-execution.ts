/**
 * Own tool execution renderer for our 7 tools:
 * status-colored box, bold title with a per-tool arg summary, output that
 * collapses to a preview and expands with ctrl+e, diff coloring for edit/write,
 * syntax highlighting for read.
 */
import { Box, Container, Spacer, Text, type TUI } from "@notshekhar/loop-tui";
import { getLanguageFromPath, highlightCode, theme } from "./theme";
import { formatToolArgs, readLineRangeText } from "./tool-summary";

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

    /** Fill in the args once the tool's input has finished streaming (the box was
     * created as a pending stub on `tool-input-start` with no args yet). */
    updateArgs(args: Record<string, unknown>): void {
        this.args = args;
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
        // Collapsed → short preview capped at COLLAPSED_LINES; expanded (ctrl+e)
        // → the full output, no cap.
        const truncated = !this.expanded && lines.length > COLLAPSED_LINES;
        const shown = truncated ? lines.slice(0, COLLAPSED_LINES) : lines;

        this.box.addChild(new Spacer(1));
        this.box.addChild(new Text(shown.join("\n"), 0, 0));
        if (truncated) {
            this.box.addChild(
                new Text(
                    theme.fg("dim", `… +${lines.length - COLLAPSED_LINES} lines (${EXPAND_HINT} to expand)`),
                    0,
                    0,
                ),
            );
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
        // suffix; the range sits outside the muted wrap so it
        // keeps its own color.
        const range = this.toolName === "read" ? this.readLineRange() : "";
        return `${title} ${theme.fg("muted", summary)}${range}`;
    }

    private argsSummary(): string {
        // Shared with the /tree row (tool-summary.ts) so both describe a call
        // the same way; the empty-args guard lives inside formatToolArgs.
        return formatToolArgs(this.toolName, this.args, this.cwd);
    }

    /**
     * `read` line range — `:start` or `:start-end` from offset/limit, empty
     * when neither is set. Themed wrap around the shared plain-text range.
     */
    private readLineRange(): string {
        const range = readLineRangeText(this.args);
        return range ? theme.fg("warning", range) : "";
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
