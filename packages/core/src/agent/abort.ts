/** Shared detection for aborted LLM calls (AI SDK throws various shapes). */
export function isAbortError(err: unknown): boolean {
    if (!err) return false;
    const e = err as { name?: string; message?: string };
    return e.name === "AbortError" || /aborted/i.test(e.message ?? "");
}
