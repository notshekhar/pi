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

export interface BashToolContext {
    cwd: string;
    abortSignal?: AbortSignal;
    /** Optional explicit shell path (settings.json `shellPath`) */
    shellPath?: string;
    /** Optional command prefix prepended to every command (e.g. shell setup) */
    commandPrefix?: string;
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
    },
): Promise<{ exitCode: number | null }> {
    return new Promise((resolve, reject) => {
        const { shell, args } = getShellConfig(opts.shellPath);
        if (!existsSync(cwd)) {
            reject(new Error(`Working directory does not exist: ${cwd}\nCannot execute bash commands.`));
            return;
        }
        const child = spawn(shell, [...args, command], {
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

export function createBashTool(ctx: BashToolContext) {
    return tool({
        description:
            "Execute a bash command. Returns merged stdout/stderr. Output truncated to 50KB / 2000 lines (tail kept). Process tree killed on abort/timeout. Timeout is in seconds (optional, no default).",
        inputSchema: z.object({
            command: z.string().describe("Bash command to execute"),
            timeout: z.number().positive().optional().describe("Timeout in seconds (optional, no default timeout)"),
        }),
        execute: async ({ command, timeout }, options) => {
            const signal = options?.abortSignal ?? ctx.abortSignal;
            const output = new OutputAccumulator({ tempFilePrefix: "pi-bash" });
            const finalCommand = ctx.commandPrefix ? `${ctx.commandPrefix}\n${command}` : command;

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
                });
                const { text } = await formatOutput();
                if (exitCode !== 0 && exitCode !== null) {
                    throw new Error(appendStatus(text, `Command exited with code ${exitCode}`));
                }
                return text;
            } catch (err) {
                if (err instanceof Error && err.message === "aborted") {
                    const { text } = await formatOutput("");
                    throw new Error(appendStatus(text, "Command aborted"));
                }
                if (err instanceof Error && err.message.startsWith("timeout:")) {
                    const secs = err.message.split(":")[1];
                    const { text } = await formatOutput("");
                    throw new Error(appendStatus(text, `Command timed out after ${secs} seconds`));
                }
                throw err;
            }
        },
    });
}
