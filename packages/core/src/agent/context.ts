import { existsSync, readFileSync, statSync, watch, type FSWatcher } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";

const CONTEXT_FILES = ["AGENTS.md", "CLAUDE.md", ".pi/AGENTS.md", ".pi/CLAUDE.md"];
// Global instructions, loaded everywhere (mirrors Claude's ~/.claude/CLAUDE.md).
// Both are loaded if present; pi writes AGENTS.md by default, but a user-authored
// ~/.pi/CLAUDE.md is honored too.
const GLOBAL_CONTEXT_FILES = [join(homedir(), ".pi", "AGENTS.md"), join(homedir(), ".pi", "CLAUDE.md")];
const MAX_FILE_BYTES = 64 * 1024;

export interface WorkspaceContext {
    text: string;
    files: string[];
}

function findRepoRoot(start: string): string {
    let dir = start;
    for (;;) {
        if (existsSync(join(dir, ".git"))) return dir;
        const parent = dirname(dir);
        if (parent === dir) return start;
        dir = parent;
    }
}

export function loadWorkspaceContext(cwd: string): WorkspaceContext {
    const root = findRepoRoot(cwd);
    const dirsToCheck = new Set<string>([root, cwd]);
    // walk cwd up to root, collecting any intermediate dir
    let dir = cwd;
    while (dir !== root && dir !== dirname(dir)) {
        dirsToCheck.add(dir);
        dir = dirname(dir);
    }

    // Global files first so they read as base rules; workspace files can refine them.
    const candidatePaths: string[] = [...GLOBAL_CONTEXT_FILES];
    for (const d of dirsToCheck) {
        for (const rel of CONTEXT_FILES) candidatePaths.push(join(d, rel));
    }

    const collected: string[] = [];
    const files: string[] = [];
    const seen = new Set<string>();
    for (const p of candidatePaths) {
        if (seen.has(p)) continue;
        seen.add(p);
        if (!existsSync(p)) continue;
        try {
            const stat = statSync(p);
            if (!stat.isFile()) continue;
            let content = readFileSync(p, "utf8");
            if (Buffer.byteLength(content) > MAX_FILE_BYTES) {
                content = content.slice(0, MAX_FILE_BYTES) + "\n...[truncated]";
            }
            collected.push(`<file path="${p}">\n${content}\n</file>`);
            files.push(p);
        } catch {}
    }

    if (collected.length === 0) return { text: "", files: [] };
    return {
        text: `<workspace-context>\n${collected.join("\n")}\n</workspace-context>`,
        files,
    };
}

export function watchWorkspaceContext(files: string[], onChange: () => void): () => void {
    const watchers: FSWatcher[] = [];
    for (const f of files) {
        try {
            watchers.push(watch(f, () => onChange()));
        } catch {}
    }
    return () => {
        for (const w of watchers) w.close();
    };
}
