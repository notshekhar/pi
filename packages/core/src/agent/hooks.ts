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
 * additionalContext, systemMessage, terminalSequence). Exit 2 → block, stderr
 * is the reason. Any other exit → non-blocking warning. Deliberate v1 subset:
 * the other Claude Code handler types (http/mcp_tool/prompt/agent) are not
 * supported.
 *
 * Agent-state watchers (herdr, Warp, …) consume these events to know whether
 * the agent is working / waiting / done: UserPromptSubmit = working,
 * Stop = done, Notification = needs attention. They get the same payloads and
 * env passthrough they get from Claude Code, plus `terminalSequence` output
 * so they can emit OSC notifications without touching /dev/tty (which would
 * tear our TUI). `hookBus` exposes start/end of every hook command for UI.
 */
import { spawn } from "node:child_process";
import { EventEmitter } from "node:events";
import { readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { settingsStore } from "../auth/storage";
import { isTrusted } from "./trust";

export type HookEvent =
    | "SessionStart"
    | "UserPromptSubmit"
    | "PreToolUse"
    | "PostToolUse"
    | "Notification"
    | "PermissionRequest"
    | "PreCompact"
    | "SubagentStop"
    | "Stop"
    | "SessionEnd";

export interface HookCommand {
    type?: "command";
    command: string;
    timeout?: number;
    /** Fire-and-forget: run without blocking the agent or contributing to the outcome. */
    async?: boolean;
    /** Shown by the UI while the hook runs (plugin hooks ship this). */
    statusMessage?: string;
}

/**
 * Observable hook activity: emits "start" ({event, command, statusMessage?})
 * and "end" ({event, command, code, timedOut, durationMs}) for every hook
 * command. Lets UIs show progress and future integrations subscribe without
 * touching the dispatch path.
 */
export const hookBus = new EventEmitter();

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
    /** Raw OSC sequences hooks want written to the terminal (Warp-style notifications). */
    terminalSequences: string[];
}

const DEFAULT_TIMEOUT_S = 60;

// SubagentStop is accepted from config for forward-compat but never fired —
// pi has no subagents.
const SUPPORTED_EVENTS: HookEvent[] = [
    "SessionStart",
    "UserPromptSubmit",
    "PreToolUse",
    "PostToolUse",
    "Notification",
    "PermissionRequest",
    "PreCompact",
    "SubagentStop",
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
/**
 * Allowlist for imported Claude Code hooks: `claudeHooksFilter` setting, an
 * array of lowercase substrings matched against the hook command and (for
 * plugins) the plugin key. Unset/empty = import everything. Lets users keep
 * e.g. only their agent-state watchers (["caveman", "herdr", "warp"]) without
 * dragging every Claude hook into pi.
 */
function claudeImportFilter(): string[] | null {
    const v = settingsStore.get("claudeHooksFilter") as unknown;
    if (!Array.isArray(v) || v.length === 0) return null;
    return v.map((s) => String(s).toLowerCase());
}

function matchesFilter(filter: string[] | null, text: string): boolean {
    if (!filter) return true;
    const lower = text.toLowerCase();
    return filter.some((term) => lower.includes(term));
}

function readClaudeHooks(files: string[]): HooksConfig {
    const merged: HooksConfig = {};
    for (const f of files) {
        mergeClaudeConfig(merged, readHooksFromFile(f), undefined, claudeImportFilter());
    }
    return merged;
}

function mergeClaudeConfig(into: HooksConfig, cfg: HooksConfig, pluginRoot?: string, filter?: string[] | null): void {
    for (const ev of SUPPORTED_EVENTS) {
        const groups = cfg[ev];
        if (!Array.isArray(groups) || !groups.length) continue;
        const prepared = groups
            .map((g) => ({
                ...g,
                matcher: remapClaudeMatcher(g.matcher),
                hooks: (g.hooks ?? [])
                    .map((h) =>
                        pluginRoot ? { ...h, command: h.command.replaceAll("${CLAUDE_PLUGIN_ROOT}", pluginRoot) } : h,
                    )
                    .filter((h) => matchesFilter(filter ?? null, h.command)),
            }))
            .filter((g) => g.hooks.length > 0);
        if (prepared.length) into[ev] = [...(into[ev] ?? []), ...prepared];
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

    const filter = claudeImportFilter();
    const merged: HooksConfig = {};
    for (const [key, on] of Object.entries(enabled)) {
        if (!on) continue;
        // Filter matches the plugin key ("caveman@caveman", "warp@claude-code-warp");
        // an allowed plugin imports all of its hooks.
        if (!matchesFilter(filter, key)) continue;
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
    // A broken config file must never take the agent down.
    try {
        return loadHooksConfigUnsafe(cwd);
    } catch {
        return {};
    }
}

function loadHooksConfigUnsafe(cwd: string): HooksConfig {
    const home = process.env.HOME ?? process.env.USERPROFILE ?? "";
    const importClaude = (settingsStore.get("importClaudeHooks") as boolean | undefined) !== false;
    // User-global hooks (the user's own machine config) always load.
    const userPi = (settingsStore.get("hooks") as HooksConfig | undefined) ?? {};
    const userClaude = importClaude ? readClaudeHooks([join(home, ".claude", "settings.json")]) : {};
    const userClaudePlugins = importClaude ? readClaudePluginHooks(home) : {};

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
    const layers = [userClaude, userClaudePlugins, userPi, projectClaude, projectPi];
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

// The Claude Code hook API surface we emulate. Advertised via
// CLAUDE_CODE_VERSION so version-sniffing integrations (e.g. Warp) take the
// structured `terminalSequence` output path instead of writing raw OSC to
// /dev/tty, which tears our TUI. 2.1.141 introduced terminalSequence.
const CLAUDE_HOOK_API_VERSION = "2.1.141";

/** Cap captured stdout/stderr per hook — runaway output must not eat memory. */
const MAX_CAPTURE_BYTES = 1_000_000;

function clampTimeoutS(t: number | undefined): number {
    if (typeof t !== "number" || !Number.isFinite(t) || t <= 0) return DEFAULT_TIMEOUT_S;
    return Math.min(t, 600);
}

function safeStringify(value: unknown): string {
    try {
        return JSON.stringify(value) ?? "{}";
    } catch {
        return "{}"; // circular tool output — hook still gets a valid payload
    }
}

/** hookBus subscribers are app code — their bugs must not break dispatch. */
function busEmit(event: string, data: unknown): void {
    try {
        hookBus.emit(event, data);
    } catch {
        // listener threw — ignore
    }
}

function runCommand(cmd: HookCommand, payload: HookPayload, cwd: string): Promise<CommandResult> {
    const startedAt = Date.now();
    const event = payload.hook_event_name;
    busEmit("start", { event, command: cmd.command, statusMessage: cmd.statusMessage });
    return new Promise((resolve) => {
        // sh on POSIX; cmd.exe on Windows builds (no /bin/sh there).
        const isWin = process.platform === "win32";
        const child = spawn(isWin ? "cmd.exe" : "/bin/sh", [isWin ? "/c" : "-c", cmd.command], {
            cwd,
            // PI_PROJECT_DIR for pi hooks; CLAUDE_PROJECT_DIR so imported Claude
            // hooks that reference ${CLAUDE_PROJECT_DIR} keep working unchanged.
            // Watcher env (HERDR_*, etc.) flows through untouched via process.env.
            env: {
                ...process.env,
                PI_PROJECT_DIR: cwd,
                CLAUDE_PROJECT_DIR: cwd,
                CLAUDE_CODE_VERSION: CLAUDE_HOOK_API_VERSION,
            },
            stdio: ["pipe", "pipe", "pipe"],
            // Own process group so a timeout kills the shell's children too.
            detached: true,
        });
        let stdout = "";
        let stderr = "";
        let timedOut = false;
        const timer = setTimeout(
            () => {
                timedOut = true;
                // Negative pid → kill the whole process group, not just /bin/sh.
                try {
                    process.kill(-child.pid!, "SIGKILL");
                } catch {
                    child.kill("SIGKILL");
                }
            },
            clampTimeoutS(cmd.timeout) * 1000,
        );
        child.stdout.on("data", (d: Buffer) => {
            if (stdout.length < MAX_CAPTURE_BYTES) stdout += d.toString();
        });
        child.stderr.on("data", (d: Buffer) => {
            if (stderr.length < MAX_CAPTURE_BYTES) stderr += d.toString();
        });
        let finished = false;
        const finish = (code: number | null) => {
            if (finished) return; // spawn-error then close would double-fire
            finished = true;
            clearTimeout(timer);
            busEmit("end", {
                event,
                command: cmd.command,
                statusMessage: cmd.statusMessage,
                code,
                timedOut,
                durationMs: Date.now() - startedAt,
            });
            resolve({
                code,
                stdout,
                stderr: code === 127 && !stderr ? "failed to spawn hook command" : stderr,
                timedOut,
            });
        };
        child.on("error", () => finish(127));
        child.on("close", (code) => finish(code));
        // A hook may exit without reading stdin — swallow EPIPE instead of
        // crashing the whole process on an unhandled stream error.
        child.stdin.on("error", () => {});
        child.stdin.write(safeStringify(payload));
        child.stdin.end();
    });
}

interface HookJsonOutput {
    decision?: string;
    reason?: string;
    continue?: boolean;
    stopReason?: string;
    systemMessage?: string;
    terminalSequence?: string;
    hookSpecificOutput?: {
        permissionDecision?: string;
        permissionDecisionReason?: string;
        additionalContext?: string;
        updatedInput?: unknown;
    };
}

/**
 * Fold one command's result into the outcome. Output (messages, context,
 * terminal sequences, updatedInput) is always collected; block decisions are
 * first-wins in config order — a later hook never overrides an earlier block.
 */
function applyResult(
    event: HookEvent,
    cmd: HookCommand,
    res: CommandResult,
    outcome: HookOutcome,
    contexts: string[],
): void {
    // User-facing strings get clipped — a hook spewing megabytes must not
    // flood the chat. additionalContext stays full: it's model-bound by design.
    const clip = (s: string, n: number) => (s.length > n ? `${s.slice(0, n)}… [truncated]` : s);
    const block = (reason: string) => {
        if (!outcome.block) {
            outcome.block = true;
            outcome.reason = clip(reason, 500);
        }
    };
    if (res.timedOut) {
        outcome.messages.push(`hook timed out (${event}): ${clip(cmd.command, 200)}`);
        return;
    }
    if (res.code === 2) {
        block(res.stderr.trim() || `blocked by ${event} hook`);
        return;
    }
    if (res.code !== 0) {
        outcome.messages.push(
            `hook failed (${event}, exit ${res.code}): ${clip(res.stderr.trim() || cmd.command, 500)}`,
        );
        return;
    }
    const text = res.stdout.trim();
    if (!text) return;
    let parsed: HookJsonOutput;
    try {
        parsed = JSON.parse(text) as HookJsonOutput;
    } catch {
        // Non-JSON stdout: for SessionStart/UserPromptSubmit it's context
        // (Claude Code behavior); elsewhere show it to the user.
        if (event === "SessionStart" || event === "UserPromptSubmit") contexts.push(text);
        else outcome.messages.push(clip(text, 1000));
        return;
    }
    if (!parsed || typeof parsed !== "object") return; // bare JSON scalar — nothing to apply
    const hso = parsed.hookSpecificOutput;
    if (typeof parsed.systemMessage === "string" && parsed.systemMessage)
        outcome.messages.push(clip(parsed.systemMessage, 1000));
    if (typeof parsed.terminalSequence === "string" && parsed.terminalSequence)
        outcome.terminalSequences.push(parsed.terminalSequence);
    if (typeof hso?.additionalContext === "string" && hso.additionalContext) contexts.push(hso.additionalContext);
    if (hso?.updatedInput !== undefined && outcome.updatedInput === undefined) outcome.updatedInput = hso.updatedInput;
    // `continue: false` halts everything in Claude Code — treat as block.
    if (parsed.continue === false) {
        block(parsed.stopReason ?? parsed.reason ?? `stopped by ${event} hook`);
        return;
    }
    // "ask" needs a permission prompt we don't have — deny is the safe
    // fallback (silently allowing would grant what the hook wanted gated).
    if (hso?.permissionDecision === "ask") {
        block(
            `${hso.permissionDecisionReason ?? `${event} hook requested confirmation`} (ask unsupported in pi — denied)`,
        );
        return;
    }
    if (parsed.decision === "block" || hso?.permissionDecision === "deny") {
        block(parsed.reason ?? hso?.permissionDecisionReason ?? `blocked by ${event} hook`);
    }
}

/**
 * Run all configured hooks for an event in parallel (Claude Code behavior)
 * and merge results in config order: all output is collected, the first
 * configured block wins. `async: true` hooks fire without being awaited and
 * never contribute to the outcome. Never throws — a misbehaving hook or
 * config degrades to a warning message, not a broken agent.
 */
export async function runHooks(
    event: HookEvent,
    matcherValue: string | undefined,
    payload: Omit<HookPayload, "hook_event_name">,
    cwd: string,
): Promise<HookOutcome> {
    const outcome: HookOutcome = { block: false, messages: [], terminalSequences: [] };
    try {
        const groups = loadHooksConfig(cwd)[event];
        if (!groups?.length) return outcome;

        const fullPayload: HookPayload = { ...payload, cwd, hook_event_name: event };

        const sync: HookCommand[] = [];
        for (const group of groups) {
            if (!matcherTest(group.matcher, matcherValue)) continue;
            for (const cmd of group.hooks ?? []) {
                if (!cmd || typeof cmd.command !== "string" || !cmd.command.trim()) continue;
                if (cmd.type && cmd.type !== "command") continue; // v1: command hooks only
                if (cmd.async) void runCommand(cmd, fullPayload, cwd);
                else sync.push(cmd);
            }
        }
        if (sync.length === 0) return outcome;

        const results = await Promise.all(sync.map((cmd) => runCommand(cmd, fullPayload, cwd)));

        const contexts: string[] = [];
        for (let i = 0; i < sync.length; i++) {
            applyResult(event, sync[i], results[i], outcome, contexts);
        }
        if (contexts.length) outcome.additionalContext = contexts.join("\n");
        return outcome;
    } catch (err) {
        outcome.messages.push(`hook system error (${event}): ${err instanceof Error ? err.message : String(err)}`);
        return outcome;
    }
}
