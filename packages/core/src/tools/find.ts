import { createInterface } from "node:readline";
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import path from "node:path";
import { tool } from "ai";
import { z } from "zod";
import { resolveToCwd } from "./utils/path-utils";
import { ensureTool } from "./utils/tools-manager";
import { DEFAULT_MAX_BYTES, formatSize, truncateHead } from "./utils/truncate";

const DEFAULT_LIMIT = 1000;

function toPosixPath(value: string): string {
    return value.split(path.sep).join("/");
}

export interface FindToolContext {
    cwd: string;
    abortSignal?: AbortSignal;
}

export function createFindTool(ctx: FindToolContext) {
    return tool({
        description: `Search for files by glob pattern. Returns matching file paths relative to the search directory. Respects .gitignore. Output is truncated to ${DEFAULT_LIMIT} results or ${DEFAULT_MAX_BYTES / 1024}KB (whichever is hit first).`,
        inputSchema: z.object({
            pattern: z
                .string()
                .describe("Glob pattern to match files, e.g. '*.ts', '**/*.json', or 'src/**/*.spec.ts'"),
            path: z.string().optional().describe("Directory to search in (default: current directory)"),
            limit: z.number().int().positive().optional().describe("Maximum number of results (default: 1000)"),
        }),
        execute: async ({ pattern, path: searchDir, limit }, options) => {
            const signal = options?.abortSignal ?? ctx.abortSignal;
            if (signal?.aborted) throw new Error("Operation aborted");

            const searchPath = resolveToCwd(searchDir || ".", ctx.cwd);
            if (!existsSync(searchPath)) throw new Error(`Path not found: ${searchPath}`);
            const effectiveLimit = limit ?? DEFAULT_LIMIT;

            const fdPath = await ensureTool("fd", true);
            if (signal?.aborted) throw new Error("Operation aborted");
            if (!fdPath) throw new Error("fd is not available and could not be downloaded");

            const args: string[] = [
                "--glob",
                "--color=never",
                "--hidden",
                "--no-require-git",
                "--max-results",
                String(effectiveLimit),
            ];

            let effectivePattern = pattern;
            if (pattern.includes("/")) {
                args.push("--full-path");
                if (!pattern.startsWith("/") && !pattern.startsWith("**/") && pattern !== "**") {
                    effectivePattern = `**/${pattern}`;
                }
            }
            args.push("--", effectivePattern, searchPath);

            return new Promise<string>((resolve, reject) => {
                const child = spawn(fdPath, args, { stdio: ["ignore", "pipe", "pipe"] });
                const rl = createInterface({ input: child.stdout });
                let stderr = "";
                const lines: string[] = [];

                const cleanup = () => {
                    rl.close();
                    signal?.removeEventListener("abort", onAbort);
                };
                const onAbort = () => {
                    if (!child.killed) child.kill();
                };
                signal?.addEventListener("abort", onAbort, { once: true });

                child.stderr?.on("data", (chunk) => {
                    stderr += chunk.toString();
                });
                rl.on("line", (line) => lines.push(line));

                child.on("error", (err) => {
                    cleanup();
                    reject(new Error(`Failed to run fd: ${err.message}`));
                });
                child.on("close", (code) => {
                    cleanup();
                    if (signal?.aborted) {
                        reject(new Error("Operation aborted"));
                        return;
                    }
                    const output = lines.join("\n");
                    if (code !== 0 && !output) {
                        reject(new Error(stderr.trim() || `fd exited with code ${code}`));
                        return;
                    }
                    if (!output) {
                        resolve("No files found matching pattern");
                        return;
                    }
                    const relativized: string[] = [];
                    for (const raw of lines) {
                        const line = raw.replace(/\r$/, "").trim();
                        if (!line) continue;
                        const hadTrailingSlash = line.endsWith("/") || line.endsWith("\\");
                        let rel = line;
                        if (line.startsWith(searchPath)) rel = line.slice(searchPath.length + 1);
                        else rel = path.relative(searchPath, line);
                        if (hadTrailingSlash && !rel.endsWith("/")) rel += "/";
                        relativized.push(toPosixPath(rel));
                    }
                    const limitReached = relativized.length >= effectiveLimit;
                    const rawOut = relativized.join("\n");
                    const truncation = truncateHead(rawOut, { maxLines: Number.MAX_SAFE_INTEGER });
                    let result = truncation.content;
                    const notices: string[] = [];
                    if (limitReached)
                        notices.push(
                            `${effectiveLimit} results limit reached. Use limit=${effectiveLimit * 2} for more, or refine pattern`,
                        );
                    if (truncation.truncated) notices.push(`${formatSize(DEFAULT_MAX_BYTES)} limit reached`);
                    if (notices.length > 0) result += `\n\n[${notices.join(". ")}]`;
                    resolve(result);
                });
            });
        },
    });
}
