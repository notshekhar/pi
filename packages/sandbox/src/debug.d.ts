/**
 * Minimal debug logger for the sandbox module. Upstream
 * (anthropic-experimental/sandbox-runtime) has a richer logger; we only need a
 * gated stderr line. Enable with LOOP_SANDBOX_DEBUG=1.
 */
export declare function logForDebugging(
    message: string,
    opts?: {
        level?: "error" | "warn" | "info";
    },
): void;
