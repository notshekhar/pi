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

export const PLAN_BASE_PROMPT = `You are pi-plan, a planning assistant for coding tasks.

Explore the codebase with your read-only tools and produce a concrete implementation plan.

Guidelines:
- Investigate relevant files, structure, and conventions before proposing anything.
- Output a step-by-step plan: which files change, what goes where, in what order.
- Call out risks, unknowns, and decisions the user still has to make.
- Do NOT modify anything — your toolset is read-only by design.
- Keep the plan actionable enough that another agent could execute it directly.`;

const PLAN_TOOLS = ["read", "ls", "grep", "find"];

/** Built-in agents: fixed tool sets, prompt overridable via ~/.pi/agents/<name>.md. */
const BUILTINS: Record<string, { prompt: string; tools?: string[] }> = {
    [DEFAULT_AGENT_NAME]: { prompt: DEFAULT_BASE_PROMPT },
    plan: { prompt: PLAN_BASE_PROMPT, tools: PLAN_TOOLS },
};

export interface AgentInfo {
    name: string;
    prompt: string;
    /** Built-in: prompt overridable, tool set fixed. */
    builtin: boolean;
    /** Allowed tool names; undefined = all tools. */
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

function sanitizeTools(tools: string[] | undefined): string[] | undefined {
    if (!tools) return undefined;
    const valid = tools.filter((t) => (TOOL_NAMES as readonly string[]).includes(t));
    if (valid.length === 0 || valid.length === TOOL_NAMES.length) return undefined;
    return valid;
}

function parseAgentFile(raw: string): { prompt: string; tools?: string[] } {
    const fm = /^---\n([\s\S]*?)\n---\n?/.exec(raw);
    if (!fm) return { prompt: raw.trim() };
    const prompt = raw.slice(fm[0].length).trim();
    const toolsLine = /^tools:\s*(.+)$/m.exec(fm[1]);
    const tools = toolsLine ? toolsLine[1].split(",").map((s) => s.trim()) : undefined;
    return { prompt, tools: sanitizeTools(tools) };
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
        if (parsed) agents.push({ name, prompt: parsed.prompt, builtin: false, tools: parsed.tools });
    }
    return agents;
}

export function getAgentPrompt(name: string): string | undefined {
    return readAgentFile(name)?.prompt ?? BUILTINS[name]?.prompt;
}

/** Allowed tools for an agent; undefined = all. Built-in tool sets are fixed. */
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
    const effectiveTools = name in BUILTINS ? undefined : sanitizeTools(tools);
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
