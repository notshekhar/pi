import { createInterface } from "node:readline";
import { spawn } from "node:child_process";
import { readFileSync, statSync } from "node:fs";
import { basename, relative } from "node:path";
import { tool } from "ai";
import { z } from "zod";
import { resolveToCwd } from "./utils/path-utils";
import { ensureTool } from "./utils/tools-manager";
import { DEFAULT_MAX_BYTES, formatSize, GREP_MAX_LINE_LENGTH, truncateHead, truncateLine } from "./utils/truncate";

const DEFAULT_LIMIT = 100;

export interface GrepToolContext {
    cwd: string;
    abortSignal?: AbortSignal;
}

export function createGrepTool(ctx: GrepToolContext) {
    return tool({
        description: `Search file contents for a pattern. Returns matching lines with file paths and line numbers. Respects .gitignore. Output is truncated to ${DEFAULT_LIMIT} matches or ${DEFAULT_MAX_BYTES / 1024}KB (whichever is hit first). Long lines are truncated to ${GREP_MAX_LINE_LENGTH} chars.`,
        inputSchema: z.object({
            pattern: z.string().describe("Search pattern (regex or literal string)"),
            path: z.string().optional().describe("Directory or file to search (default: current directory)"),
            glob: z.string().optional().describe("Filter files by glob pattern, e.g. '*.ts' or '**/*.spec.ts'"),
            ignoreCase: z.boolean().optional().describe("Case-insensitive search (default: false)"),
            literal: z
                .boolean()
                .optional()
                .describe("Treat pattern as literal string instead of regex (default: false)"),
            context: z
                .number()
                .int()
                .min(0)
                .max(20)
                .optional()
                .describe("Number of lines to show before and after each match (default: 0)"),
            limit: z
                .number()
                .int()
                .positive()
                .optional()
                .describe("Maximum number of matches to return (default: 100)"),
        }),
        execute: async ({ pattern, path: searchDir, glob, ignoreCase, literal, context, limit }, options) => {
            const signal = options?.abortSignal ?? ctx.abortSignal;
            if (signal?.aborted) throw new Error("Operation aborted");

            const rgPath = await ensureTool("rg", true);
            if (!rgPath) throw new Error("ripgrep (rg) is not available and could not be downloaded");

            const searchPath = resolveToCwd(searchDir || ".", ctx.cwd);
            let isDirectory: boolean;
            try {
                isDirectory = statSync(searchPath).isDirectory();
            } catch {
                throw new Error(`Path not found: ${searchPath}`);
            }

            const contextValue = context && context > 0 ? context : 0;
            const effectiveLimit = Math.max(1, limit ?? DEFAULT_LIMIT);
            const formatPath = (filePath: string): string => {
                if (isDirectory) {
                    const rel = relative(searchPath, filePath);
                    if (rel && !rel.startsWith("..")) return rel.replace(/\\/g, "/");
                }
                return basename(filePath);
            };

            const fileCache = new Map<string, string[]>();
            const getFileLines = (filePath: string): string[] => {
                let lines = fileCache.get(filePath);
                if (!lines) {
                    try {
                        const c = readFileSync(filePath, "utf8");
                        lines = c.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n");
                    } catch {
                        lines = [];
                    }
                    fileCache.set(filePath, lines);
                }
                return lines;
            };

            const args: string[] = ["--json", "--line-number", "--color=never", "--hidden"];
            if (ignoreCase) args.push("--ignore-case");
            if (literal) args.push("--fixed-strings");
            if (glob) args.push("--glob", glob);
            args.push("--", pattern, searchPath);

            return new Promise<string>((resolve, reject) => {
                const child = spawn(rgPath, args, { stdio: ["ignore", "pipe", "pipe"] });
                const rl = createInterface({ input: child.stdout });
                let stderr = "";
                let matchCount = 0;
                let matchLimitReached = false;
                let linesTruncated = false;
                let aborted = false;
                let killedDueToLimit = false;
                const matches: Array<{ filePath: string; lineNumber: number; lineText?: string }> = [];

                const cleanup = () => {
                    rl.close();
                    signal?.removeEventListener("abort", onAbort);
                };
                const stopChild = (dueToLimit = false) => {
                    if (!child.killed) {
                        killedDueToLimit = dueToLimit;
                        child.kill();
                    }
                };
                const onAbort = () => {
                    aborted = true;
                    stopChild();
                };
                signal?.addEventListener("abort", onAbort, { once: true });
                child.stderr?.on("data", (chunk) => {
                    stderr += chunk.toString();
                });

                const formatBlock = (filePath: string, lineNumber: number): string[] => {
                    const rel = formatPath(filePath);
                    const lines = getFileLines(filePath);
                    if (!lines.length) return [`${rel}:${lineNumber}: (unable to read file)`];
                    const block: string[] = [];
                    const start = contextValue > 0 ? Math.max(1, lineNumber - contextValue) : lineNumber;
                    const end = contextValue > 0 ? Math.min(lines.length, lineNumber + contextValue) : lineNumber;
                    for (let current = start; current <= end; current++) {
                        const raw = lines[current - 1] ?? "";
                        const sanitized = raw.replace(/\r/g, "");
                        const { text: tt, wasTruncated } = truncateLine(sanitized);
                        if (wasTruncated) linesTruncated = true;
                        if (current === lineNumber) block.push(`${rel}:${current}: ${tt}`);
                        else block.push(`${rel}-${current}- ${tt}`);
                    }
                    return block;
                };

                rl.on("line", (line) => {
                    if (!line.trim() || matchCount >= effectiveLimit) return;
                    let event: {
                        type?: string;
                        data?: { path?: { text?: string }; line_number?: number; lines?: { text?: string } };
                    };
                    try {
                        event = JSON.parse(line);
                    } catch {
                        return;
                    }
                    if (event.type === "match") {
                        matchCount++;
                        const filePath = event.data?.path?.text;
                        const lineNumber = event.data?.line_number;
                        const lineText = event.data?.lines?.text;
                        if (filePath && typeof lineNumber === "number")
                            matches.push({ filePath, lineNumber, lineText });
                        if (matchCount >= effectiveLimit) {
                            matchLimitReached = true;
                            stopChild(true);
                        }
                    }
                });

                child.on("error", (error) => {
                    cleanup();
                    reject(new Error(`Failed to run ripgrep: ${error.message}`));
                });
                child.on("close", (code) => {
                    cleanup();
                    if (aborted) {
                        reject(new Error("Operation aborted"));
                        return;
                    }
                    if (!killedDueToLimit && code !== 0 && code !== 1) {
                        reject(new Error(stderr.trim() || `ripgrep exited with code ${code}`));
                        return;
                    }
                    if (matchCount === 0) {
                        resolve("No matches found");
                        return;
                    }
                    const outputLines: string[] = [];
                    for (const m of matches) {
                        if (contextValue === 0 && m.lineText !== undefined) {
                            const rel = formatPath(m.filePath);
                            const sanitized = m.lineText.replace(/\r\n/g, "\n").replace(/\r/g, "").replace(/\n$/, "");
                            const { text: tt, wasTruncated } = truncateLine(sanitized);
                            if (wasTruncated) linesTruncated = true;
                            outputLines.push(`${rel}:${m.lineNumber}: ${tt}`);
                        } else {
                            outputLines.push(...formatBlock(m.filePath, m.lineNumber));
                        }
                    }
                    const raw = outputLines.join("\n");
                    const truncation = truncateHead(raw, { maxLines: Number.MAX_SAFE_INTEGER });
                    let out = truncation.content;
                    const notices: string[] = [];
                    if (matchLimitReached)
                        notices.push(
                            `${effectiveLimit} matches limit reached. Use limit=${effectiveLimit * 2} for more, or refine pattern`,
                        );
                    if (truncation.truncated) notices.push(`${formatSize(DEFAULT_MAX_BYTES)} limit reached`);
                    if (linesTruncated)
                        notices.push(
                            `Some lines truncated to ${GREP_MAX_LINE_LENGTH} chars. Use read tool to see full lines`,
                        );
                    if (notices.length > 0) out += `\n\n[${notices.join(". ")}]`;
                    resolve(out);
                });
            });
        },
    });
}
