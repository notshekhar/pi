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
export declare function macGetMandatoryDenyPatterns(allowGitConfig?: boolean): string[];
/**
 * Wrap a command with the macOS sandbox. Returns a single shell-quoted command
 * string (run it via `<shell> -c <result>`), or the original command when no
 * restrictions apply.
 */
export declare function wrapCommandWithSandboxMacOS(params: MacOSSandboxParams): string;
