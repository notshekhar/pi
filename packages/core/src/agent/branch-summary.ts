/**
 * Branch summaries for /tree navigation — ported from pi-mono
 * core/compaction/branch-summarization. When the user navigates away from a
 * branch, its entries can be summarized into a branch-summary entry that
 * joins the model context at the navigation target.
 */
import { generateText } from "ai";
import { getModel } from "../providers";
import type { Session } from "../sessions";
import type { Entry } from "../types";
import { isAbortError } from "./abort";

export const BRANCH_SUMMARY_PREAMBLE = `The user explored a different conversation branch before returning here.
Summary of that exploration:

`;

const BRANCH_SUMMARY_PROMPT = `Create a structured summary of this conversation branch for context when returning later.

Use this EXACT format:

## Goal
[What was the user trying to accomplish in this branch?]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Work that was started but not finished]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [What should happen next to continue this work]

Keep each section concise. Preserve exact file paths, function names, and error messages.`;

/**
 * Collect the entries abandoned by navigating from oldLeafId to targetId:
 * everything from the old leaf back to (excluding) the deepest common
 * ancestor, in chronological order.
 */
export function collectEntriesForBranchSummary(
    session: Session,
    oldLeafId: string | null,
    targetId: string,
): { entries: Entry[]; commonAncestorId: string | null } {
    if (!oldLeafId) return { entries: [], commonAncestorId: null };

    const oldPath = new Set(session.getBranch(oldLeafId).map((e) => e.id));
    const targetPath = session.getBranch(targetId);

    let commonAncestorId: string | null = null;
    for (let i = targetPath.length - 1; i >= 0; i--) {
        if (oldPath.has(targetPath[i].id)) {
            commonAncestorId = targetPath[i].id!;
            break;
        }
    }

    const entries: Entry[] = [];
    let current: string | null = oldLeafId;
    while (current && current !== commonAncestorId) {
        const entry = session.getEntry(current);
        if (!entry) break;
        entries.push(entry);
        current = entry.parentId ?? null;
    }
    entries.reverse();
    return { entries, commonAncestorId };
}

function branchEntryToText(e: Entry): string | null {
    switch (e.type) {
        case "message":
            return `[${e.role}] ${typeof e.content === "string" ? e.content : JSON.stringify(e.content)}`;
        case "subagent":
            return `[subagent ${e.agent}] ${e.result}`;
        case "compact":
            return `[compaction summary] ${e.summary}`;
        case "branch-summary":
            return `[branch summary] ${e.summary}`;
        default:
            return null;
    }
}

export class BranchSummaryAbortedError extends Error {
    constructor() {
        super("branch summary aborted");
        this.name = "BranchSummaryAbortedError";
    }
}

/** Summarize an abandoned branch's entries for a branch-summary entry. */
export async function runBranchSummary(opts: {
    entries: Entry[];
    modelId: string;
    abortSignal?: AbortSignal;
    customInstructions?: string;
}): Promise<string> {
    const conversationText = opts.entries
        .map(branchEntryToText)
        .filter((t): t is string => t !== null)
        .join("\n");
    const instructions = opts.customInstructions
        ? `${BRANCH_SUMMARY_PROMPT}\n\nAdditional focus: ${opts.customInstructions}`
        : BRANCH_SUMMARY_PROMPT;

    if (opts.abortSignal?.aborted) throw new BranchSummaryAbortedError();

    const model = await getModel(opts.modelId);
    try {
        const result = await generateText({
            model,
            prompt: `<conversation>\n${conversationText}\n</conversation>\n\n${instructions}`,
            abortSignal: opts.abortSignal,
        });
        return result.text;
    } catch (err) {
        if (isAbortError(err) || opts.abortSignal?.aborted) throw new BranchSummaryAbortedError();
        throw err;
    }
}
