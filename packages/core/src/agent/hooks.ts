/**
 * Hooks — Claude Code–compatible lifecycle hooks (command type).
 *
 * Config lives under the `hooks` key in ~/.pi/settings.json (user) and
 * <cwd>/.pi/settings.json (project); project groups run after user groups.
 * The shape, matcher semantics, stdin payload, exit-code contract, and stdout
 * JSON fields mirror Claude Code's hooks so existing hook scripts port 1:1:
 *
 *   { "hooks": { "PreToolUse": [ { "matcher": "bash",
 *       "hooks": [ { "type": "command", "command": "./check.sh", "timeout": 60 } ] } ] } }
 *
 * Contract per command: JSON payload on stdin. Exit 0 → stdout parsed as JSON
 * (decision/block, hookSpecificOutput.permissionDecision, updatedInput,
 * additionalContext, systemMessage). Exit 2 → block, stderr is the reason.
 * Any other exit → non-blocking warning. Deliberate v1 subset: the other
 * Claude Code handler types (http/mcp_tool/prompt/agent) are not supported.
 */
import { spawn } from "node:child_process";
import { readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { settingsStore } from "../auth/storage";
import { isTrusted } from "./trust";

export type HookEvent =
  | "SessionStart"
  | "UserPromptSubmit"
  | "PreToolUse"
  | "PostToolUse"
  | "Stop"
  | "SessionEnd";

export interface HookCommand {
  type?: "command";
  command: string;
  timeout?: number;
}

export interface HookMatcherGroup {
  matcher?: string;
  hooks: HookCommand[];
}

export type HooksConfig = Partial<Record<HookEvent, HookMatcherGroup[]>>;

export interface HookPayload {
  session_id?: string;
  cwd: string;
  hook_event_name: HookEvent;
  tool_name?: string;
  tool_input?: unknown;
  tool_output?: unknown;
  prompt?: string;
  [key: string]: unknown;
}

export interface HookOutcome {
  /** A hook blocked the action (PreToolUse deny, prompt/stop block). */
  block: boolean;
  reason?: string;
  /** Extra context a hook wants injected for the model. */
  additionalContext?: string;
  /** PreToolUse: replacement tool input. */
  updatedInput?: unknown;
  /** Messages hooks want shown to the user (systemMessage / warnings). */
  messages: string[];
}

const DEFAULT_TIMEOUT_S = 60;

const SUPPORTED_EVENTS: HookEvent[] = [
  "SessionStart",
  "UserPromptSubmit",
  "PreToolUse",
  "PostToolUse",
  "Stop",
  "SessionEnd",
];

// mtime-keyed JSON cache: runHooks fires twice per tool call, so config files
// are re-checked with a cheap stat instead of a full read+parse every time.
const jsonCache = new Map<string, { mtimeMs: number; data: unknown }>();

function readJsonFile(path: string): unknown {
  let mtimeMs: number;
  try {
    mtimeMs = statSync(path).mtimeMs;
  } catch {
    return undefined;
  }
  const hit = jsonCache.get(path);
  if (hit && hit.mtimeMs === mtimeMs) return hit.data;
  let data: unknown;
  try {
    data = JSON.parse(readFileSync(path, "utf8"));
  } catch {
    data = undefined;
  }
  jsonCache.set(path, { mtimeMs, data });
  return data;
}

function readHooksFromFile(path: string): HooksConfig {
  const parsed = readJsonFile(path) as { hooks?: HooksConfig } | undefined;
  return parsed?.hooks ?? {};
}

// Claude Code tool names → our tool names, so imported Claude hooks fire
// against the right tools. Matchers that are exact names / `|` lists are
// remapped; regex matchers are left as-is (they may target either casing).
const CLAUDE_TOOL_MAP: Record<string, string> = {
  Bash: "bash",
  Edit: "edit",
  MultiEdit: "edit",
  Write: "write",
  Read: "read",
  Grep: "grep",
  Glob: "find",
  LS: "ls",
};

function remapClaudeMatcher(matcher: string | undefined): string | undefined {
  if (!matcher || !/^[\w|]+$/.test(matcher)) return matcher;
  return matcher
    .split("|")
    .map((name) => CLAUDE_TOOL_MAP[name] ?? name)
    .join("|");
}

/**
 * Import hooks already installed for Claude Code (~/.claude/settings.json,
 * <cwd>/.claude/settings.json, <cwd>/.claude/settings.local.json). The config
 * shape is identical; we keep only the events we support and remap tool-name
 * matchers to our tool names. Gated by the `importClaudeHooks` setting
 * (default on) since these are the user's own already-trusted hooks.
 */
function readClaudeHooks(files: string[]): HooksConfig {
  const merged: HooksConfig = {};
  for (const f of files) {
    mergeClaudeConfig(merged, readHooksFromFile(f));
  }
  return merged;
}

function mergeClaudeConfig(into: HooksConfig, cfg: HooksConfig, pluginRoot?: string): void {
  for (const ev of SUPPORTED_EVENTS) {
    const groups = cfg[ev];
    if (!Array.isArray(groups) || !groups.length) continue;
    const prepared = groups.map((g) => ({
      ...g,
      matcher: remapClaudeMatcher(g.matcher),
      hooks: (g.hooks ?? []).map((h) =>
        pluginRoot ? { ...h, command: h.command.replaceAll("${CLAUDE_PLUGIN_ROOT}", pluginRoot) } : h,
      ),
    }));
    into[ev] = [...(into[ev] ?? []), ...prepared];
  }
}

/**
 * Hooks shipped by enabled Claude Code plugins (e.g. caveman, superpowers).
 * Plugins are resolved through ~/.claude/settings.json `enabledPlugins` →
 * ~/.claude/plugins/installed_plugins.json install paths. A plugin defines
 * hooks either inline in .claude-plugin/plugin.json (`hooks` object), via a
 * path in that field, or in hooks/hooks.json at the plugin root.
 * `${CLAUDE_PLUGIN_ROOT}` in commands expands to the install path.
 */
function readClaudePluginHooks(home: string): HooksConfig {
  const settings = readJsonFile(join(home, ".claude", "settings.json")) as
    | { enabledPlugins?: Record<string, boolean> }
    | undefined;
  const enabled = settings?.enabledPlugins;
  if (!enabled) return {};
  const installed = readJsonFile(join(home, ".claude", "plugins", "installed_plugins.json")) as
    | { plugins?: Record<string, Array<{ installPath?: string }>> }
    | undefined;
  if (!installed?.plugins) return {};

  const unwrap = (data: unknown): HooksConfig | undefined => {
    if (!data || typeof data !== "object") return undefined;
    const wrapped = (data as { hooks?: unknown }).hooks;
    return (wrapped && typeof wrapped === "object" ? wrapped : data) as HooksConfig;
  };

  const merged: HooksConfig = {};
  for (const [key, on] of Object.entries(enabled)) {
    if (!on) continue;
    const root = installed.plugins[key]?.[0]?.installPath;
    if (!root) continue;
    const manifest = readJsonFile(join(root, ".claude-plugin", "plugin.json")) as
      | { hooks?: HooksConfig | string }
      | undefined;
    let cfg: HooksConfig | undefined;
    if (manifest?.hooks && typeof manifest.hooks === "object") cfg = manifest.hooks;
    else if (typeof manifest?.hooks === "string") cfg = unwrap(readJsonFile(join(root, manifest.hooks)));
    else cfg = unwrap(readJsonFile(join(root, "hooks", "hooks.json")));
    if (cfg) mergeClaudeConfig(merged, cfg, root);
  }
  return merged;
}

export function loadHooksConfig(cwd: string): HooksConfig {
  const home = process.env.HOME ?? process.env.USERPROFILE ?? "";
  const importClaude = (settingsStore.get("importClaudeHooks") as boolean | undefined) !== false;
  // User-global hooks (the user's own machine config) always load.
  const userPi = (settingsStore.get("hooks") as HooksConfig | undefined) ?? {};
  const userClaude = importClaude ? readClaudeHooks([join(home, ".claude", "settings.json")]) : {};

  // Project hooks come from the repo — gated behind project trust, so opening
  // an untrusted clone doesn't run its .pi/.claude hooks.
  let projectPi: HooksConfig = {};
  let projectClaude: HooksConfig = {};
  if (isTrusted(cwd)) {
    projectPi = readHooksFromFile(join(cwd, ".pi", "settings.json"));
    projectClaude = importClaude
      ? readClaudeHooks([join(cwd, ".claude", "settings.json"), join(cwd, ".claude", "settings.local.json")])
      : {};
  }

  const merged: HooksConfig = {};
  const layers = [userClaude, userPi, projectClaude, projectPi];
  const events = new Set(layers.flatMap((l) => Object.keys(l)) as HookEvent[]);
  for (const ev of events) {
    merged[ev] = layers.flatMap((l) => l[ev] ?? []);
  }
  return merged;
}

/** Claude Code matcher semantics: empty/"*" = all; word chars + `|` = exact list; else regex. */
export function matcherTest(matcher: string | undefined, value: string | undefined): boolean {
  if (!matcher || matcher === "*") return true;
  if (value === undefined) return true; // events without a matcher target
  if (/^[\w|]+$/.test(matcher)) {
    return matcher.split("|").includes(value);
  }
  try {
    return new RegExp(matcher).test(value);
  } catch {
    return false;
  }
}

interface CommandResult {
  code: number | null;
  stdout: string;
  stderr: string;
  timedOut: boolean;
}

function runCommand(cmd: HookCommand, payload: HookPayload, cwd: string): Promise<CommandResult> {
  return new Promise((resolve) => {
    const child = spawn("/bin/sh", ["-c", cmd.command], {
      cwd,
      // PI_PROJECT_DIR for pi hooks; CLAUDE_PROJECT_DIR so imported Claude
      // hooks that reference ${CLAUDE_PROJECT_DIR} keep working unchanged.
      env: { ...process.env, PI_PROJECT_DIR: cwd, CLAUDE_PROJECT_DIR: cwd },
      stdio: ["pipe", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    let timedOut = false;
    const timer = setTimeout(
      () => {
        timedOut = true;
        child.kill("SIGKILL");
      },
      (cmd.timeout ?? DEFAULT_TIMEOUT_S) * 1000,
    );
    child.stdout.on("data", (d: Buffer) => (stdout += d.toString()));
    child.stderr.on("data", (d: Buffer) => (stderr += d.toString()));
    child.on("error", () => {
      clearTimeout(timer);
      resolve({ code: 127, stdout, stderr: stderr || "failed to spawn hook command", timedOut });
    });
    child.on("close", (code) => {
      clearTimeout(timer);
      resolve({ code, stdout, stderr, timedOut });
    });
    child.stdin.write(JSON.stringify(payload));
    child.stdin.end();
  });
}

interface HookJsonOutput {
  decision?: string;
  reason?: string;
  systemMessage?: string;
  hookSpecificOutput?: {
    permissionDecision?: string;
    permissionDecisionReason?: string;
    additionalContext?: string;
    updatedInput?: unknown;
  };
}

/**
 * Run all configured hooks for an event sequentially. First block wins and
 * stops the chain (matches the practical Claude Code behavior for decisions).
 */
export async function runHooks(
  event: HookEvent,
  matcherValue: string | undefined,
  payload: Omit<HookPayload, "hook_event_name">,
  cwd: string,
): Promise<HookOutcome> {
  const outcome: HookOutcome = { block: false, messages: [] };
  const groups = loadHooksConfig(cwd)[event];
  if (!groups?.length) return outcome;

  const fullPayload: HookPayload = { ...payload, cwd, hook_event_name: event };
  const contexts: string[] = [];

  for (const group of groups) {
    if (!matcherTest(group.matcher, matcherValue)) continue;
    for (const cmd of group.hooks ?? []) {
      if (cmd.type && cmd.type !== "command") continue; // v1: command hooks only
      const res = await runCommand(cmd, fullPayload, cwd);

      if (res.timedOut) {
        outcome.messages.push(`hook timed out (${event}): ${cmd.command}`);
        continue;
      }
      if (res.code === 2) {
        outcome.block = true;
        outcome.reason = res.stderr.trim() || `blocked by ${event} hook`;
        return outcome;
      }
      if (res.code !== 0) {
        outcome.messages.push(`hook failed (${event}, exit ${res.code}): ${res.stderr.trim() || cmd.command}`);
        continue;
      }
      const text = res.stdout.trim();
      if (!text) continue;
      let parsed: HookJsonOutput;
      try {
        parsed = JSON.parse(text) as HookJsonOutput;
      } catch {
        // Non-JSON stdout: for SessionStart/UserPromptSubmit it's context
        // (Claude Code behavior); elsewhere show it to the user.
        if (event === "SessionStart" || event === "UserPromptSubmit") contexts.push(text);
        else outcome.messages.push(text);
        continue;
      }
      const hso = parsed.hookSpecificOutput;
      if (parsed.systemMessage) outcome.messages.push(parsed.systemMessage);
      if (hso?.additionalContext) contexts.push(hso.additionalContext);
      if (hso?.updatedInput !== undefined) outcome.updatedInput = hso.updatedInput;
      if (parsed.decision === "block" || hso?.permissionDecision === "deny") {
        outcome.block = true;
        outcome.reason =
          parsed.reason ?? hso?.permissionDecisionReason ?? `blocked by ${event} hook`;
        return outcome;
      }
    }
  }

  if (contexts.length) outcome.additionalContext = contexts.join("\n");
  return outcome;
}
