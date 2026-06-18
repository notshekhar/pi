/**
 * Dangerous files that should be protected from writes. These can be used for
 * code execution or data exfiltration.
 */
export declare const DANGEROUS_FILES: readonly [
    ".gitconfig",
    ".gitmodules",
    ".bashrc",
    ".bash_profile",
    ".zshrc",
    ".zprofile",
    ".profile",
    ".ripgreprc",
    ".mcp.json",
];
/**
 * Dangerous directories that contain sensitive config or executable files.
 */
export declare const DANGEROUS_DIRECTORIES: readonly [".git", ".vscode", ".idea"];
/**
 * Dangerous directories to deny writes to. Excludes .git (needed writable for
 * git operations — we block .git/hooks and .git/config specifically instead).
 */
export declare function getDangerousDirectories(): string[];
/**
 * Normalize for case-insensitive comparison, preventing bypass via mixed-case
 * paths on case-insensitive filesystems (macOS/Windows).
 */
export declare function normalizeCaseForComparison(pathStr: string): string;
/** Whether a path pattern contains glob characters. */
export declare function containsGlobChars(pathPattern: string): boolean;
/**
 * Remove a trailing /** glob suffix — it just means "directory and everything
 * under it", which subpath matching already covers.
 */
export declare function removeTrailingGlobSuffix(pathPattern: string): string;
/**
 * Whether a symlink resolution crosses expected path boundaries (would broaden
 * scope). Returns true if the resolved path is an ancestor of the original or
 * resolves to a system root.
 */
export declare function isSymlinkOutsideBoundary(originalPath: string, resolvedPath: string): boolean;
/**
 * Normalize a path for sandbox configs: expand ~, resolve relatives against
 * cwd, resolve symlinks (for non-glob paths, when staying within boundary), and
 * preserve wildcards for globs.
 */
export declare function normalizePathForSandbox(pathPattern: string): string;
/**
 * System paths that should stay writable for commands to work. Intentionally
 * broad for compatibility; tighten in security-sensitive environments.
 */
export declare function getDefaultWritePaths(): string[];
/**
 * Per-tool trust-store env vars set to the TLS-termination CA cert path so
 * HTTPS clients in the sandboxed child accept proxy-minted certs.
 */
export declare const CA_TRUST_VARS: readonly [
    "NODE_EXTRA_CA_CERTS",
    "SSL_CERT_FILE",
    "CURL_CA_BUNDLE",
    "REQUESTS_CA_BUNDLE",
    "PIP_CERT",
    "GIT_SSL_CAINFO",
    "AWS_CA_BUNDLE",
    "CARGO_HTTP_CAINFO",
    "DENO_CERT",
];
/**
 * Generate proxy environment variables for sandboxed processes. Without proxy
 * ports (Stage 1) this returns just the minimal SANDBOX_RUNTIME + TMPDIR set.
 */
export declare function generateProxyEnvVars(
    httpProxyPort?: number,
    socksProxyPort?: number,
    caCertPath?: string,
    proxyAuthToken?: string,
): string[];
/** Encode a command for sandbox monitoring (truncate to 100 chars, base64). */
export declare function encodeSandboxedCommand(command: string): string;
/** Decode a base64-encoded command from sandbox monitoring. */
export declare function decodeSandboxedCommand(encodedCommand: string): string;
/**
 * Convert a gitignore-style glob pattern to a regular expression.
 * - `*` matches any chars except `/`
 * - `**` matches any chars including `/`
 * - `?` matches a single char except `/`
 * - `[abc]` matches a char in the set
 */
export declare function globToRegex(globPattern: string): string;
/**
 * Expand a glob pattern into concrete file paths. Used on Linux where
 * bubblewrap doesn't support globs natively.
 */
export declare function expandGlobPattern(globPath: string): string[];
