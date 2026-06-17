export type Platform = "macos" | "linux" | "windows" | "unknown";
/**
 * Get the WSL version (1 or 2+) if running in WSL.
 * Returns undefined if not running in WSL.
 */
export declare function getWslVersion(): string | undefined;
/**
 * Detect the current platform. All Linux (including WSL2+) returns "linux";
 * use getWslVersion() to detect WSL1 (unsupported by bubblewrap).
 */
export declare function getPlatform(): Platform;
