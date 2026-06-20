import { access as fsAccess, readFile as fsReadFile } from "node:fs/promises";
import { constants } from "node:fs";
import { tool } from "ai";
import { z } from "zod";
import { resolveReadPath } from "./utils/path-utils";
import { recordRead } from "./utils/read-registry";
import { fetchUrlAsText, isHttpUrl } from "./utils/fetch-url";
import { getDoc, renderDocsIndex } from "../docs";
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

/** Resolve a `loop://` URI: docs index, a specific doc, or a wrapped URL fetch. */
async function readLoopUri(uri: string, signal?: AbortSignal): Promise<string> {
    const rest = uri.slice("loop://".length);
    if (rest === "docs" || rest === "docs/") {
        return renderDocsIndex();
    }
    if (rest.startsWith("docs/")) {
        const name = rest.slice("docs/".length);
        const doc = getDoc(name);
        if (!doc) return `[no such doc: ${name}]\n\n${renderDocsIndex()}`;
        return doc.content;
    }
    if (rest.startsWith("fetch/")) {
        const url = rest.slice("fetch/".length).trim();
        if (!isHttpUrl(url)) return `[loop://fetch/ expects an http(s):// URL, got: ${url}]`;
        return fetchUrlAsText(url, signal);
    }
    return `[unknown loop:// path: ${uri}. Use loop://docs, loop://docs/<name>.md, or loop://fetch/<url>]`;
}

export function createReadTool(ctx: ReadToolContext) {
    return tool({
        description: `Read the contents of a file, fetch a URL, or read loop's internal docs.

- Local path → reads the file (text, truncated to ${DEFAULT_MAX_LINES} lines or ${DEFAULT_MAX_BYTES / 1024}KB; use offset/limit for large files; continue with offset until complete).
- \`loop://fetch/<url>\` (or a bare http(s):// URL) → fetches the page and returns it as readable text (HTML stripped).
- \`loop://docs\` → lists loop's internal docs. \`loop://docs/<name>.md\` reads one (e.g. \`loop://docs/config.md\`).

IMPORTANT: when the user asks to add or change a model, custom provider, hook, MCP server, or custom agent, FIRST read \`loop://docs/config.md\` for the exact file locations and JSON shapes, then make the change. After editing any loop config, tell the user to hard-reload with /reload or by restarting loop — config changes don't apply to the running session until then.`,
        inputSchema: z.object({
            path: z
                .string()
                .describe(
                    "Local file path (relative or absolute), an http(s):// URL, or a loop:// URI (loop://docs, loop://docs/<name>.md, loop://fetch/<url>)",
                ),
            offset: z.number().int().positive().optional().describe("Line number to start reading from (1-indexed)"),
            limit: z.number().int().positive().optional().describe("Maximum number of lines to read"),
        }),
        execute: async ({ path, offset, limit }, options) => {
            const signal = options?.abortSignal ?? ctx.abortSignal;
            if (signal?.aborted) throw new Error("Operation aborted");
            const trimmed = path.trim();
            // loop:// scheme → internal docs or wrapped URL fetch.
            if (trimmed.toLowerCase().startsWith("loop://")) {
                return readLoopUri(trimmed, signal);
            }
            // URL → fetch as text (offset/limit don't apply to web content).
            if (isHttpUrl(path)) {
                return fetchUrlAsText(trimmed, signal);
            }
            const absolutePath = resolveReadPath(path, ctx.cwd);
            await fsAccess(absolutePath, constants.R_OK);
            const mime = detectImageMimeType(absolutePath);
            if (mime) {
                return `[image file: ${mime} at ${absolutePath}. Image rendering not supported in this tool result; use bash 'file' or vision-capable read elsewhere.]`;
            }
            const buf = await fsReadFile(absolutePath);
            // Unlocks edit/write for this file (read-before-modify enforcement).
            recordRead(absolutePath);
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
