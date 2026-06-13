/**
 * Typed access to ~/.pi/settings.json. One place declares every key and its
 * type — call sites get autocomplete and lose the `as X | undefined` casts.
 * Unknown/extra keys in the file are preserved untouched.
 */
import { settingsStore } from "./auth/storage";
import type { ThinkingLevel } from "./agent/thinking";
import type { HooksConfig } from "./agent/hooks";
import type { McpServerConfig } from "./mcp/config";

export interface PiSettings {
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
    piCompatMode?: string;
    agent?: string;
    lastChangelogVersion?: string;
    projectModels?: Record<string, string>;
    /** cwd → provider → last model picked with that provider in that folder. */
    projectProviderModels?: Record<string, Record<string, string>>;
    importClaudeHooks?: boolean;
    claudeHooksFilter?: string[];
    hooks?: HooksConfig;
    /** Master switch for MCP servers. Default on. */
    mcp?: boolean;
    /** Connected MCP servers, keyed by display name. */
    mcpServers?: Record<string, McpServerConfig>;
}

export function getSetting<K extends keyof PiSettings>(key: K): PiSettings[K] {
    return settingsStore.get(key) as PiSettings[K];
}

export function setSetting<K extends keyof PiSettings>(key: K, value: PiSettings[K]): void {
    settingsStore.set(key, value);
}
