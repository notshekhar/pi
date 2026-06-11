/** Built-in persona + guidelines. Custom agents replace only this part —
 * the environment block below is always appended so any agent keeps
 * functioning as a coding agent (knows cwd and tools). */
export const DEFAULT_BASE_PROMPT = `You are pi-agent, a terminal coding assistant.

Guidelines:
- Prefer edit over write for existing files. Make exact unique matches.
- Run bash commands with full paths or change directory explicitly.
- Read files before editing them.
- Keep responses concise. Show diffs and outputs from tool results.
- Do not invent file paths. List or find first if unsure.`;

export function buildSystemPrompt(opts: {
    cwd: string;
    workspaceContext?: string;
    basePrompt?: string;
    /** Tool names actually available this turn (per-agent subsets). */
    tools?: string[];
}): string {
    const base = opts.basePrompt?.trim() || DEFAULT_BASE_PROMPT;
    const toolList = opts.tools?.length ? opts.tools.join(", ") : "read, write, edit, bash, ls, grep, find";
    const env = `Working directory: ${opts.cwd}

You have these tools: ${toolList}.`;
    const parts = [base, env];
    if (opts.workspaceContext) parts.push(opts.workspaceContext);
    return parts.join("\n\n");
}
