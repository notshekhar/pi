import { existsSync } from "node:fs";
import { spawn } from "node:child_process";
import { tool } from "ai";
import { z } from "zod";
import { waitForChildProcess } from "./utils/child-process";
import {
    getShellConfig,
    getShellEnv,
    killProcessTree,
    trackDetachedChildPid,
    untrackDetachedChildPid,
} from "./utils/shell";
import { OutputAccumulator } from "./utils/output-accumulator";
import { DEFAULT_MAX_BYTES, formatSize } from "./utils/truncate";
import { DEFAULT_BASH_DENY, findDeniedCommand, formatDenyRefusal } from "./utils/command-deny";
import { getSetting } from "../settings";
import { sandbox, type SandboxConfig } from "@notshekhar/loop-sandbox";

export interface BashToolContext {
    cwd: string;
    abortSignal?: AbortSignal;
    /** Optional explicit shell path (settings.json `shellPath`) */
    shellPath?: string;
    /** Optional command prefix prepended to every command (e.g. shell setup) */
    commandPrefix?: string;
    /**
     * Force a fail-closed, kernel-enforced read-only sandbox (no writable cwd),
     * regardless of user sandbox settings. Set for read-only agents (plan): if
     * the sandbox can't be enforced, the command is REFUSED rather than run.
     */
    readOnlyFs?: boolean;
}

function execBash(
    command: string,
    cwd: string,
    opts: {
        onData: (d: Buffer) => void;
        signal?: AbortSignal;
        timeout?: number;
        env?: NodeJS.ProcessEnv;
        shellPath?: string;
        /** Pre-built spawn argv (sandbox wrapper). When set, `command` is ignored. */
        argv?: string[];
    },
): Promise<{ exitCode: number | null }> {
    return new Promise((resolve, reject) => {
        if (!existsSync(cwd)) {
            reject(new Error(`Working directory does not exist: ${cwd}\nCannot execute bash commands.`));
            return;
        }
        // Sandbox path: spawn the wrapper argv directly (no host shell layer).
        // Otherwise spawn the command through the configured shell as before.
        let file: string;
        let spawnArgs: string[];
        if (opts.argv && opts.argv.length > 0) {
            file = opts.argv[0];
            spawnArgs = opts.argv.slice(1);
        } else {
            const { shell, args } = getShellConfig(opts.shellPath);
            file = shell;
            spawnArgs = [...args, command];
        }
        const child = spawn(file, spawnArgs, {
            cwd,
            detached: process.platform !== "win32",
            env: opts.env ?? getShellEnv(),
            stdio: ["ignore", "pipe", "pipe"],
        });
        if (child.pid) trackDetachedChildPid(child.pid);
        let timedOut = false;
        let timeoutHandle: NodeJS.Timeout | undefined;
        if (opts.timeout !== undefined && opts.timeout > 0) {
            timeoutHandle = setTimeout(() => {
                timedOut = true;
                if (child.pid) killProcessTree(child.pid);
            }, opts.timeout * 1000);
        }
        child.stdout?.on("data", opts.onData);
        child.stderr?.on("data", opts.onData);
        const onAbort = () => {
            if (child.pid) killProcessTree(child.pid);
        };
        if (opts.signal) {
            if (opts.signal.aborted) onAbort();
            else opts.signal.addEventListener("abort", onAbort, { once: true });
        }
        waitForChildProcess(child)
            .then((code) => {
                if (child.pid) untrackDetachedChildPid(child.pid);
                if (timeoutHandle) clearTimeout(timeoutHandle);
                if (opts.signal) opts.signal.removeEventListener("abort", onAbort);
                if (opts.signal?.aborted) {
                    reject(new Error("aborted"));
                    return;
                }
                if (timedOut) {
                    reject(new Error(`timeout:${opts.timeout}`));
                    return;
                }
                resolve({ exitCode: code });
            })
            .catch((err) => {
                if (child.pid) untrackDetachedChildPid(child.pid);
                if (timeoutHandle) clearTimeout(timeoutHandle);
                if (opts.signal) opts.signal.removeEventListener("abort", onAbort);
                reject(err);
            });
    });
}

/**
 * Decide how to run a command under the sandbox. Returns a spawn argv when the
 * sandbox is active, or a warning when it's enabled but can't be enforced.
 *
 * Fail-open by design (the user's configured behavior): if the boundary can't
 * be applied we still run the command, but surface a warning so it's never a
 * silent downgrade. `command` is the final command (after commandPrefix).
 */
async function resolveSandbox(command: string, ctx: BashToolContext): Promise<{ argv?: string[]; warning?: string }> {
    // Read-only agents (e.g. plan): bash MUST be kernel-enforced read-only, and
    // fail CLOSED — if the sandbox can't be applied, refuse rather than run
    // unsandboxed. (Plan only gets bash on sandbox-capable platforms, so the
    // unsupported branch is defensive.)
    if (ctx.readOnlyFs) {
        if (!sandbox.isSupported()) {
            throw new Error(
                "This agent runs bash in a read-only OS sandbox, which isn't available on this platform — bash is disabled here. Investigate with read/ls/grep/find instead.",
            );
        }
        const deps = sandbox.checkDependencies();
        if (deps.errors.length > 0) {
            throw new Error(
                `This agent runs bash in a read-only OS sandbox, but it's unavailable (${deps.errors.join(", ")}). Refusing to run bash unsandboxed. Use read/ls/grep/find, or install the missing dependency.`,
            );
        }
        const { shell } = getShellConfig(ctx.shellPath);
        const config: SandboxConfig = {
            filesystem: { allowWrite: [], denyWrite: [], denyRead: [], allowRead: [], readOnly: true },
            network: "deny",
        };
        try {
            const wrapped = await sandbox.wrap({ command, shell, cwd: ctx.cwd, config });
            if (!wrapped) throw new Error("sandbox could not wrap the command");
            return { argv: wrapped.argv };
        } catch (err) {
            const msg = err instanceof Error ? err.message : String(err);
            throw new Error(`Read-only sandbox failed to start (${msg}). Refusing to run bash unsandboxed.`);
        }
    }

    const s = getSetting("sandbox");
    if (!s?.enabled) return {};

    if (!sandbox.isSupported()) {
        return { warning: "sandbox is enabled but not supported on this platform — ran WITHOUT isolation." };
    }
    const deps = sandbox.checkDependencies();
    if (deps.errors.length > 0) {
        return { warning: `sandbox is enabled but unavailable (${deps.errors.join(", ")}) — ran WITHOUT isolation.` };
    }

    const { shell } = getShellConfig(ctx.shellPath);
    const config: SandboxConfig = {
        filesystem: {
            allowWrite: s.allowWrite ?? [],
            denyWrite: s.denyWrite ?? [],
            denyRead: s.denyRead ?? [],
            allowRead: s.allowRead ?? [],
            allowGitConfig: s.allowGitConfig,
        },
        network: s.network ?? "deny",
    };
    try {
        const wrapped = await sandbox.wrap({ command, shell, cwd: ctx.cwd, config });
        if (!wrapped) return { warning: "sandbox could not wrap the command — ran WITHOUT isolation." };
        return { argv: wrapped.argv };
    } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        return { warning: `sandbox failed to start (${msg}) — ran WITHOUT isolation.` };
    }
}

export function createBashTool(ctx: BashToolContext) {
    return tool({
        description:
            "Execute a bash command. Returns merged stdout/stderr. Output truncated to 50KB / 2000 lines (tail kept). Process tree killed on abort/timeout. Timeout is in seconds (optional, no default).",
        inputSchema: z.object({
            command: z.string().describe("Bash command to execute"),
            timeout: z.number().positive().optional().describe("Timeout in seconds (optional, no default timeout)"),
        }),
        execute: async ({ command, timeout }, options) => {
            // Denylist guardrail: refuse blocked commands before anything runs.
            // Read live so settings edits apply without a restart; an unset
            // bashDeny falls back to the seeded defaults. Checked against the
            // raw command (before commandPrefix) so the user's text is judged.
            const denied = findDeniedCommand(command, getSetting("bashDeny") ?? DEFAULT_BASH_DENY);
            if (denied) throw new Error(formatDenyRefusal(denied));

            const signal = options?.abortSignal ?? ctx.abortSignal;
            const output = new OutputAccumulator({ tempFilePrefix: "loop-bash" });
            const finalCommand = ctx.commandPrefix ? `${ctx.commandPrefix}\n${command}` : command;

            // Resolve the sandbox against the final command (commandPrefix included).
            const sandboxRun = await resolveSandbox(finalCommand, ctx);
            const withSandboxWarning = (text: string): string =>
                sandboxRun.warning ? appendStatus(`[loop sandbox] ${sandboxRun.warning}`, text) : text;

            const formatOutput = async (emptyText = "(no output)"): Promise<{ text: string; truncated: boolean }> => {
                output.finish();
                const snap = output.snapshot({ persistIfTruncated: true });
                await output.closeTempFile();
                let text = snap.content || emptyText;
                const t = snap.truncation;
                if (t.truncated) {
                    const startLine = t.totalLines - t.outputLines + 1;
                    const endLine = t.totalLines;
                    if (t.lastLinePartial) {
                        text += `\n\n[Showing last ${formatSize(t.outputBytes)} of line ${endLine} (line is ${formatSize(output.getLastLineBytes())}). Full output: ${snap.fullOutputPath}]`;
                    } else if (t.truncatedBy === "lines") {
                        text += `\n\n[Showing lines ${startLine}-${endLine} of ${t.totalLines}. Full output: ${snap.fullOutputPath}]`;
                    } else {
                        text += `\n\n[Showing lines ${startLine}-${endLine} of ${t.totalLines} (${formatSize(DEFAULT_MAX_BYTES)} limit). Full output: ${snap.fullOutputPath}]`;
                    }
                }
                return { text, truncated: t.truncated };
            };

            const appendStatus = (text: string, status: string) => `${text ? `${text}\n\n` : ""}${status}`;

            try {
                const { exitCode } = await execBash(finalCommand, ctx.cwd, {
                    onData: (d) => output.append(d),
                    signal,
                    timeout,
                    shellPath: ctx.shellPath,
                    argv: sandboxRun.argv,
                });
                const { text } = await formatOutput();
                if (exitCode !== 0 && exitCode !== null) {
                    throw new Error(withSandboxWarning(appendStatus(text, `Command exited with code ${exitCode}`)));
                }
                return withSandboxWarning(text);
            } catch (err) {
                if (err instanceof Error && err.message === "aborted") {
                    const { text } = await formatOutput("");
                    throw new Error(withSandboxWarning(appendStatus(text, "Command aborted")));
                }
                if (err instanceof Error && err.message.startsWith("timeout:")) {
                    const secs = err.message.split(":")[1];
                    const { text } = await formatOutput("");
                    throw new Error(withSandboxWarning(appendStatus(text, `Command timed out after ${secs} seconds`)));
                }
                throw err;
            }
        },
    });
}
