/**
 * Custom agents — named system prompts under ~/.pi/agents/<name>.md.
 *
 * "default" is the built-in pi prompt; saving a prompt under that name
 * overrides it (delete the file to reset). Every other file registers as a
 * /<name> slash command that switches the active agent. Only the persona/
 * instruction text is per-agent — the environment block (cwd, tools) and
 * workspace context are always appended by buildSystemPrompt so custom
 * agents keep working as coding agents.
 */
import { existsSync, mkdirSync, readdirSync, readFileSync, unlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { getPiDir } from "../auth/storage";
import { DEFAULT_BASE_PROMPT } from "./system-prompt";

export const DEFAULT_AGENT_NAME = "default";

export interface AgentInfo {
  name: string;
  prompt: string;
  /** true for "default" (built-in, file-overridable). */
  builtin: boolean;
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

export function listAgents(): AgentInfo[] {
  const agents: AgentInfo[] = [
    { name: DEFAULT_AGENT_NAME, prompt: getAgentPrompt(DEFAULT_AGENT_NAME) ?? DEFAULT_BASE_PROMPT, builtin: true },
  ];
  const dir = agentsDir();
  if (!existsSync(dir)) return agents;
  for (const f of readdirSync(dir).sort()) {
    if (!f.endsWith(".md")) continue;
    const name = f.slice(0, -3);
    if (name === DEFAULT_AGENT_NAME || !isValidAgentName(name)) continue;
    try {
      agents.push({ name, prompt: readFileSync(join(dir, f), "utf8").trim(), builtin: false });
    } catch {
      // unreadable file — skip
    }
  }
  return agents;
}

export function getAgentPrompt(name: string): string | undefined {
  const p = agentPath(name);
  if (existsSync(p)) {
    try {
      const text = readFileSync(p, "utf8").trim();
      if (text) return text;
    } catch {
      // fall through
    }
  }
  return name === DEFAULT_AGENT_NAME ? DEFAULT_BASE_PROMPT : undefined;
}

export function agentExists(name: string): boolean {
  return name === DEFAULT_AGENT_NAME || existsSync(agentPath(name));
}

/** True when "default" has a file override (i.e. can be reset). */
export function hasDefaultOverride(): boolean {
  return existsSync(agentPath(DEFAULT_AGENT_NAME));
}

export function saveAgent(name: string, prompt: string): void {
  if (!isValidAgentName(name)) throw new Error(`invalid agent name: ${name}`);
  mkdirSync(agentsDir(), { recursive: true });
  writeFileSync(agentPath(name), prompt.trim() + "\n");
}

/** Removes the agent file. For "default" this resets to the built-in prompt. */
export function deleteAgent(name: string): boolean {
  const p = agentPath(name);
  if (!existsSync(p)) return false;
  unlinkSync(p);
  return true;
}
