/**
 * Custom agents — named system prompts under ~/.pi/agents/<name>.md.
 *
 * File format: optional frontmatter with a `tools:` line (comma-separated
 * subset of TOOL_NAMES), then the prompt body. No frontmatter = all tools.
 *
 *   ---
 *   tools: read, grep, find, ls
 *   ---
 *   You are a code reviewer...
 *
 * Built-ins: "default" (full toolset) and "plan" (read-only planning agent).
 * Saving a prompt under a built-in name overrides its prompt (delete the file
 * to reset) — built-in tool sets are fixed and never editable. Every other
 * file registers as a /<name> slash command for one-shot runs.
 */
import { existsSync, mkdirSync, readdirSync, readFileSync, unlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { getPiDir } from "../auth/storage";
import { TOOL_NAMES } from "../tools";
import { DEFAULT_BASE_PROMPT } from "./system-prompt";

export const DEFAULT_AGENT_NAME = "default";

export const PLAN_BASE_PROMPT = `You are pi-plan, a planning assistant for coding tasks. You investigate, you never modify.

Method:
1. Map the territory first — ls/find for structure, grep for the patterns and call sites involved, read the files that matter. Never plan against imagined code.
2. For broad exploration (several directories, many candidate files), delegate to subagents with the task tool — they run read-only and return focused reports, keeping your context lean.
3. Produce the plan.

The plan must contain:
- Ordered steps: which file changes, what goes where, and why that order.
- Exact anchors: function names, line references, existing patterns to mirror.
- Risks, unknowns, and any decision the user still has to make — flagged, not silently assumed.

Hard rules:
- Your write access does not exist; your subagents' write access does not exist. Investigation only.
- A plan is done when another agent could execute it without re-discovering anything.`;

const READONLY_TOOLS = ["read", "ls", "grep", "find"];

/** Built-in agents: fixed tool sets, prompt overridable via ~/.pi/agents/<name>.md. */
const BUILTINS: Record<string, { prompt: string; tools?: string[] }> = {
    [DEFAULT_AGENT_NAME]: { prompt: DEFAULT_BASE_PROMPT },
    // plan may delegate (task); its subagents fork plan (or are capped to its
    // tools), so everything it spawns stays read-only with no extra config.
    plan: { prompt: PLAN_BASE_PROMPT, tools: [...READONLY_TOOLS, "task"] },
};

export interface AgentInfo {
    name: string;
    prompt: string;
    /** Built-in: prompt overridable, tool set fixed. */
    builtin: boolean;
    /** Allowed tool names (may include "task"); undefined = all tools. */
    tools?: string[];
}

function agentsDir(): string {
    return join(getPiDir(), "agents");
}

function agentPath(name: string): string {
    return join(agentsDir(), `${name}.md`);
}

/** Slash-command safe: starts alphanumeric, then alnum/dash/underscore, ≤32 chars. */
export function isValidAgentName(name: string): boolean {
    return /^[a-z0-9][a-z0-9_-]{0,31}$/i.test(name);
}

/** Names selectable as agent tools: the file tools plus "task" (subagents). */
export const AGENT_TOOL_NAMES = [...TOOL_NAMES, "task"] as const;

function sanitizeTools(tools: string[] | undefined, valid: readonly string[]): string[] | undefined {
    if (!tools) return undefined;
    const kept = tools.filter((t) => valid.includes(t));
    if (kept.length === 0 || kept.length === valid.length) return undefined;
    return kept;
}

export function parseAgentFile(raw: string): { prompt: string; tools?: string[] } {
    const fm = /^---\n([\s\S]*?)\n---\n?/.exec(raw);
    if (!fm) return { prompt: raw.trim() };
    const prompt = raw.slice(fm[0].length).trim();
    const toolsLine = /^tools:\s*(.+)$/m.exec(fm[1]);
    const tools = toolsLine ? toolsLine[1].split(",").map((s) => s.trim()) : undefined;
    // `subagent-tools:` (removed) is ignored if present in older files —
    // subagents now inherit the spawning turn's tools instead of a config cap.
    return { prompt, tools: sanitizeTools(tools, AGENT_TOOL_NAMES) };
}

function readAgentFile(name: string): { prompt: string; tools?: string[] } | undefined {
    const p = agentPath(name);
    if (!existsSync(p)) return undefined;
    try {
        const parsed = parseAgentFile(readFileSync(p, "utf8"));
        return parsed.prompt ? parsed : undefined;
    } catch {
        return undefined;
    }
}

export function listAgents(): AgentInfo[] {
    const agents: AgentInfo[] = Object.entries(BUILTINS).map(([name, b]) => ({
        name,
        prompt: readAgentFile(name)?.prompt ?? b.prompt,
        builtin: true,
        tools: b.tools,
    }));
    const dir = agentsDir();
    if (!existsSync(dir)) return agents;
    for (const f of readdirSync(dir).sort()) {
        if (!f.endsWith(".md")) continue;
        const name = f.slice(0, -3);
        if (name in BUILTINS || !isValidAgentName(name)) continue;
        const parsed = readAgentFile(name);
        if (parsed) {
            agents.push({ name, prompt: parsed.prompt, builtin: false, tools: parsed.tools });
        }
    }
    return agents;
}

export function getAgentPrompt(name: string): string | undefined {
    return readAgentFile(name)?.prompt ?? BUILTINS[name]?.prompt;
}

/** Allowed tools for an agent (may include "task"); undefined = all. Built-in tool sets are fixed. */
export function getAgentTools(name: string): string[] | undefined {
    if (name in BUILTINS) return BUILTINS[name].tools;
    return readAgentFile(name)?.tools;
}

export function agentExists(name: string): boolean {
    return name in BUILTINS || existsSync(agentPath(name));
}

export function isBuiltinAgent(name: string): boolean {
    return name in BUILTINS;
}

/** True when a built-in has a prompt override file (i.e. can be reset). */
export function hasBuiltinOverride(name: string): boolean {
    return name in BUILTINS && existsSync(agentPath(name));
}

/** Kept for callers predating the plan agent. */
export function hasDefaultOverride(): boolean {
    return hasBuiltinOverride(DEFAULT_AGENT_NAME);
}

export function saveAgent(name: string, prompt: string, tools?: string[]): void {
    if (!isValidAgentName(name)) throw new Error(`invalid agent name: ${name}`);
    mkdirSync(agentsDir(), { recursive: true });
    // Built-in tool sets are fixed — only the prompt is persisted for them.
    const isBuiltin = name in BUILTINS;
    const effectiveTools = isBuiltin ? undefined : sanitizeTools(tools, AGENT_TOOL_NAMES);
    const fm = effectiveTools ? `---\ntools: ${effectiveTools.join(", ")}\n---\n\n` : "";
    writeFileSync(agentPath(name), fm + prompt.trim() + "\n");
}

/** Removes the agent file. For built-ins this resets the prompt override. */
export function deleteAgent(name: string): boolean {
    const p = agentPath(name);
    if (!existsSync(p)) return false;
    unlinkSync(p);
    return true;
}
