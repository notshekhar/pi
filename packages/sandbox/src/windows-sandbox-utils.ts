/**
 * Windows sandbox — NOT IMPLEMENTED (honest stub).
 *
 * Upstream (anthropic-experimental/sandbox-runtime, Apache-2.0) isolates Windows
 * via a native WFP component (`srt-win`, a Rust crate) plus restricted tokens.
 * That requires MSVC + the native binary and cannot be built or tested here, so
 * it is intentionally not ported. `isSandboxSupported()` returns false on
 * Windows, so this path is never taken by default — these throw only if a caller
 * forces the Windows wrapper.
 *
 * !!! UNVERIFIED / NOT IMPLEMENTED — see NOTICE.md and upstream vendor/srt-win.
 */

const NOT_IMPLEMENTED =
    "Windows sandbox is not implemented in @notshekhar/loop-sandbox. It needs the " +
    "native WFP component (srt-win). Run loop's bash tool without the sandbox on " +
    "Windows, or use WSL2 (Linux sandbox) / a container.";

export function checkWindowsDependencies(): { errors: string[]; warnings: string[] } {
    return { errors: [NOT_IMPLEMENTED], warnings: [] };
}

export function wrapCommandWithSandboxWindows(): never {
    throw new Error(NOT_IMPLEMENTED);
}
