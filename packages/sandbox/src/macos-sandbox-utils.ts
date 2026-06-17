/**
 * macOS Seatbelt profile generation + `sandbox-exec` command wrapping.
 *
 * Ported from anthropic-experimental/sandbox-runtime (Apache-2.0):
 * https://github.com/anthropic-experimental/sandbox-runtime — see NOTICE.md.
 *
 * Divergence: the upstream log-stream violation monitor
 * (startMacOSSandboxLogMonitor) is not ported yet (Stage 3). The caller passes
 * an already-resolved absolute shell path instead of a shell name resolved via
 * whichSync.
 */
import shellquote from "shell-quote";
import * as path from "node:path";
import { logForDebugging } from "./debug";
import {
    normalizePathForSandbox,
    generateProxyEnvVars,
    encodeSandboxedCommand,
    containsGlobChars,
    globToRegex,
    DANGEROUS_FILES,
    getDangerousDirectories,
} from "./sandbox-utils";
import type { FsReadRestrictionConfig, FsWriteRestrictionConfig } from "./sandbox-schemas";

export interface MacOSSandboxParams {
    command: string;
    needsNetworkRestriction: boolean;
    httpProxyPort?: number;
    socksProxyPort?: number;
    proxyAuthToken?: string;
    caCertPath?: string;
    allowUnixSockets?: string[];
    allowAllUnixSockets?: boolean;
    allowLocalBinding?: boolean;
    allowMachLookup?: string[];
    readConfig: FsReadRestrictionConfig | undefined;
    writeConfig: FsWriteRestrictionConfig | undefined;
    allowPty?: boolean;
    allowGitConfig?: boolean;
    enableWeakerNetworkIsolation?: boolean;
    allowAppleEvents?: boolean;
    /** Absolute path to the shell binary to run the command under. */
    shell: string;
}

/**
 * Mandatory deny patterns (glob form — macOS profiles match these via regex).
 */
export function macGetMandatoryDenyPatterns(allowGitConfig = false): string[] {
    const cwd = process.cwd();
    const denyPaths: string[] = [];

    for (const fileName of DANGEROUS_FILES) {
        denyPaths.push(path.resolve(cwd, fileName));
        denyPaths.push(`**/${fileName}`);
    }

    for (const dirName of getDangerousDirectories()) {
        denyPaths.push(path.resolve(cwd, dirName));
        denyPaths.push(`**/${dirName}/**`);
    }

    // Git hooks are always blocked.
    denyPaths.push(path.resolve(cwd, ".git/hooks"));
    denyPaths.push("**/.git/hooks/**");

    if (!allowGitConfig) {
        denyPaths.push(path.resolve(cwd, ".git/config"));
        denyPaths.push("**/.git/config");
    }

    return [...new Set(denyPaths)];
}

const sessionSuffix = `_${Math.random().toString(36).slice(2, 11)}_SBX`;

function generateLogTag(command: string): string {
    return `CMD64_${encodeSandboxedCommand(command)}_END_${sessionSuffix}`;
}

/** Escape a path for the sandbox profile via JSON.stringify. */
function escapePath(pathStr: string): string {
    return JSON.stringify(pathStr);
}

/** All ancestor directories of a path, up to (not including) root. */
function getAncestorDirectories(pathStr: string): string[] {
    const ancestors: string[] = [];
    let currentPath = path.dirname(pathStr);
    while (currentPath !== "/" && currentPath !== ".") {
        ancestors.push(currentPath);
        const parentPath = path.dirname(currentPath);
        if (parentPath === currentPath) break;
        currentPath = parentPath;
    }
    return ancestors;
}

/**
 * Deny file-write-unlink / file-write-create on protected paths and their
 * ancestors, blocking bypass-by-move and symlink-replacement of not-yet-created
 * protected paths.
 */
function generateMoveBlockingRules(pathPatterns: string[], logTag: string): string[] {
    const rules: string[] = [];
    const ops = ["file-write-unlink", "file-write-create"] as const;

    for (const pathPattern of pathPatterns) {
        const normalizedPath = normalizePathForSandbox(pathPattern);

        if (containsGlobChars(normalizedPath)) {
            const regexPattern = globToRegex(normalizedPath);
            for (const op of ops) {
                rules.push(`(deny ${op}`, `  (regex ${escapePath(regexPattern)})`, `  (with message "${logTag}"))`);
            }

            const staticPrefix = normalizedPath.split(/[*?[\]]/)[0];
            if (staticPrefix && staticPrefix !== "/") {
                const baseDir = staticPrefix.endsWith("/") ? staticPrefix.slice(0, -1) : path.dirname(staticPrefix);
                for (const op of ops) {
                    rules.push(
                        `(deny ${op}`,
                        `  (literal ${escapePath(baseDir)})`,
                        `  (with message "${logTag}"))`,
                    );
                }
                for (const ancestorDir of getAncestorDirectories(baseDir)) {
                    for (const op of ops) {
                        rules.push(
                            `(deny ${op}`,
                            `  (literal ${escapePath(ancestorDir)})`,
                            `  (with message "${logTag}"))`,
                        );
                    }
                }
            }
        } else {
            for (const op of ops) {
                rules.push(
                    `(deny ${op}`,
                    `  (subpath ${escapePath(normalizedPath)})`,
                    `  (with message "${logTag}"))`,
                );
            }
            for (const ancestorDir of getAncestorDirectories(normalizedPath)) {
                for (const op of ops) {
                    rules.push(
                        `(deny ${op}`,
                        `  (literal ${escapePath(ancestorDir)})`,
                        `  (with message "${logTag}"))`,
                    );
                }
            }
        }
    }

    return rules;
}

/**
 * Filesystem read rules. Seatbelt is last-match-wins, so:
 *   (allow file-read*) → (deny … broad) → (allow … re-allow within deny).
 */
function generateReadRules(
    config: FsReadRestrictionConfig | undefined,
    logTag: string,
    writeAllowPaths?: string[],
): string[] {
    if (!config) return [`(allow file-read*)`];

    const rules: string[] = [];
    let deniesRoot = false;

    rules.push(`(allow file-read*)`);

    for (const pathPattern of config.denyOnly || []) {
        const normalizedPath = normalizePathForSandbox(pathPattern);
        if (normalizedPath === "/") deniesRoot = true;

        if (containsGlobChars(normalizedPath)) {
            const regexPattern = globToRegex(normalizedPath);
            rules.push(`(deny file-read*`, `  (regex ${escapePath(regexPattern)})`, `  (with message "${logTag}"))`);
        } else {
            rules.push(
                `(deny file-read*`,
                `  (subpath ${escapePath(normalizedPath)})`,
                `  (with message "${logTag}"))`,
            );
        }
    }

    // Re-allow the literal root so path traversal / exec works even when "/"
    // is denied (exposes `ls /` dirent names but no subtree contents).
    if (deniesRoot) rules.push(`(allow file-read* (literal "/"))`);

    const allowedSubpaths: string[] = [];
    for (const pathPattern of config.allowWithinDeny || []) {
        const normalizedPath = normalizePathForSandbox(pathPattern);
        if (containsGlobChars(normalizedPath)) {
            const regexPattern = globToRegex(normalizedPath);
            rules.push(
                `(allow file-read*`,
                `  (regex ${escapePath(regexPattern)})`,
                `  (with message "${logTag}"))`,
            );
        } else {
            allowedSubpaths.push(normalizedPath);
            rules.push(
                `(allow file-read*`,
                `  (subpath ${escapePath(normalizedPath)})`,
                `  (with message "${logTag}"))`,
            );
        }
    }

    // A literal deny nested inside a literal re-allow must land last.
    for (const denyPath of config.denyOnly || []) {
        if (containsGlobChars(denyPath)) continue;
        const normalized = normalizePathForSandbox(denyPath);
        if (allowedSubpaths.some((a) => normalized.startsWith(a + "/"))) {
            rules.push(
                `(deny file-read*`,
                `  (subpath ${escapePath(normalized)})`,
                `  (with message "${logTag}"))`,
            );
        }
    }

    // Allow stat/lstat on directories so realpath() can traverse denied regions.
    if (config.denyOnly.length > 0) {
        rules.push(`(allow file-read-metadata`, `  (vnode-type DIRECTORY))`);
    }

    rules.push(...generateMoveBlockingRules(config.denyOnly || [], logTag));

    // Re-allow unlink/create within write-allowed paths (the broad move-blocking
    // denies above would otherwise block deletions in writable dirs).
    if (writeAllowPaths && writeAllowPaths.length > 0) {
        for (const pathPattern of writeAllowPaths) {
            const normalizedPath = normalizePathForSandbox(pathPattern);
            for (const op of ["file-write-unlink", "file-write-create"] as const) {
                if (containsGlobChars(normalizedPath)) {
                    const regexPattern = globToRegex(normalizedPath);
                    rules.push(
                        `(allow ${op}`,
                        `  (regex ${escapePath(regexPattern)})`,
                        `  (with message "${logTag}"))`,
                    );
                } else {
                    rules.push(
                        `(allow ${op}`,
                        `  (subpath ${escapePath(normalizedPath)})`,
                        `  (with message "${logTag}"))`,
                    );
                }
            }
        }
    }

    return rules;
}

/** Filesystem write rules (allow-only + deny-within + mandatory denies). */
function generateWriteRules(
    config: FsWriteRestrictionConfig | undefined,
    logTag: string,
    allowGitConfig = false,
): string[] {
    if (!config) return [`(allow file-write*)`];

    const rules: string[] = [];

    for (const pathPattern of config.allowOnly || []) {
        const normalizedPath = normalizePathForSandbox(pathPattern);
        if (containsGlobChars(normalizedPath)) {
            const regexPattern = globToRegex(normalizedPath);
            rules.push(`(allow file-write*`, `  (regex ${escapePath(regexPattern)})`, `  (with message "${logTag}"))`);
        } else {
            rules.push(
                `(allow file-write*`,
                `  (subpath ${escapePath(normalizedPath)})`,
                `  (with message "${logTag}"))`,
            );
        }
    }

    const denyPaths = [...(config.denyWithinAllow || []), ...macGetMandatoryDenyPatterns(allowGitConfig)];

    for (const pathPattern of denyPaths) {
        const normalizedPath = normalizePathForSandbox(pathPattern);
        if (containsGlobChars(normalizedPath)) {
            const regexPattern = globToRegex(normalizedPath);
            rules.push(`(deny file-write*`, `  (regex ${escapePath(regexPattern)})`, `  (with message "${logTag}"))`);
        } else {
            rules.push(
                `(deny file-write*`,
                `  (subpath ${escapePath(normalizedPath)})`,
                `  (with message "${logTag}"))`,
            );
        }
    }

    rules.push(...generateMoveBlockingRules(denyPaths, logTag));

    return rules;
}

interface ProfileParams {
    readConfig: FsReadRestrictionConfig | undefined;
    writeConfig: FsWriteRestrictionConfig | undefined;
    httpProxyPort?: number;
    socksProxyPort?: number;
    needsNetworkRestriction: boolean;
    allowUnixSockets?: string[];
    allowAllUnixSockets?: boolean;
    allowLocalBinding?: boolean;
    allowMachLookup?: string[];
    allowPty?: boolean;
    allowGitConfig?: boolean;
    enableWeakerNetworkIsolation?: boolean;
    allowAppleEvents?: boolean;
    logTag: string;
}

/** Generate the complete Seatbelt profile (S-expressions). */
function generateSandboxProfile({
    readConfig,
    writeConfig,
    httpProxyPort,
    socksProxyPort,
    needsNetworkRestriction,
    allowUnixSockets,
    allowAllUnixSockets,
    allowLocalBinding,
    allowMachLookup,
    allowPty,
    allowGitConfig = false,
    enableWeakerNetworkIsolation = false,
    allowAppleEvents = false,
    logTag,
}: ProfileParams): string {
    const profile: string[] = [
        "(version 1)",
        `(deny default (with message "${logTag}"))`,
        "",
        `; LogTag: ${logTag}`,
        "",
        "; Process permissions",
        "(allow process-exec)",
        "(allow process-fork)",
        "(allow process-info* (target same-sandbox))",
        "(allow signal (target same-sandbox))",
        "(allow mach-priv-task-port (target same-sandbox))",
        "",
        "(allow user-preference-read)",
        "",
        "; Mach IPC - specific services only (no wildcard)",
        "(allow mach-lookup",
        '  (global-name "com.apple.audio.systemsoundserver")',
        '  (global-name "com.apple.distributed_notifications@Uv3")',
        '  (global-name "com.apple.FontObjectsServer")',
        '  (global-name "com.apple.fonts")',
        '  (global-name "com.apple.logd")',
        '  (global-name "com.apple.lsd.mapdb")',
        '  (global-name "com.apple.PowerManagement.control")',
        '  (global-name "com.apple.system.logger")',
        '  (global-name "com.apple.system.notification_center")',
        '  (global-name "com.apple.system.opendirectoryd.libinfo")',
        '  (global-name "com.apple.system.opendirectoryd.membership")',
        '  (global-name "com.apple.bsd.dirhelper")',
        '  (global-name "com.apple.securityd.xpc")',
        '  (global-name "com.apple.coreservices.launchservicesd")',
        ")",
        "",
        ...(enableWeakerNetworkIsolation
            ? [
                  "; trustd.agent - needed for Go TLS certificate verification",
                  '(allow mach-lookup (global-name "com.apple.trustd.agent"))',
              ]
            : []),
        ...(allowAppleEvents
            ? [
                  "; Apple Events - opt-in (open/osascript talk to other apps)",
                  "(allow appleevent-send)",
                  '(allow mach-lookup (global-name "com.apple.coreservices.appleevents"))',
                  "(allow lsopen)",
                  '(allow mach-lookup (global-name "com.apple.CoreServices.coreservicesd"))',
                  '(allow mach-lookup (global-name "com.apple.coreservices.quarantine-resolver"))',
              ]
            : []),
        ...(allowMachLookup && allowMachLookup.length > 0
            ? [
                  "; User-specified XPC/Mach services",
                  ...allowMachLookup.map((name) =>
                      name.endsWith("*")
                          ? `(allow mach-lookup (global-name-prefix ${escapePath(name.slice(0, -1))}))`
                          : `(allow mach-lookup (global-name ${escapePath(name)}))`,
                  ),
              ]
            : []),
        "",
        "; POSIX IPC - shared memory + semaphores (Python multiprocessing)",
        "(allow ipc-posix-shm)",
        "(allow ipc-posix-sem)",
        "",
        "; IOKit - specific operations only",
        "(allow iokit-open",
        '  (iokit-registry-entry-class "IOSurfaceRootUserClient")',
        '  (iokit-registry-entry-class "RootDomainUserClient")',
        '  (iokit-user-client-class "IOSurfaceSendRight")',
        ")",
        "(allow iokit-get-properties)",
        "",
        "; Safe system-sockets (no network access)",
        "(allow system-socket (require-all (socket-domain AF_SYSTEM) (socket-protocol 2)))",
        "",
        "; sysctl - read a curated set",
        "(allow sysctl-read",
        '  (sysctl-name "hw.activecpu")',
        '  (sysctl-name "hw.busfrequency_compat")',
        '  (sysctl-name "hw.byteorder")',
        '  (sysctl-name "hw.cacheconfig")',
        '  (sysctl-name "hw.cachelinesize_compat")',
        '  (sysctl-name "hw.cpufamily")',
        '  (sysctl-name "hw.cpufrequency")',
        '  (sysctl-name "hw.cpufrequency_compat")',
        '  (sysctl-name "hw.cputype")',
        '  (sysctl-name "hw.l1dcachesize_compat")',
        '  (sysctl-name "hw.l1icachesize_compat")',
        '  (sysctl-name "hw.l2cachesize_compat")',
        '  (sysctl-name "hw.l3cachesize_compat")',
        '  (sysctl-name "hw.logicalcpu")',
        '  (sysctl-name "hw.logicalcpu_max")',
        '  (sysctl-name "hw.machine")',
        '  (sysctl-name "hw.memsize")',
        '  (sysctl-name "hw.ncpu")',
        '  (sysctl-name "hw.nperflevels")',
        '  (sysctl-name "hw.packages")',
        '  (sysctl-name "hw.pagesize_compat")',
        '  (sysctl-name "hw.pagesize")',
        '  (sysctl-name "hw.physicalcpu")',
        '  (sysctl-name "hw.physicalcpu_max")',
        '  (sysctl-name "hw.tbfrequency_compat")',
        '  (sysctl-name "hw.vectorunit")',
        '  (sysctl-name "kern.argmax")',
        '  (sysctl-name "kern.bootargs")',
        '  (sysctl-name "kern.hostname")',
        '  (sysctl-name "kern.maxfiles")',
        '  (sysctl-name "kern.maxfilesperproc")',
        '  (sysctl-name "kern.maxproc")',
        '  (sysctl-name "kern.ngroups")',
        '  (sysctl-name "kern.osproductversion")',
        '  (sysctl-name "kern.osrelease")',
        '  (sysctl-name "kern.ostype")',
        '  (sysctl-name "kern.osvariant_status")',
        '  (sysctl-name "kern.osversion")',
        '  (sysctl-name "kern.secure_kernel")',
        '  (sysctl-name "kern.tcsm_available")',
        '  (sysctl-name "kern.tcsm_enable")',
        '  (sysctl-name "kern.usrstack64")',
        '  (sysctl-name "kern.version")',
        '  (sysctl-name "kern.willshutdown")',
        '  (sysctl-name "machdep.cpu.brand_string")',
        '  (sysctl-name "machdep.ptrauth_enabled")',
        '  (sysctl-name "security.mac.lockdown_mode_state")',
        '  (sysctl-name "sysctl.proc_cputype")',
        '  (sysctl-name "vm.loadavg")',
        '  (sysctl-name-prefix "hw.optional.arm")',
        '  (sysctl-name-prefix "hw.optional.arm.")',
        '  (sysctl-name-prefix "hw.optional.armv8_")',
        '  (sysctl-name-prefix "hw.perflevel")',
        '  (sysctl-name-prefix "kern.proc.all")',
        '  (sysctl-name-prefix "kern.proc.pgrp.")',
        '  (sysctl-name-prefix "kern.proc.pid.")',
        '  (sysctl-name-prefix "machdep.cpu.")',
        '  (sysctl-name-prefix "net.routetable.")',
        ")",
        "",
        "(allow sysctl-write",
        '  (sysctl-name "kern.tcsm_enable")',
        ")",
        "",
        "(allow distributed-notification-post)",
        "",
        '(allow mach-lookup (global-name "com.apple.SecurityServer"))',
        "",
        "; File I/O on device files",
        '(allow file-ioctl (literal "/dev/null"))',
        '(allow file-ioctl (literal "/dev/zero"))',
        '(allow file-ioctl (literal "/dev/random"))',
        '(allow file-ioctl (literal "/dev/urandom"))',
        '(allow file-ioctl (literal "/dev/dtracehelper"))',
        '(allow file-ioctl (literal "/dev/tty"))',
        "",
        "(allow file-ioctl file-read-data file-write-data",
        "  (require-all",
        '    (literal "/dev/null")',
        "    (vnode-type CHARACTER-DEVICE)",
        "  )",
        ")",
        "",
    ];

    profile.push("; Network");
    if (!needsNetworkRestriction) {
        profile.push("(allow network*)");
    } else {
        if (allowLocalBinding) {
            profile.push('(allow network-bind (local ip "*:*"))');
            profile.push('(allow network-inbound (local ip "*:*"))');
            profile.push('(allow network-outbound (local ip "*:*"))');
        }
        if (allowAllUnixSockets) {
            profile.push("(allow system-socket (socket-domain AF_UNIX))");
            profile.push('(allow network-bind (local unix-socket (path-regex #"^/")))');
            profile.push('(allow network-outbound (remote unix-socket (path-regex #"^/")))');
        } else if (allowUnixSockets && allowUnixSockets.length > 0) {
            profile.push("(allow system-socket (socket-domain AF_UNIX))");
            for (const socketPath of allowUnixSockets) {
                const normalizedPath = normalizePathForSandbox(socketPath);
                profile.push(`(allow network-bind (local unix-socket (subpath ${escapePath(normalizedPath)})))`);
                profile.push(`(allow network-outbound (remote unix-socket (subpath ${escapePath(normalizedPath)})))`);
            }
        }

        if (httpProxyPort !== undefined) {
            profile.push(`(allow network-bind (local ip "localhost:${httpProxyPort}"))`);
            profile.push(`(allow network-inbound (local ip "localhost:${httpProxyPort}"))`);
            profile.push(`(allow network-outbound (remote ip "localhost:${httpProxyPort}"))`);
        }
        if (socksProxyPort !== undefined) {
            profile.push(`(allow network-bind (local ip "localhost:${socksProxyPort}"))`);
            profile.push(`(allow network-inbound (local ip "localhost:${socksProxyPort}"))`);
            profile.push(`(allow network-outbound (remote ip "localhost:${socksProxyPort}"))`);
        }
    }
    profile.push("");

    const writeAllowPaths = writeConfig?.allowOnly;
    profile.push("; File read");
    profile.push(...generateReadRules(readConfig, logTag, writeAllowPaths));
    profile.push("");

    profile.push("; File write");
    profile.push(...generateWriteRules(writeConfig, logTag, allowGitConfig));

    if (allowPty) {
        profile.push("");
        profile.push("; Pseudo-terminal (pty) support");
        profile.push("(allow pseudo-tty)");
        profile.push("(allow file-ioctl");
        profile.push('  (literal "/dev/ptmx")');
        profile.push('  (regex #"^/dev/ttys")');
        profile.push(")");
        profile.push("(allow file-read* file-write*");
        profile.push('  (literal "/dev/ptmx")');
        profile.push('  (regex #"^/dev/ttys")');
        profile.push(")");
    }

    return profile.join("\n");
}

/**
 * Wrap a command with the macOS sandbox. Returns a single shell-quoted command
 * string (run it via `<shell> -c <result>`), or the original command when no
 * restrictions apply.
 */
export function wrapCommandWithSandboxMacOS(params: MacOSSandboxParams): string {
    const {
        command,
        needsNetworkRestriction,
        httpProxyPort,
        socksProxyPort,
        proxyAuthToken,
        caCertPath,
        allowUnixSockets,
        allowAllUnixSockets,
        allowLocalBinding,
        allowMachLookup,
        readConfig,
        writeConfig,
        allowPty,
        allowGitConfig = false,
        enableWeakerNetworkIsolation = false,
        allowAppleEvents = false,
        shell,
    } = params;

    const hasReadRestrictions = readConfig && readConfig.denyOnly.length > 0;
    const hasWriteRestrictions = writeConfig !== undefined;

    if (!needsNetworkRestriction && !hasReadRestrictions && !hasWriteRestrictions) {
        return command;
    }

    const logTag = generateLogTag(command);

    const profile = generateSandboxProfile({
        readConfig,
        writeConfig,
        httpProxyPort,
        socksProxyPort,
        needsNetworkRestriction,
        allowUnixSockets,
        allowAllUnixSockets,
        allowLocalBinding,
        allowMachLookup,
        allowPty,
        allowGitConfig,
        enableWeakerNetworkIsolation,
        allowAppleEvents,
        logTag,
    });

    const proxyEnvArgs = generateProxyEnvVars(httpProxyPort, socksProxyPort, caCertPath, proxyAuthToken);

    const wrappedCommand = shellquote.quote([
        "env",
        ...proxyEnvArgs,
        "/usr/bin/sandbox-exec",
        "-p",
        profile,
        shell,
        "-c",
        command,
    ]);

    logForDebugging(
        `[macOS] restrictions — network: ${needsNetworkRestriction}, read: ${hasReadRestrictions}, write: ${hasWriteRestrictions}`,
    );

    return wrappedCommand;
}
