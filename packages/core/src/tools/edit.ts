import { constants } from "node:fs";
import { access as fsAccess, readFile as fsReadFile, writeFile as fsWriteFile } from "node:fs/promises";
import { tool } from "ai";
import { z } from "zod";
import {
    applyEditsToNormalizedContent,
    detectLineEnding,
    generateDiffString,
    normalizeToLF,
    restoreLineEndings,
    stripBom,
    type Edit,
} from "./utils/edit-diff";
import { withFileMutationQueue } from "./utils/file-mutation-queue";
import { resolveToCwd } from "./utils/path-utils";

export interface EditToolContext {
    cwd: string;
    abortSignal?: AbortSignal;
}

function prepareEditInput(input: unknown): { path: string; edits: Edit[] } {
    if (!input || typeof input !== "object") throw new Error("edit input must be an object");
    const args = input as Record<string, unknown>;
    // Some models (Opus 4.6, GLM-5.1) send edits as a JSON string instead of an array
    if (typeof args.edits === "string") {
        try {
            const parsed = JSON.parse(args.edits);
            if (Array.isArray(parsed)) args.edits = parsed;
        } catch {}
    }
    // Legacy flat oldText/newText → push into edits[]
    if (typeof args.oldText === "string" && typeof args.newText === "string") {
        const edits = Array.isArray(args.edits) ? [...(args.edits as Edit[])] : [];
        edits.push({ oldText: args.oldText, newText: args.newText });
        args.edits = edits;
    }
    if (!Array.isArray(args.edits) || (args.edits as Edit[]).length === 0) {
        throw new Error("Edit tool input is invalid. edits must contain at least one replacement.");
    }
    return { path: args.path as string, edits: args.edits as Edit[] };
}

export function createEditTool(ctx: EditToolContext) {
    return tool({
        description:
            "Edit a file by applying one or more targeted text replacements. Each replacement matches against the ORIGINAL file (not incrementally after prior edits). Use edit for precise changes; oldText must match exactly and uniquely. For multiple changes in one file, batch them into a single call's edits[] array. Do not emit overlapping or nested edits.",
        inputSchema: z.object({
            path: z.string().describe("Path to the file to edit (relative or absolute)"),
            edits: z
                .array(
                    z.object({
                        oldText: z
                            .string()
                            .describe(
                                "Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call.",
                            ),
                        newText: z.string().describe("Replacement text for this targeted edit."),
                    }),
                )
                .describe(
                    "One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead.",
                ),
        }),
        execute: async (input, options) => {
            const signal = options?.abortSignal ?? ctx.abortSignal;
            if (signal?.aborted) throw new Error("Operation aborted");

            const { path, edits } = prepareEditInput(input);
            const absolutePath = resolveToCwd(path, ctx.cwd);

            return withFileMutationQueue(absolutePath, async () => {
                if (signal?.aborted) throw new Error("Operation aborted");
                try {
                    await fsAccess(absolutePath, constants.R_OK | constants.W_OK);
                } catch (err) {
                    const code =
                        err instanceof Error && "code" in err
                            ? `Error code: ${(err as { code?: string }).code}`
                            : String(err);
                    throw new Error(`Could not edit file: ${path}. ${code}.`);
                }
                if (signal?.aborted) throw new Error("Operation aborted");
                const buffer = await fsReadFile(absolutePath);
                const rawContent = buffer.toString("utf-8");
                if (signal?.aborted) throw new Error("Operation aborted");

                const { bom, text: content } = stripBom(rawContent);
                const originalEnding = detectLineEnding(content);
                const normalizedContent = normalizeToLF(content);
                const { baseContent, newContent } = applyEditsToNormalizedContent(normalizedContent, edits, path);

                if (signal?.aborted) throw new Error("Operation aborted");
                const finalContent = bom + restoreLineEndings(newContent, originalEnding);
                await fsWriteFile(absolutePath, finalContent);

                const diffResult = generateDiffString(baseContent, newContent);
                return `Successfully replaced ${edits.length} block(s) in ${path}.\n\n${diffResult.diff}`;
            });
        },
    });
}
