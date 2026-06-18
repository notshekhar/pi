import { type SandboxDependencyCheck } from "./linux-sandbox-utils";
export type { SandboxDependencyCheck } from "./linux-sandbox-utils";
export type { FsReadRestrictionConfig, FsWriteRestrictionConfig, NetworkRestrictionConfig } from "./sandbox-schemas";
export { getPlatform, getWslVersion } from "./platform";
/**
 * Network policy. Stage 1 supports the two extremes; the per-domain allowlist
 * (`{ allow: [...] }`) is Stage 2 (needs the proxy stack).
 */
export type SandboxNetwork = "allow" | "deny";
/**
 * Public sandbox config. Filesystem deliberately exposes BOTH allow and deny
 * lists (read = deny-then-allow-back, write = allow-only-minus-deny) so callers
 * can express either an allowlist or a denylist posture.
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
export declare function defaultSandboxConfig(): SandboxConfig;
/** Whether the current platform can enforce a sandbox at all. */
export declare function isSandboxSupported(): boolean;
/** Check that the platform's sandbox tooling is actually installed/usable. */
export declare function checkSandboxDependencies(opts?: { bwrapPath?: string }): SandboxDependencyCheck;
/**
 * Wrap a command so the OS enforces the sandbox. Returns the spawn descriptor,
 * or null when the platform is unsupported (caller decides fail-open/closed).
 */
export declare function wrapWithSandbox(opts: WrapOptions): WrappedCommand | null;
