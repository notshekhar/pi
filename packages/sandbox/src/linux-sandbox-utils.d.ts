import type { FsReadRestrictionConfig, FsWriteRestrictionConfig } from "./sandbox-schemas";
export interface SandboxDependencyCheck {
    errors: string[];
    warnings: string[];
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
}
/** Check Stage-1 Linux dependencies (just bwrap for now). */
export declare function checkLinuxDependencies(opts?: { bwrapPath?: string }): SandboxDependencyCheck;
/**
 * Wrap a command with the Linux sandbox. Returns a single shell-quoted command
 * string (run via `<shell> -c <result>`), or the original command when no
 * restrictions apply.
 */
export declare function wrapCommandWithSandboxLinux(params: LinuxSandboxParams): string;
