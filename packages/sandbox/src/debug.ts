/**
 * Minimal debug logger for the sandbox module. Upstream
 * (anthropic-experimental/sandbox-runtime) has a richer logger; we only need a
 * gated stderr line. Enable with PI_SANDBOX_DEBUG=1.
 */
export function logForDebugging(message: string, opts?: { level?: "error" | "warn" | "info" }): void {
    if (!process.env.PI_SANDBOX_DEBUG) return;
    const level = opts?.level ?? "info";
    process.stderr.write(`[sandbox:${level}] ${message}\n`);
}
