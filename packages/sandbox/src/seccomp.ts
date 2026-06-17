/**
 * Linux seccomp integration.
 *
 * The actual BPF loader (`apply-seccomp`) is C and must be compiled on a Linux
 * host (`bun run build:seccomp` → vendor/seccomp/<arch>/). This module only
 * resolves that prebuilt binary and produces the shell prefix that applies the
 * filter; if the binary isn't present, seccomp is simply skipped (bubblewrap
 * still applies).
 *
 * !!! UNVERIFIED: cannot be compiled or run on macOS. Mechanism mirrors
 *     anthropic-experimental/sandbox-runtime (Apache-2.0) — see NOTICE.md.
 */
import { existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import shellquote from "shell-quote";
import { getPlatform } from "./platform";

/** Locate the package's vendor/ dir from this module (src in dev, dist in build). */
function vendorDir(): string {
    const here = dirname(fileURLToPath(import.meta.url));
    // src/seccomp.ts → ../vendor ; dist/index.js → ../vendor — both resolve up one.
    return join(here, "..", "vendor", "seccomp");
}

/** Path to the compiled apply-seccomp binary for this arch, if it exists. */
export function getApplySeccompBinaryPath(): string | undefined {
    if (getPlatform() !== "linux") return undefined;
    const arch = process.arch === "arm64" ? "arm64" : process.arch === "x64" ? "x64" : undefined;
    if (!arch) return undefined;
    const bin = join(vendorDir(), arch, "apply-seccomp");
    return existsSync(bin) ? bin : undefined;
}

/** Whether the seccomp layer is available (compiled) on this host. */
export function seccompAvailable(): boolean {
    return getApplySeccompBinaryPath() !== undefined;
}

/**
 * Shell prefix that runs a command under seccomp, or undefined when the binary
 * isn't built. The result ends with a trailing space; callers append
 * `<shell> -c <command>`.
 */
export function getApplySeccompPrefix(): string | undefined {
    const bin = getApplySeccompBinaryPath();
    return bin ? `${shellquote.quote([bin])} ` : undefined;
}
