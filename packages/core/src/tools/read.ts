import { access as fsAccess, readFile as fsReadFile } from "node:fs/promises";
import { constants } from "node:fs";
import { tool } from "ai";
import { z } from "zod";
import { resolveReadPath } from "./utils/path-utils";
import { DEFAULT_MAX_BYTES, DEFAULT_MAX_LINES, formatSize, truncateHead } from "./utils/truncate";

const IMAGE_EXT_RE = /\.(jpe?g|png|gif|webp)$/i;

function detectImageMimeType(path: string): string | null {
    const m = path.match(IMAGE_EXT_RE);
    if (!m) return null;
    const ext = m[1].toLowerCase();
    if (ext === "jpg" || ext === "jpeg") return "image/jpeg";
    if (ext === "png") return "image/png";
    if (ext === "gif") return "image/gif";
    if (ext === "webp") return "image/webp";
    return null;
}

export interface ReadToolContext {
    cwd: string;
    abortSignal?: AbortSignal;
}

export function createReadTool(ctx: ReadToolContext) {
    return tool({
        description: `Read the contents of a file. Supports text files. For text files, output is truncated to ${DEFAULT_MAX_LINES} lines or ${DEFAULT_MAX_BYTES / 1024}KB (whichever is hit first). Use offset/limit for large files. When you need the full file, continue with offset until complete.`,
        inputSchema: z.object({
            path: z.string().describe("Path to the file to read (relative or absolute)"),
            offset: z.number().int().positive().optional().describe("Line number to start reading from (1-indexed)"),
            limit: z.number().int().positive().optional().describe("Maximum number of lines to read"),
        }),
        execute: async ({ path, offset, limit }, options) => {
            const signal = options?.abortSignal ?? ctx.abortSignal;
            if (signal?.aborted) throw new Error("Operation aborted");
            const absolutePath = resolveReadPath(path, ctx.cwd);
            await fsAccess(absolutePath, constants.R_OK);
            const mime = detectImageMimeType(absolutePath);
            if (mime) {
                return `[image file: ${mime} at ${absolutePath}. Image rendering not supported in this tool result; use bash 'file' or vision-capable read elsewhere.]`;
            }
            const buf = await fsReadFile(absolutePath);
            const textContent = buf.toString("utf-8");
            const allLines = textContent.split("\n");
            const totalFileLines = allLines.length;
            const startLine = offset ? Math.max(0, offset - 1) : 0;
            const startLineDisplay = startLine + 1;
            if (startLine >= allLines.length) {
                throw new Error(`Offset ${offset} is beyond end of file (${allLines.length} lines total)`);
            }
            let selectedContent: string;
            let userLimitedLines: number | undefined;
            if (limit !== undefined) {
                const endLine = Math.min(startLine + limit, allLines.length);
                selectedContent = allLines.slice(startLine, endLine).join("\n");
                userLimitedLines = endLine - startLine;
            } else {
                selectedContent = allLines.slice(startLine).join("\n");
            }
            const truncation = truncateHead(selectedContent);
            let outputText: string;
            if (truncation.firstLineExceedsLimit) {
                const firstLineSize = formatSize(Buffer.byteLength(allLines[startLine], "utf-8"));
                outputText = `[Line ${startLineDisplay} is ${firstLineSize}, exceeds ${formatSize(DEFAULT_MAX_BYTES)} limit. Use bash: sed -n '${startLineDisplay}p' ${path} | head -c ${DEFAULT_MAX_BYTES}]`;
            } else if (truncation.truncated) {
                const endLineDisplay = startLineDisplay + truncation.outputLines - 1;
                const nextOffset = endLineDisplay + 1;
                outputText = truncation.content;
                if (truncation.truncatedBy === "lines") {
                    outputText += `\n\n[Showing lines ${startLineDisplay}-${endLineDisplay} of ${totalFileLines}. Use offset=${nextOffset} to continue.]`;
                } else {
                    outputText += `\n\n[Showing lines ${startLineDisplay}-${endLineDisplay} of ${totalFileLines} (${formatSize(DEFAULT_MAX_BYTES)} limit). Use offset=${nextOffset} to continue.]`;
                }
            } else if (userLimitedLines !== undefined && startLine + userLimitedLines < allLines.length) {
                const remaining = allLines.length - (startLine + userLimitedLines);
                const nextOffset = startLine + userLimitedLines + 1;
                outputText = `${truncation.content}\n\n[${remaining} more lines in file. Use offset=${nextOffset} to continue.]`;
            } else {
                outputText = truncation.content;
            }
            return outputText;
        },
    });
}
