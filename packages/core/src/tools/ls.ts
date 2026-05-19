import { existsSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { tool } from "ai";
import { z } from "zod";
import { resolveToCwd } from "./utils/path-utils";
import { DEFAULT_MAX_BYTES, formatSize, truncateHead } from "./utils/truncate";

const DEFAULT_LIMIT = 500;

export interface LsToolContext {
  cwd: string;
  abortSignal?: AbortSignal;
}

export function createLsTool(ctx: LsToolContext) {
  return tool({
    description: `List directory contents. Returns entries sorted alphabetically, with '/' suffix for directories. Includes dotfiles. Output is truncated to ${DEFAULT_LIMIT} entries or ${DEFAULT_MAX_BYTES / 1024}KB (whichever is hit first).`,
    inputSchema: z.object({
      path: z.string().optional().describe("Directory to list (default: current directory)"),
      limit: z.number().int().positive().optional().describe("Maximum number of entries to return (default: 500)"),
    }),
    execute: async ({ path, limit }, options) => {
      const signal = options?.abortSignal ?? ctx.abortSignal;
      if (signal?.aborted) throw new Error("Operation aborted");

      const dirPath = resolveToCwd(path || ".", ctx.cwd);
      const effectiveLimit = limit ?? DEFAULT_LIMIT;

      if (!existsSync(dirPath)) throw new Error(`Path not found: ${dirPath}`);
      const st = statSync(dirPath);
      if (!st.isDirectory()) throw new Error(`Not a directory: ${dirPath}`);

      let entries: string[];
      try {
        entries = readdirSync(dirPath);
      } catch (e) {
        throw new Error(`Cannot read directory: ${e instanceof Error ? e.message : e}`);
      }
      entries.sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase()));

      const results: string[] = [];
      let entryLimitReached = false;
      for (const entry of entries) {
        if (signal?.aborted) throw new Error("Operation aborted");
        if (results.length >= effectiveLimit) {
          entryLimitReached = true;
          break;
        }
        const fullPath = join(dirPath, entry);
        let suffix = "";
        try {
          const entryStat = statSync(fullPath);
          if (entryStat.isDirectory()) suffix = "/";
        } catch {
          continue;
        }
        results.push(entry + suffix);
      }

      if (results.length === 0) return "(empty directory)";

      const rawOutput = results.join("\n");
      const truncation = truncateHead(rawOutput, { maxLines: Number.MAX_SAFE_INTEGER });
      let output = truncation.content;
      const notices: string[] = [];
      if (entryLimitReached) notices.push(`${effectiveLimit} entries limit reached. Use limit=${effectiveLimit * 2} for more`);
      if (truncation.truncated) notices.push(`${formatSize(DEFAULT_MAX_BYTES)} limit reached`);
      if (notices.length > 0) output += `\n\n[${notices.join(". ")}]`;
      return output;
    },
  });
}
