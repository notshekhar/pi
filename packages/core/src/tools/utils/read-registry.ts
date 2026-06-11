/**
 * Read-before-modify enforcement (Claude Code behavior, enforced in the
 * tools themselves instead of relying on the prompt):
 * - edit on a file never read this session → error telling the model to read first
 * - edit on a file changed on disk since it was read → error forcing a re-read
 * - write only guards files that already exist (new files pass freely)
 *
 * Process-lifetime registry keyed by absolute path; reads from subagents
 * count too — they share the process.
 */
import { statSync } from "node:fs";

const readAt = new Map<string, number>(); // absolute path → mtimeMs when read

export function recordRead(absolutePath: string): void {
    try {
        readAt.set(absolutePath, statSync(absolutePath).mtimeMs);
    } catch {
        // unreadable stat — leave unrecorded
    }
}

/** Call after a successful edit/write so follow-up edits aren't flagged stale. */
export function recordModified(absolutePath: string): void {
    recordRead(absolutePath);
}

/** Returns an error message when the modification must be blocked, else null. */
export function checkReadBeforeModify(absolutePath: string, displayPath: string): string | null {
    const seenMtime = readAt.get(absolutePath);
    if (seenMtime === undefined) {
        return `File has not been read in this session: ${displayPath}. Read it with the read tool first, then edit.`;
    }
    try {
        if (statSync(absolutePath).mtimeMs > seenMtime) {
            return `File changed on disk since it was read: ${displayPath}. Read it again before editing.`;
        }
    } catch {
        // stat failed (deleted?) — let the tool surface its own error
    }
    return null;
}
