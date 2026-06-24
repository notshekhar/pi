/**
 * Custom agents — named system prompts under ~/.loop/agents/<name>.md.
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
import { getLoopDir } from "../auth/storage";
import { TOOL_NAMES } from "../tools";
import { DEFAULT_BASE_PROMPT } from "./system-prompt";
import { getExtensionHost } from "../extensions";
import { isSandboxSupported } from "@notshekhar/loop-sandbox";

export const DEFAULT_AGENT_NAME = "default";

export const PLAN_BASE_PROMPT = `You are loop-plan, a planning assistant for coding tasks. You investigate, you never modify.

Method:
1. Map the territory first — ls/find for structure, grep for the patterns and call sites involved, read the files that matter. Never plan against imagined code.
2. Use bash for read-only investigation only: inspect state and gather facts (git log/status/diff, ls, cat, build/test/type-check output, dependency versions, which/--version). Never use it to change anything.
3. For broad exploration (several directories, many candidate files), delegate to subagents with the task tool — they run read-only and return focused reports, keeping your context lean.
4. Produce the plan.

The plan must contain:
- Ordered steps: which file changes, what goes where, and why that order.
- Exact anchors: function names, line references, existing patterns to mirror.
- Risks, unknowns, and any decision the user still has to make — flagged, not silently assumed.

Hard rules:
- You have no write or edit tools — you cannot create or modify files. bash is for inspection ONLY: never run commands that mutate the filesystem, repo, or environment (no \`>\`/\`>>\` redirects into files, \`sed -i\`, \`rm\`, \`mv\`, \`git commit\`/\`checkout\`/\`apply\`, \`npm install\`, etc.). The same restriction binds your subagents.
- A plan is done when another agent could execute it without re-discovering anything.`;

// `sql` is read-only by enforcement (only SELECT/WITH/EXPLAIN/SHOW/DESCRIBE), so
// it rides along with the read-only file tools — every agent that gets those
// gets it too.
const READONLY_TOOLS = ["read", "ls", "grep", "find", "sql"];

// plan's tools. bash is included only when the OS sandbox can enforce read-only
// on this platform (see the plan BUILTINS comment). isSandboxSupported() is a
// cheap, deterministic platform check, so resolving once at module load is fine.
const PLAN_TOOLS = isSandboxSupported() ? [...READONLY_TOOLS, "bash", "task"] : [...READONLY_TOOLS, "task"];

export const ANALYST_BASE_PROMPT = `You are loop-data-analyst, a precise data assistant. Correctness over completeness — if a table, column, value, or range is missing or ambiguous, ASK the user instead of guessing.

Default to the sql tool — it is your primary instrument. Reach for read/ls/grep/find only when you actually need to inspect project files (migrations, models, docs); answer data questions with sql, not by reading files.

Method:
1. Map the schema before writing real queries. Use the sql tool against information_schema (tables, columns, constraints) or the dialect's catalog to learn structure — never plan a query against imagined tables or columns.
2. The user works through named connections. Every sql call needs a connectionId; if you don't know which connection to use, ask.
3. Use read/ls/grep/find to inspect the project (migrations, models, SQL files, docs) for schema and business logic before querying.

Hard rules:
- Never guess table names, columns, joins, enums, statuses, or business logic.
- Never assume a data point — dates, time ranges, thresholds, IDs, metric definitions, filter values. If it isn't explicitly given and the result depends on it, ASK first.
- You are read-only: you have no write/edit/bash, and the sql tool rejects anything but SELECT/WITH/EXPLAIN/SHOW/DESCRIBE. Never attempt to mutate data.
- Always add a LIMIT (default LIMIT 100) to exploratory queries.
- Never expose raw PII unless explicitly asked; mask otherwise.

Output:
- Summarize results concisely — counts, totals, trends — don't dump raw rows as text.
- State the SQL you ran and which connection, so the user can verify.
- Surface query errors plainly with the failing SQL.

Correct > Fast · Ask > Assume · Silence > Hallucination`;

export const DATA_ANALYST_AGENT_NAME = "data-analyst";

/** Built-in agents: fixed tool sets, prompt overridable via ~/.loop/agents/<name>.md. */
const BUILTINS: Record<string, { prompt: string; tools?: string[]; hidden?: boolean }> = {
    // default is unrestricted, so it gets every tool including sql.
    [DEFAULT_AGENT_NAME]: { prompt: DEFAULT_BASE_PROMPT },
    // plan may delegate (task) and query data (sql, via READONLY_TOOLS); it has
    // NO write/edit tools. It also gets bash for inspection — but ONLY where the
    // OS sandbox can enforce read-only (macOS/Linux): the agent loop forces
    // plan's bash into a fail-closed read-only sandbox (no writable cwd), so the
    // kernel — not just the prompt — guarantees it can't mutate anything. On
    // platforms without sandbox support (e.g. Windows) bash is withheld entirely
    // rather than handed over with only a prompt-level restriction.
    plan: { prompt: PLAN_BASE_PROMPT, tools: PLAN_TOOLS },
    // data-analyst: identical tool set to plan — the only difference is the
    // prompt. `hidden` keeps it out of the Tab cycle until the user selects it.
    [DATA_ANALYST_AGENT_NAME]: { prompt: ANALYST_BASE_PROMPT, tools: [...READONLY_TOOLS, "task"], hidden: true },
};

export interface AgentInfo {
    name: string;
    prompt: string;
    /** Built-in: prompt overridable, tool set fixed. */
    builtin: boolean;
    /** Allowed tool names (may include "task"); undefined = all tools. */
    tools?: string[];
    /** Built-in kept out of the Tab cycle until the user selects it. */
    hidden?: boolean;
}

function agentsDir(): string {
    return join(getLoopDir(), "agents");
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

/**
 * Tool names valid in an agent allowlist: the builtins/task plus any tools
 * extensions have registered. Dynamic so a custom agent can name an extension
 * tool without it being silently dropped. Equals AGENT_TOOL_NAMES when no
 * extensions are loaded (zero-regression).
 */
function agentToolNames(): string[] {
    return [...AGENT_TOOL_NAMES, ...getExtensionHost().getTools().add.keys()];
}

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
    return { prompt, tools: sanitizeTools(tools, agentToolNames()) };
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
        hidden: b.hidden,
    }));
    const dir = agentsDir();
    if (existsSync(dir)) {
        for (const f of readdirSync(dir).sort()) {
            if (!f.endsWith(".md")) continue;
            const name = f.slice(0, -3);
            if (name in BUILTINS || !isValidAgentName(name)) continue;
            const parsed = readAgentFile(name);
            if (parsed) {
                agents.push({ name, prompt: parsed.prompt, builtin: false, tools: parsed.tools });
            }
        }
    }
    // Extension-registered agents, last — they cannot shadow a builtin or a
    // user's file-based agent of the same name. None when no extensions loaded.
    const existing = new Set(agents.map((a) => a.name));
    for (const ea of getExtensionHost().getAgents()) {
        if (existing.has(ea.name) || !isValidAgentName(ea.name)) continue;
        agents.push({ name: ea.name, prompt: ea.prompt, builtin: false, tools: ea.tools });
        existing.add(ea.name);
    }
    return agents;
}

export function getAgentPrompt(name: string): string | undefined {
    const own = readAgentFile(name)?.prompt ?? BUILTINS[name]?.prompt;
    if (own) return own;
    return getExtensionHost()
        .getAgents()
        .find((a) => a.name === name)?.prompt;
}

/**
 * Allowed tools for an agent (may include "task"); undefined = all. Built-in
 * tool sets are fixed. Extension `api.tools.grant(agent, tool)` augments a
 * restricted agent's allowlist; an all-tools agent already has everything, so
 * grants don't apply there. No grants/ext agents → identical to before.
 */
export function getAgentTools(name: string): string[] | undefined {
    let base: string[] | undefined;
    if (name in BUILTINS) {
        base = BUILTINS[name].tools;
    } else {
        const file = readAgentFile(name);
        base = file
            ? file.tools
            : getExtensionHost()
                  .getAgents()
                  .find((a) => a.name === name)?.tools;
    }
    if (base === undefined) return undefined; // all tools — grants already covered
    const grants = getExtensionHost().getToolGrants(name);
    return grants.length === 0 ? base : [...new Set([...base, ...grants])];
}

export function agentExists(name: string): boolean {
    return (
        name in BUILTINS ||
        existsSync(agentPath(name)) ||
        getExtensionHost()
            .getAgents()
            .some((a) => a.name === name)
    );
}

/**
 * Whether an agent's effective tool list means its bash must run read-only:
 * bash is allowed but neither write nor edit is. Such agents (e.g. plan) can
 * inspect via bash but must not mutate, so the bash tool forces a fail-closed
 * read-only OS sandbox. `undefined` (all tools) is NOT read-only.
 */
export function isReadOnlyBashAgent(tools: string[] | undefined): boolean {
    if (!tools) return false;
    return tools.includes("bash") && !tools.includes("write") && !tools.includes("edit");
}

export function isBuiltinAgent(name: string): boolean {
    return name in BUILTINS;
}

/** A built-in that stays out of the Tab cycle until explicitly selected. */
export function isHiddenAgent(name: string): boolean {
    return BUILTINS[name]?.hidden === true;
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
    const effectiveTools = isBuiltin ? undefined : sanitizeTools(tools, agentToolNames());
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
