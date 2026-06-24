/**
 * Typed access to ~/.loop/settings.json. One place declares every key and its
 * type — call sites get autocomplete and lose the `as X | undefined` casts.
 * Unknown/extra keys in the file are preserved untouched.
 */
import { settingsStore } from "./auth/storage";
import type { ThinkingLevel } from "./agent/thinking";
import type { HooksConfig } from "./agent/hooks";
import type { McpServerConfig } from "./mcp/config";
import type { BashDenyEntry } from "./tools/utils/command-deny";

export interface LoopSettings {
    defaultModel?: string;
    theme?: string;
    thinkingLevel?: ThinkingLevel;
    maxSteps?: number;
    subagentMaxSteps?: number;
    /** Master switch for the task tool (subagents). Default on. */
    subagents?: boolean;
    /** Post-turn recap under responses that wrote/edited files. Default off. */
    recap?: boolean;
    /** Live date + hh:mm:ss clock in the footer. Default off. */
    clock?: boolean;
    /** Fire /reminder alerts. Default on; set false to mute reminders entirely. */
    reminders?: boolean;
    autoCompactThreshold?: number;
    workspaceContext?: boolean;
    skills?: boolean;
    agent?: string;
    lastChangelogVersion?: string;
    projectModels?: Record<string, string>;
    /** cwd → provider → last model picked with that provider in that folder. */
    projectProviderModels?: Record<string, Record<string, string>>;
    /** Pull in hooks from ~/.claude (settings + plugins) and project .claude.
     * Default OFF — set true to opt in. */
    importClaudeHooks?: boolean;
    claudeHooksFilter?: string[];
    hooks?: HooksConfig;
    /** Master switch for MCP servers. Default ON — set false to disable entirely
     * (hides /mcp and skips auto-connect). Toggle via /settings. */
    mcp?: boolean;
    /** Connected MCP servers, keyed by display name. */
    mcpServers?: Record<string, McpServerConfig>;
    /**
     * Bash commands the agent is refused (a guardrail, not a sandbox). Entries
     * match by command name, optionally + subcommand ("git commit"). Omit the
     * key to use DEFAULT_BASH_DENY; set it (even to []) to take full control.
     */
    bashDeny?: BashDenyEntry[];
    /**
     * OS-level sandbox for the bash tool (Seatbelt on macOS, bubblewrap on
     * Linux). Off unless `enabled`. Fail-open: if it can't be enforced the
     * command still runs, with a warning. Network is "deny" by default; the
     * per-domain allowlist is not wired yet.
     */
    sandbox?: {
        enabled?: boolean;
        /**
         * Network policy. "allow" = full network, "deny" = none (default when
         * enabled), or a per-domain allowlist: `{ allow: ["*.github.com"],
         * deny?: ["*"] }` enforced by host-side proxies.
         */
        network?: "allow" | "deny" | { allow: string[]; deny?: string[] };
        /** Extra writable paths beyond defaults + the command's cwd. */
        allowWrite?: string[];
        /** Paths to deny writing even within writable regions. */
        denyWrite?: string[];
        /** Broad regions to deny reading. */
        denyRead?: string[];
        /** Re-allow reads within denied regions. */
        allowRead?: string[];
        /** Allow writes to .git/config (default false; .git/hooks always denied). */
        allowGitConfig?: boolean;
    };
}

export function getSetting<K extends keyof LoopSettings>(key: K): LoopSettings[K] {
    return settingsStore.get(key) as LoopSettings[K];
}

export function setSetting<K extends keyof LoopSettings>(key: K, value: LoopSettings[K]): void {
    settingsStore.set(key, value);
}
