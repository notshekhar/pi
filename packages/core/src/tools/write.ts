import { mkdir, writeFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { dirname } from "node:path";
import { tool } from "ai";
import { z } from "zod";
import { resolveToCwd } from "./utils/path-utils";
import { withFileMutationQueue } from "./utils/file-mutation-queue";
import { checkReadBeforeModify, recordModified } from "./utils/read-registry";

export interface WriteToolContext {
    cwd: string;
    abortSignal?: AbortSignal;
}

export function createWriteTool(ctx: WriteToolContext) {
    return tool({
        description:
            "Write content to a file, overwriting if it exists. Creates parent directories as needed. Use edit for targeted modifications of existing files.",
        inputSchema: z.object({
            path: z.string().describe("Path to write (relative or absolute)"),
            content: z.string().describe("Full file contents to write"),
        }),
        execute: async ({ path, content }, options) => {
            const signal = options?.abortSignal ?? ctx.abortSignal;
            if (signal?.aborted) throw new Error("Operation aborted");
            const absolutePath = resolveToCwd(path, ctx.cwd);
            const dir = dirname(absolutePath);
            return withFileMutationQueue(absolutePath, async () => {
                if (signal?.aborted) throw new Error("Operation aborted");
                // Overwriting an existing file requires having read it this
                // session; new files/paths pass freely.
                if (existsSync(absolutePath)) {
                    const readError = checkReadBeforeModify(absolutePath, path);
                    if (readError) throw new Error(`${readError} (write would overwrite the existing file)`);
                }
                await mkdir(dir, { recursive: true });
                if (signal?.aborted) throw new Error("Operation aborted");
                await writeFile(absolutePath, content);
                recordModified(absolutePath);
                return `Successfully wrote ${content.length} bytes to ${path}`;
            });
        },
    });
}
