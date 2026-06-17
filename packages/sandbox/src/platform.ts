/**
 * Platform detection utilities.
 *
 * Ported from anthropic-experimental/sandbox-runtime (Apache-2.0):
 * https://github.com/anthropic-experimental/sandbox-runtime — see NOTICE.md.
 */
import * as fs from "node:fs";

export type Platform = "macos" | "linux" | "windows" | "unknown";

/**
 * Get the WSL version (1 or 2+) if running in WSL.
 * Returns undefined if not running in WSL.
 */
export function getWslVersion(): string | undefined {
    if (process.platform !== "linux") return undefined;
    try {
        const procVersion = fs.readFileSync("/proc/version", { encoding: "utf8" });
        const wslVersionMatch = procVersion.match(/WSL(\d+)/i);
        if (wslVersionMatch && wslVersionMatch[1]) return wslVersionMatch[1];
        // WSL1's original format ("…-Microsoft") has no explicit version marker.
        if (procVersion.toLowerCase().includes("microsoft")) return "1";
        return undefined;
    } catch {
        return undefined;
    }
}

/**
 * Detect the current platform. All Linux (including WSL2+) returns "linux";
 * use getWslVersion() to detect WSL1 (unsupported by bubblewrap).
 */
export function getPlatform(): Platform {
    switch (process.platform) {
        case "darwin":
            return "macos";
        case "linux":
            return "linux";
        case "win32":
            return "windows";
        default:
            return "unknown";
    }
}
