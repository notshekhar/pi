/**
 * Linux bubblewrap (bwrap) command construction.
 *
 * Inspired by / adapted from anthropic-experimental/sandbox-runtime (Apache-2.0):
 * https://github.com/anthropic-experimental/sandbox-runtime — see NOTICE.md.
 *
 * Filesystem isolation: read-only root + writable allow-list binds, deny-write
 * masks via /dev/null binds, dev/proc, PID + network namespaces.
 *
 * Network domain allowlist: `--unshare-net` removes loopback, so host-side
 * `socat` bridges expose the JS proxies as Unix sockets, which are bound into
 * the namespace; an inner `socat` re-exposes them as localhost:3128/1080 and
 * the proxy env points there. This mirrors upstream's bridge design.
 *
 * !!! UNVERIFIED: the bwrap/socat paths cannot be compiled or run on macOS.
 *     They are written to match upstream's mechanism but must be tested on a
 *     real Linux box before being relied upon. The seccomp BPF layer
 *     (apply-seccomp) is NOT ported here — see seccomp-stub.ts.
 */
import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { randomBytes } from "node:crypto";
import * as fs from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import shellquote from "shell-quote";
import { logForDebugging } from "./debug";
import { normalizePathForSandbox, generateProxyEnvVars } from "./sandbox-utils";
import { getApplySeccompPrefix } from "./seccomp";
import type { FsReadRestrictionConfig, FsWriteRestrictionConfig } from "./sandbox-schemas";

/** Fixed ports the inner socat listeners bind inside the (isolated) netns. */
const INNER_HTTP_PORT = 3128;
const INNER_SOCKS_PORT = 1080;

export interface SandboxDependencyCheck {
    errors: string[];
    warnings: string[];
}

export interface LinuxNetworkBridge {
    httpSocketPath: string;
    socksSocketPath: string;
    httpBridge: ChildProcess;
    socksBridge: ChildProcess;
}

export interface LinuxSandboxParams {
    command: string;
    needsNetworkRestriction: boolean;
    readConfig: FsReadRestrictionConfig | undefined;
    writeConfig: FsWriteRestrictionConfig | undefined;
    allowGitConfig?: boolean;
    /** Absolute path to the shell binary to run the command under. */
    shell: string;
    /** Absolute path to bwrap; resolved via PATH when omitted. */
    bwrapPath?: string;
    /** When set (allowlist mode), bind these host bridge sockets + route proxy env. */
    httpSocketPath?: string;
    socksSocketPath?: string;
    /** Absolute path to socat; resolved via PATH when omitted. */
    socatPath?: string;
}

/** Resolve a binary via PATH (absolute paths returned as-is). */
function whichSync(bin: string): string | null {
    if (bin.startsWith("/")) return fs.existsSync(bin) ? bin : null;
    try {
        const r = spawnSync("which", [bin], { encoding: "utf-8", timeout: 5000 });
        if (r.status === 0 && r.stdout) {
            const first = r.stdout.trim().split(/\r?\n/)[0];
            if (first) return first;
        }
    } catch {
        // fall through
    }
    return null;
}

/** Check Linux dependencies: bwrap always; socat only when an allowlist is used. */
export function checkLinuxDependencies(opts: { bwrapPath?: string; socatPath?: string; needsSocat?: boolean } = {}): SandboxDependencyCheck {
    const errors: string[] = [];
    const warnings: string[] = [];
    if (!whichSync(opts.bwrapPath ?? "bwrap")) errors.push("bubblewrap (bwrap) not found");
    if (opts.needsSocat && !whichSync(opts.socatPath ?? "socat")) {
        errors.push("socat not found (required for the network allowlist on Linux)");
    }
    return { errors, warnings };
}

/**
 * Host side of the network bridge: socat `UNIX-LISTEN:<sock> → TCP:localhost:<proxyPort>`
 * for each proxy. The Unix sockets are later bound into the bwrap namespace.
 *
 * UNVERIFIED — Linux only; cannot be exercised on macOS.
 */
export async function initializeLinuxNetworkBridge(
    httpProxyPort: number,
    socksProxyPort: number,
    socatPath?: string,
): Promise<LinuxNetworkBridge> {
    const socat = socatPath ?? "socat";
    const id = randomBytes(8).toString("hex");
    const httpSocketPath = join(tmpdir(), `pi-http-${id}.sock`);
    const socksSocketPath = join(tmpdir(), `pi-socks-${id}.sock`);

    const startBridge = (sockPath: string, port: number, label: string): ChildProcess => {
        const proc = spawn(socat, [`UNIX-LISTEN:${sockPath},fork,reuseaddr`, `TCP:localhost:${port},keepalive`], {
            stdio: "ignore",
        });
        proc.on("error", (err) => logForDebugging(`${label} bridge error: ${err}`, { level: "error" }));
        proc.on("exit", (code) => logForDebugging(`${label} bridge exited (code ${code})`));
        if (!proc.pid) throw new Error(`Failed to start ${label} bridge`);
        return proc;
    };

    const httpBridge = startBridge(httpSocketPath, httpProxyPort, "HTTP");
    let socksBridge: ChildProcess;
    try {
        socksBridge = startBridge(socksSocketPath, socksProxyPort, "SOCKS");
    } catch (err) {
        if (httpBridge.pid) try { process.kill(httpBridge.pid, "SIGTERM"); } catch {}
        throw err;
    }

    // Wait for both sockets to appear.
    for (let i = 0; i < 5; i++) {
        if (fs.existsSync(httpSocketPath) && fs.existsSync(socksSocketPath)) break;
        if (i === 4) {
            for (const p of [httpBridge, socksBridge]) if (p.pid) try { process.kill(p.pid, "SIGTERM"); } catch {}
            throw new Error("bridge sockets did not appear");
        }
        await new Promise((r) => setTimeout(r, (i + 1) * 100));
    }

    return { httpSocketPath, socksSocketPath, httpBridge, socksBridge };
}

/** Run the user command under seccomp when available, else plainly. */
function userCommandInvocation(shell: string, command: string, seccompPrefix?: string): string {
    return seccompPrefix
        ? `${seccompPrefix}${shellquote.quote([shell, "-c", command])}`
        : `eval ${shellquote.quote([command])}`;
}

/** Build the inner shell script: start the in-netns socat listeners, then run the command. */
function buildInnerNetworkScript(params: LinuxSandboxParams, seccompPrefix?: string): string {
    const { command, httpSocketPath, socksSocketPath, shell, socatPath } = params;
    const socat = shellquote.quote([socatPath ?? "socat"]);
    const lines = [
        `${socat} TCP-LISTEN:${INNER_HTTP_PORT},fork,reuseaddr UNIX-CONNECT:${httpSocketPath} >/dev/null 2>&1 &`,
        `${socat} TCP-LISTEN:${INNER_SOCKS_PORT},fork,reuseaddr UNIX-CONNECT:${socksSocketPath} >/dev/null 2>&1 &`,
        `trap "kill %1 %2 2>/dev/null; exit" EXIT`,
        // seccomp (if built) runs after socat so socat can still create sockets.
        userCommandInvocation(shell, command, seccompPrefix),
    ];
    return `${shell} -c ${shellquote.quote([lines.join("\n")])}`;
}

/** Build the full bwrap argv. */
function buildBwrapArgs(params: LinuxSandboxParams): string[] {
    const { needsNetworkRestriction, writeConfig, readConfig, httpSocketPath, socksSocketPath } = params;
    const useProxy = Boolean(httpSocketPath && socksSocketPath);
    const args: string[] = ["--new-session", "--die-with-parent"];

    if (needsNetworkRestriction) args.push("--unshare-net");

    if (writeConfig) {
        args.push("--ro-bind", "/", "/");
        for (const rawPath of writeConfig.allowOnly) {
            const p = normalizePathForSandbox(rawPath);
            if (!p.startsWith("/dev/") && fs.existsSync(p)) args.push("--bind", p, p);
        }
        for (const rawPath of writeConfig.denyWithinAllow) {
            const p = normalizePathForSandbox(rawPath);
            if (!p.startsWith("/dev/") && fs.existsSync(p)) args.push("--ro-bind", p, p);
        }
    } else {
        args.push("--bind", "/", "/");
    }

    if (readConfig) {
        for (const rawPath of readConfig.denyOnly) {
            const p = normalizePathForSandbox(rawPath);
            if (!fs.existsSync(p)) continue;
            let isDir = false;
            try {
                isDir = fs.statSync(p).isDirectory();
            } catch {}
            if (isDir) args.push("--tmpfs", p);
            else args.push("--ro-bind", "/dev/null", p);
        }
        for (const rawPath of readConfig.allowWithinDeny ?? []) {
            const p = normalizePathForSandbox(rawPath);
            if (fs.existsSync(p)) args.push("--ro-bind", p, p);
        }
    }

    // Network allowlist: bind the host bridge sockets in + set proxy env to the
    // inner socat ports. generateProxyEnvVars yields "KEY=VALUE" strings.
    if (useProxy) {
        args.push("--bind", httpSocketPath!, httpSocketPath!);
        args.push("--bind", socksSocketPath!, socksSocketPath!);
        for (const kv of generateProxyEnvVars(INNER_HTTP_PORT, INNER_SOCKS_PORT)) {
            const eq = kv.indexOf("=");
            if (eq > 0) args.push("--setenv", kv.slice(0, eq), kv.slice(eq + 1));
        }
    }

    args.push("--dev", "/dev");
    args.push("--unshare-pid", "--proc", "/proc");

    return args;
}

/**
 * Wrap a command with the Linux sandbox. Returns a single shell-quoted command
 * string (run via `<shell> -c <result>`), or the original command when no
 * restrictions apply. UNVERIFIED on macOS.
 */
export function wrapCommandWithSandboxLinux(params: LinuxSandboxParams): string {
    const { command, needsNetworkRestriction, readConfig, writeConfig, shell, bwrapPath, httpSocketPath, socksSocketPath } = params;

    const hasReadRestrictions = readConfig && readConfig.denyOnly.length > 0;
    const hasWriteRestrictions = writeConfig !== undefined;

    if (!needsNetworkRestriction && !hasReadRestrictions && !hasWriteRestrictions) return command;

    const bwrap = whichSync(bwrapPath ?? "bwrap");
    if (!bwrap) throw new Error("bubblewrap (bwrap) not found");

    const bwrapArgs = buildBwrapArgs(params);
    // With an allowlist, the inner command is a socat-wrapped script; otherwise
    // it's the plain command under the shell. Seccomp (when built) wraps the
    // user-command invocation in both cases.
    const useProxy = Boolean(httpSocketPath && socksSocketPath);
    const seccompPrefix = getApplySeccompPrefix();
    const inner = useProxy
        ? buildInnerNetworkScript(params, seccompPrefix)
        : `${shell} -c ${shellquote.quote([userCommandInvocation(shell, command, seccompPrefix)])}`;
    const wrapped = shellquote.quote([bwrap, ...bwrapArgs, "--", "/bin/sh", "-c", inner]);

    logForDebugging(
        `[Linux] restrictions — network: ${needsNetworkRestriction}, proxy: ${useProxy}, read: ${hasReadRestrictions}, write: ${hasWriteRestrictions}`,
    );

    return wrapped;
}
