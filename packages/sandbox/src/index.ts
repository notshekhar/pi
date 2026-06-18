/**
 * @notshekhar/loop-sandbox — OS-level sandbox for loop's bash tool.
 *
 * Ported/adapted from anthropic-experimental/sandbox-runtime (Apache-2.0):
 * https://github.com/anthropic-experimental/sandbox-runtime — see NOTICE.md.
 *
 * Filesystem isolation is stateless (a pure command wrap). The network
 * domain-allowlist needs host-side HTTP/SOCKS proxies, so when an allowlist is
 * configured the runtime starts them lazily, caches them for the process, and
 * live-swaps the active rules per command (mirroring upstream updateConfig).
 */
import * as fs from "node:fs";
import { getPlatform, getWslVersion } from "./platform";
import { getDefaultWritePaths } from "./sandbox-utils";
import { wrapCommandWithSandboxMacOS } from "./macos-sandbox-utils";
import {
    wrapCommandWithSandboxLinux,
    checkLinuxDependencies,
    initializeLinuxNetworkBridge,
    type SandboxDependencyCheck,
    type LinuxNetworkBridge,
} from "./linux-sandbox-utils";
import { createHttpProxyServer } from "./http-proxy";
import { createSocksProxyServer, type SocksProxyWrapper } from "./socks-proxy";
import { makeHostFilter, type DomainRules } from "./domain-filter";
import { logForDebugging } from "./debug";
import type { FsReadRestrictionConfig, FsWriteRestrictionConfig } from "./sandbox-schemas";

export type { SandboxDependencyCheck } from "./linux-sandbox-utils";
export type { FsReadRestrictionConfig, FsWriteRestrictionConfig, NetworkRestrictionConfig } from "./sandbox-schemas";
export type { DomainRules } from "./domain-filter";
export { getPlatform, getWslVersion } from "./platform";

/**
 * Network policy:
 *  - "allow" — full network.
 *  - "deny"  — no network at all.
 *  - { allow, deny } — per-domain allowlist enforced by the proxies. Hosts
 *    matching `deny` are blocked first; then `allow` (e.g. "*.github.com");
 *    anything unmatched is denied.
 */
export type SandboxNetwork = "allow" | "deny" | { allow: string[]; deny?: string[] };

/**
 * Public sandbox config. Filesystem exposes BOTH allow and deny lists (read =
 * deny-then-allow-back, write = allow-only-minus-deny) so callers can express
 * an allowlist or a denylist posture.
 */
export interface SandboxConfig {
    filesystem: {
        /** Extra writable paths, beyond defaults + the working directory. */
        allowWrite: string[];
        /** Paths to deny writing, even within writable regions. */
        denyWrite: string[];
        /** Broad regions to deny reading. */
        denyRead: string[];
        /** Re-allow reads within denied regions (most-specific wins). */
        allowRead: string[];
        /** Allow writes to .git/config (default false; .git/hooks always denied). */
        allowGitConfig?: boolean;
        /**
         * Fully read-only filesystem: NO writable paths at all — not even the
         * working directory (only harmless device nodes like /dev/null). Used to
         * give read-only agents kernel-enforced no-write bash. Overrides
         * allowWrite/denyWrite.
         */
        readOnly?: boolean;
    };
    network: SandboxNetwork;
}

export interface WrapOptions {
    command: string;
    /** Absolute path to the shell binary (e.g. from the host's shell config). */
    shell: string;
    /** The command's working directory — always made writable. */
    cwd: string;
    config: SandboxConfig;
}

/** A spawn descriptor: run `spawn(argv[0], argv.slice(1), { shell: false })`. */
export interface WrappedCommand {
    argv: string[];
}

/** A config that restricts writes to cwd + system temp and blocks all network. */
export function defaultSandboxConfig(): SandboxConfig {
    return {
        filesystem: { allowWrite: [], denyWrite: [], denyRead: [], allowRead: [], allowGitConfig: false },
        network: "deny",
    };
}

/** Whether the current platform can enforce a sandbox at all. */
export function isSandboxSupported(): boolean {
    const platform = getPlatform();
    if (platform === "linux") return getWslVersion() !== "1"; // WSL1 has no bwrap.
    return platform === "macos"; // Windows is a separate (unverified) scaffold.
}

/** Check that the platform's sandbox tooling is actually installed/usable. */
export function checkSandboxDependencies(opts: { bwrapPath?: string } = {}): SandboxDependencyCheck {
    if (!isSandboxSupported()) return { errors: ["Unsupported platform"], warnings: [] };
    if (getPlatform() === "linux") return checkLinuxDependencies(opts);
    return { errors: [], warnings: [] }; // macOS sandbox-exec ships with the OS.
}

function buildWriteConfig(cwd: string, config: SandboxConfig): FsWriteRestrictionConfig {
    // Read-only: only the harmless default device/temp paths are writable —
    // cwd is deliberately excluded, so no command can modify the project.
    if (config.filesystem.readOnly) {
        return { allowOnly: getDefaultWritePaths(), denyWithinAllow: [] };
    }
    return {
        allowOnly: [...getDefaultWritePaths(), cwd, ...config.filesystem.allowWrite],
        denyWithinAllow: config.filesystem.denyWrite,
    };
}

function buildReadConfig(config: SandboxConfig): FsReadRestrictionConfig {
    return {
        denyOnly: config.filesystem.denyRead,
        allowWithinDeny: config.filesystem.allowRead,
    };
}

interface ProxyHandles {
    httpServer: ReturnType<typeof createHttpProxyServer>;
    socksServer: SocksProxyWrapper;
    httpPort: number;
    socksPort: number;
}

/**
 * Process-global sandbox runtime. Holds the lazily-started network proxies and
 * the live domain rules they filter against.
 */
class SandboxRuntime {
    private proxies?: ProxyHandles;
    private starting?: Promise<ProxyHandles>;
    /** Linux only: host-side socat bridge exposing the proxies as Unix sockets. */
    private linuxBridge?: LinuxNetworkBridge;
    /** Read live by the proxy filter, so allowlist edits apply without rebind. */
    private rules: DomainRules = { allow: [], deny: [] };

    isSupported(): boolean {
        return isSandboxSupported();
    }

    checkDependencies(opts: { bwrapPath?: string } = {}): SandboxDependencyCheck {
        return checkSandboxDependencies(opts);
    }

    private async ensureProxies(): Promise<ProxyHandles> {
        if (this.proxies) return this.proxies;
        if (this.starting) return this.starting;

        const filter = makeHostFilter(() => this.rules);
        this.starting = (async () => {
            const httpServer = createHttpProxyServer({ filter });
            await new Promise<void>((resolve, reject) => {
                httpServer.once("error", reject);
                httpServer.listen(0, "127.0.0.1", () => {
                    httpServer.removeListener("error", reject);
                    resolve();
                });
            });
            const httpAddr = httpServer.address();
            const httpPort = httpAddr && typeof httpAddr === "object" ? httpAddr.port : 0;
            httpServer.unref();

            const socksServer = createSocksProxyServer({ filter });
            const socksPort = await socksServer.listen(0, "127.0.0.1");
            socksServer.unref();

            logForDebugging(`network proxies up — http:${httpPort} socks:${socksPort}`);
            this.proxies = { httpServer, socksServer, httpPort, socksPort };
            return this.proxies;
        })();

        try {
            return await this.starting;
        } catch (err) {
            this.starting = undefined;
            throw err;
        }
    }

    /**
     * Wrap a command so the OS enforces the sandbox. Returns the spawn
     * descriptor, or null when the platform is unsupported (caller decides
     * fail-open/closed).
     */
    async wrap(opts: WrapOptions): Promise<WrappedCommand | null> {
        const { command, shell, cwd, config } = opts;
        if (!isSandboxSupported()) return null;

        const net = config.network;
        const needsNetworkRestriction = net !== "allow"; // "deny" or allowlist
        const useProxy = typeof net === "object";

        const platform = getPlatform();

        let httpProxyPort: number | undefined;
        let socksProxyPort: number | undefined;
        if (useProxy) {
            // Live-swap the rules first so the (already-running) proxies filter
            // against this command's allowlist.
            this.rules = { allow: net.allow, deny: net.deny ?? [] };
            const proxies = await this.ensureProxies();
            httpProxyPort = proxies.httpPort;
            socksProxyPort = proxies.socksPort;
        }

        const writeConfig = buildWriteConfig(cwd, config);
        const readConfig = buildReadConfig(config);

        let wrapped: string;
        if (platform === "macos") {
            wrapped = wrapCommandWithSandboxMacOS({
                command,
                needsNetworkRestriction,
                httpProxyPort,
                socksProxyPort,
                readConfig,
                writeConfig,
                allowGitConfig: config.filesystem.allowGitConfig,
                shell,
            });
        } else {
            // Linux allowlist needs the host socat bridge (loopback is gone
            // under --unshare-net). Start it lazily and pass the sockets in.
            let httpSocketPath: string | undefined;
            let socksSocketPath: string | undefined;
            if (useProxy && httpProxyPort && socksProxyPort) {
                if (!this.linuxBridge) {
                    this.linuxBridge = await initializeLinuxNetworkBridge(httpProxyPort, socksProxyPort);
                }
                httpSocketPath = this.linuxBridge.httpSocketPath;
                socksSocketPath = this.linuxBridge.socksSocketPath;
            }
            wrapped = wrapCommandWithSandboxLinux({
                command,
                needsNetworkRestriction,
                readConfig,
                writeConfig,
                allowGitConfig: config.filesystem.allowGitConfig,
                shell,
                httpSocketPath,
                socksSocketPath,
            });
        }

        return { argv: [shell, "-c", wrapped] };
    }

    /** Stop the proxies/bridge and clear state. Safe to call when idle. */
    async reset(): Promise<void> {
        const bridge = this.linuxBridge;
        this.linuxBridge = undefined;
        if (bridge) {
            for (const proc of [bridge.httpBridge, bridge.socksBridge]) {
                if (proc.pid) {
                    try {
                        process.kill(proc.pid, "SIGTERM");
                    } catch {
                        // already gone
                    }
                }
            }
            for (const sock of [bridge.httpSocketPath, bridge.socksSocketPath]) {
                try {
                    fs.rmSync(sock, { force: true });
                } catch {
                    // best effort
                }
            }
        }

        const p = this.proxies;
        this.proxies = undefined;
        this.starting = undefined;
        if (!p) return;
        await Promise.all([
            new Promise<void>((resolve) => p.httpServer.close(() => resolve())),
            p.socksServer.close().catch(() => {}),
        ]);
    }
}

/** Process-global sandbox runtime (proxies are process-wide). */
export const sandbox = new SandboxRuntime();
