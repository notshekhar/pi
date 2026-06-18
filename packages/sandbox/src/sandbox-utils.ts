/**
 * Shared sandbox helpers: path normalization, glob handling, default writable
 * paths, proxy env vars, and the dangerous-file/dir lists.
 *
 * Ported from anthropic-experimental/sandbox-runtime (Apache-2.0):
 * https://github.com/anthropic-experimental/sandbox-runtime — see NOTICE.md.
 */
import { homedir } from "node:os";
import * as path from "node:path";
import * as fs from "node:fs";
import { getPlatform } from "./platform";
import { logForDebugging } from "./debug";

/**
 * Dangerous files that should be protected from writes. These can be used for
 * code execution or data exfiltration.
 */
export const DANGEROUS_FILES = [
    ".gitconfig",
    ".gitmodules",
    ".bashrc",
    ".bash_profile",
    ".zshrc",
    ".zprofile",
    ".profile",
    ".ripgreprc",
    ".mcp.json",
] as const;

/**
 * Dangerous directories that contain sensitive config or executable files.
 */
export const DANGEROUS_DIRECTORIES = [".git", ".vscode", ".idea"] as const;

/**
 * Dangerous directories to deny writes to. Excludes .git (needed writable for
 * git operations — we block .git/hooks and .git/config specifically instead).
 */
export function getDangerousDirectories(): string[] {
    return [...DANGEROUS_DIRECTORIES.filter((d) => d !== ".git"), ".claude/commands", ".claude/agents"];
}

/**
 * Normalize for case-insensitive comparison, preventing bypass via mixed-case
 * paths on case-insensitive filesystems (macOS/Windows).
 */
export function normalizeCaseForComparison(pathStr: string): string {
    return pathStr.toLowerCase();
}

/** Whether a path pattern contains glob characters. */
export function containsGlobChars(pathPattern: string): boolean {
    return (
        pathPattern.includes("*") || pathPattern.includes("?") || pathPattern.includes("[") || pathPattern.includes("]")
    );
}

/**
 * Remove a trailing /** glob suffix — it just means "directory and everything
 * under it", which subpath matching already covers.
 */
export function removeTrailingGlobSuffix(pathPattern: string): string {
    const stripped = pathPattern.replace(/\/\*\*$/, "");
    return stripped || "/";
}

/**
 * Whether a symlink resolution crosses expected path boundaries (would broaden
 * scope). Returns true if the resolved path is an ancestor of the original or
 * resolves to a system root.
 */
export function isSymlinkOutsideBoundary(originalPath: string, resolvedPath: string): boolean {
    const normalizedOriginal = path.normalize(originalPath);
    const normalizedResolved = path.normalize(resolvedPath);

    if (normalizedResolved === normalizedOriginal) return false;

    // macOS /tmp -> /private/tmp and /var -> /private/var are legitimate.
    if (normalizedOriginal.startsWith("/tmp/") && normalizedResolved === "/private" + normalizedOriginal) {
        return false;
    }
    if (normalizedOriginal.startsWith("/var/") && normalizedResolved === "/private" + normalizedOriginal) {
        return false;
    }
    if (normalizedOriginal.startsWith("/private/tmp/") && normalizedResolved === normalizedOriginal) {
        return false;
    }
    if (normalizedOriginal.startsWith("/private/var/") && normalizedResolved === normalizedOriginal) {
        return false;
    }

    if (normalizedResolved === "/") return true;

    const resolvedParts = normalizedResolved.split("/").filter(Boolean);
    if (resolvedParts.length <= 1) return true;

    if (normalizedOriginal.startsWith(normalizedResolved + "/")) return true;

    let canonicalOriginal = normalizedOriginal;
    if (normalizedOriginal.startsWith("/tmp/")) {
        canonicalOriginal = "/private" + normalizedOriginal;
    } else if (normalizedOriginal.startsWith("/var/")) {
        canonicalOriginal = "/private" + normalizedOriginal;
    }

    if (canonicalOriginal !== normalizedOriginal && canonicalOriginal.startsWith(normalizedResolved + "/")) {
        return true;
    }

    const resolvedStartsWithOriginal = normalizedResolved.startsWith(normalizedOriginal + "/");
    const resolvedStartsWithCanonical =
        canonicalOriginal !== normalizedOriginal && normalizedResolved.startsWith(canonicalOriginal + "/");
    const resolvedIsCanonical = canonicalOriginal !== normalizedOriginal && normalizedResolved === canonicalOriginal;
    const resolvedIsSame = normalizedResolved === normalizedOriginal;

    if (!resolvedIsSame && !resolvedIsCanonical && !resolvedStartsWithOriginal && !resolvedStartsWithCanonical) {
        return true;
    }

    return false;
}

/**
 * Normalize a path for sandbox configs: expand ~, resolve relatives against
 * cwd, resolve symlinks (for non-glob paths, when staying within boundary), and
 * preserve wildcards for globs.
 */
export function normalizePathForSandbox(pathPattern: string): string {
    const cwd = process.cwd();
    let normalizedPath = pathPattern;

    if (pathPattern === "~") {
        normalizedPath = homedir();
    } else if (pathPattern.startsWith("~/")) {
        normalizedPath = homedir() + pathPattern.slice(1);
    } else if (pathPattern.startsWith("./") || pathPattern.startsWith("../")) {
        normalizedPath = path.resolve(cwd, pathPattern);
    } else if (!path.isAbsolute(pathPattern)) {
        normalizedPath = path.resolve(cwd, pathPattern);
    }

    if (containsGlobChars(normalizedPath)) {
        const staticPrefix = normalizedPath.split(/[*?[\]]/)[0];
        if (staticPrefix && staticPrefix !== "/") {
            const baseDir = staticPrefix.endsWith("/") ? staticPrefix.slice(0, -1) : path.dirname(staticPrefix);
            try {
                const resolvedBaseDir = fs.realpathSync(baseDir);
                if (!isSymlinkOutsideBoundary(baseDir, resolvedBaseDir)) {
                    const patternSuffix = normalizedPath.slice(baseDir.length);
                    return resolvedBaseDir + patternSuffix;
                }
            } catch {
                // Directory doesn't exist / can't resolve — keep original pattern.
            }
        }
        return normalizedPath;
    }

    try {
        const resolvedPath = fs.realpathSync(normalizedPath);
        if (!isSymlinkOutsideBoundary(normalizedPath, resolvedPath)) {
            normalizedPath = resolvedPath;
        }
    } catch {
        // Path doesn't exist / can't resolve — keep normalized path.
    }

    return normalizedPath;
}

/**
 * System paths that should stay writable for commands to work. Intentionally
 * broad for compatibility; tighten in security-sensitive environments.
 */
export function getDefaultWritePaths(): string[] {
    const homeDir = homedir();
    return [
        "/dev/stdout",
        "/dev/stderr",
        "/dev/null",
        "/dev/tty",
        "/dev/dtracehelper",
        "/dev/autofs_nowait",
        "/tmp/claude",
        "/private/tmp/claude",
        path.join(homeDir, ".npm/_logs"),
        path.join(homeDir, ".claude/debug"),
    ];
}

/**
 * Per-tool trust-store env vars set to the TLS-termination CA cert path so
 * HTTPS clients in the sandboxed child accept proxy-minted certs.
 */
export const CA_TRUST_VARS = [
    "NODE_EXTRA_CA_CERTS",
    "SSL_CERT_FILE",
    "CURL_CA_BUNDLE",
    "REQUESTS_CA_BUNDLE",
    "PIP_CERT",
    "GIT_SSL_CAINFO",
    "AWS_CA_BUNDLE",
    "CARGO_HTTP_CAINFO",
    "DENO_CERT",
] as const;

/**
 * Generate proxy environment variables for sandboxed processes. Without proxy
 * ports (Stage 1) this returns just the minimal SANDBOX_RUNTIME + TMPDIR set.
 */
export function generateProxyEnvVars(
    httpProxyPort?: number,
    socksProxyPort?: number,
    caCertPath?: string,
    proxyAuthToken?: string,
): string[] {
    const auth = proxyAuthToken ? `srt:${proxyAuthToken}@` : "";
    const tmpdir = process.env.LOOP_SANDBOX_TMPDIR || process.env.CLAUDE_CODE_TMPDIR || "/tmp/claude";
    const envVars: string[] = [`SANDBOX_RUNTIME=1`, `TMPDIR=${tmpdir}`];

    if (caCertPath) {
        for (const v of CA_TRUST_VARS) envVars.push(`${v}=${caCertPath}`);
    }

    if (!httpProxyPort && !socksProxyPort) return envVars;

    const noProxyAddresses = [
        "localhost",
        "127.0.0.1",
        "::1",
        "*.local",
        ".local",
        "169.254.0.0/16",
        "10.0.0.0/8",
        "172.16.0.0/12",
        "192.168.0.0/16",
    ].join(",");
    envVars.push(`NO_PROXY=${noProxyAddresses}`);
    envVars.push(`no_proxy=${noProxyAddresses}`);

    if (httpProxyPort) {
        envVars.push(`HTTP_PROXY=http://${auth}localhost:${httpProxyPort}`);
        envVars.push(`HTTPS_PROXY=http://${auth}localhost:${httpProxyPort}`);
        envVars.push(`http_proxy=http://${auth}localhost:${httpProxyPort}`);
        envVars.push(`https_proxy=http://${auth}localhost:${httpProxyPort}`);
    }

    if (socksProxyPort) {
        envVars.push(`ALL_PROXY=socks5h://${auth}localhost:${socksProxyPort}`);
        envVars.push(`all_proxy=socks5h://${auth}localhost:${socksProxyPort}`);
        envVars.push(`FTP_PROXY=socks5h://${auth}localhost:${socksProxyPort}`);
        envVars.push(`ftp_proxy=socks5h://${auth}localhost:${socksProxyPort}`);
    }

    return envVars;
}

/** Encode a command for sandbox monitoring (truncate to 100 chars, base64). */
export function encodeSandboxedCommand(command: string): string {
    return Buffer.from(command.slice(0, 100)).toString("base64");
}

/** Decode a base64-encoded command from sandbox monitoring. */
export function decodeSandboxedCommand(encodedCommand: string): string {
    return Buffer.from(encodedCommand, "base64").toString("utf8");
}

/**
 * Convert a gitignore-style glob pattern to a regular expression.
 * - `*` matches any chars except `/`
 * - `**` matches any chars including `/`
 * - `?` matches a single char except `/`
 * - `[abc]` matches a char in the set
 */
export function globToRegex(globPattern: string): string {
    return (
        "^" +
        globPattern
            .replace(/[.^$+{}()|\\]/g, "\\$&")
            .replace(/\[([^\]]*?)$/g, "\\[$1")
            .replace(/\*\*\//g, "__GLOBSTAR_SLASH__")
            .replace(/\*\*/g, "__GLOBSTAR__")
            .replace(/\*/g, "[^/]*")
            .replace(/\?/g, "[^/]")
            .replace(/__GLOBSTAR_SLASH__/g, "(.*/)?")
            .replace(/__GLOBSTAR__/g, ".*") +
        "$"
    );
}

/**
 * Expand a glob pattern into concrete file paths. Used on Linux where
 * bubblewrap doesn't support globs natively.
 */
export function expandGlobPattern(globPath: string): string[] {
    const normalizedPattern = normalizePathForSandbox(globPath);
    const staticPrefix = normalizedPattern.split(/[*?[\]]/)[0];
    if (!staticPrefix || staticPrefix === "/") {
        logForDebugging(`Glob pattern too broad, skipping: ${globPath}`);
        return [];
    }

    const baseDir = staticPrefix.endsWith("/") ? staticPrefix.slice(0, -1) : path.dirname(staticPrefix);
    if (!fs.existsSync(baseDir)) {
        logForDebugging(`Base directory for glob does not exist: ${baseDir}`);
        return [];
    }

    const regex = new RegExp(globToRegex(normalizedPattern));
    const results: string[] = [];
    try {
        const entries = fs.readdirSync(baseDir, { recursive: true, withFileTypes: true });
        for (const entry of entries) {
            const parentDir =
                (entry as { parentPath?: string }).parentPath ?? (entry as { path?: string }).path ?? baseDir;
            const fullPath = path.join(parentDir, entry.name);
            if (regex.test(fullPath)) results.push(fullPath);
        }
    } catch (err) {
        logForDebugging(`Error expanding glob pattern ${globPath}: ${err}`);
    }

    return results;
}
