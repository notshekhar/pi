import { createBashTool } from "./bash";
import { createEditTool } from "./edit";
import { createFindTool } from "./find";
import { createGrepTool } from "./grep";
import { createLsTool } from "./ls";
import { createReadTool } from "./read";
import { createSqlTool } from "./sql";
import { createWriteTool } from "./write";

export interface ToolContext {
    cwd: string;
    abortSignal?: AbortSignal;
    shellPath?: string;
    commandPrefix?: string;
    /**
     * Force bash into a fail-closed, kernel-enforced read-only sandbox (no
     * writable cwd). Set for read-only agents (e.g. plan) that get bash but no
     * write/edit, so bash physically cannot mutate the filesystem.
     */
    readOnlyFs?: boolean;
}

export function createTools(ctx: ToolContext) {
    return {
        read: createReadTool(ctx),
        write: createWriteTool(ctx),
        edit: createEditTool(ctx),
        bash: createBashTool(ctx),
        ls: createLsTool(ctx),
        grep: createGrepTool(ctx),
        find: createFindTool(ctx),
        // sql is read-only by enforcement (SELECT/WITH/EXPLAIN/SHOW/DESCRIBE), so
        // it lives in the main toolset like any other tool: the default agent
        // gets it automatically, and restricted agents keep it only if their
        // allowlist names it (plan/data-analyst do, via READONLY_TOOLS).
        sql: createSqlTool(ctx),
    };
}

export type ToolSet = ReturnType<typeof createTools>;

/** Stable tool name list — agents reference these for per-agent tool selection. */
export const TOOL_NAMES = ["read", "write", "edit", "bash", "ls", "grep", "find", "sql"] as const;
export type ToolName = (typeof TOOL_NAMES)[number];
export { clearReadRegistry } from "./utils/read-registry";
// Bash denylist guardrail — types + defaults for the /bashdeny management UI.
export {
    DEFAULT_BASH_DENY,
    denyPattern,
    findDeniedCommand,
    formatDenyRefusal,
    type BashDenyEntry,
} from "./utils/command-deny";
// Resolves the bundled/downloaded `fd` & `rg` binaries — also used by the CLI to
// power @-mention fuzzy file search in the editor's autocomplete.
export { ensureTool, getToolPath } from "./utils/tools-manager";

export {
    createBashTool,
    createEditTool,
    createFindTool,
    createGrepTool,
    createLsTool,
    createReadTool,
    createSqlTool,
    createWriteTool,
};
