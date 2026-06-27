/** Errors render in chat, never persist to the session — make them readable. */

/** Keep one-liners short: first line only, trimmed, capped. */
function firstLine(s: string): string {
    const line = (s.split("\n")[0] ?? s).trim();
    return line.length > 500 ? line.slice(0, 500) + "…" : line;
}

/** Pull the human-readable message out of a provider's JSON error body. */
function providerMessage(body: unknown): string | undefined {
    if (!body) return undefined;
    let obj: unknown = body;
    if (typeof body === "string") {
        const s = body.trim();
        if (!s) return undefined;
        try {
            obj = JSON.parse(s);
        } catch {
            return firstLine(s); // plain-text body
        }
    }
    if (obj && typeof obj === "object") {
        const o = obj as Record<string, unknown>;
        const err = o.error;
        if (err && typeof err === "object") {
            const m = (err as Record<string, unknown>).message;
            if (typeof m === "string" && m.trim()) return firstLine(m);
        }
        if (typeof err === "string" && err.trim()) return firstLine(err);
        if (typeof o.message === "string" && o.message.trim()) return firstLine(o.message);
    }
    return undefined;
}

/** Node/Bun syscall errno codes look like EPERM, ENOENT, ECONNREFUSED, ETIMEDOUT. */
function isErrno(s: unknown): s is string {
    return typeof s === "string" && /^E[A-Z]{2,}$/.test(s);
}

/**
 * Walk the error and its `.cause` chain for a diagnostic code to show like
 * Claude's "(EPERM)": prefer a syscall errno, else a non-generic `.code`/`.name`.
 * Bun/Node bury the real syscall error inside `.cause` on fetch/IO failures, so a
 * flat `.code` read misses it — that's how a real EPERM/ECONNREFUSED ends up
 * surfacing as a vague top-line message with no actionable code.
 */
function errorCode(err: unknown, depth = 0): string | undefined {
    if (!err || typeof err !== "object" || depth > 5) return undefined;
    const e = err as Record<string, unknown>;
    if (isErrno(e.code)) return e.code;
    const fromCause = errorCode(e.cause, depth + 1);
    if (fromCause) return fromCause;
    if (typeof e.code === "string" && e.code.trim() && e.code !== "ERR_UNHANDLED_ERROR") return e.code;
    const name = typeof e.name === "string" ? e.name : "";
    if (name && name !== "Error" && name !== "TypeError" && name !== "AI_APICallError") return name;
    return undefined;
}

/** Append a "(CODE)" tag when one is available and not already in the message. */
function withCode(message: string, err: unknown): string {
    const code = errorCode(err);
    return code && !message.includes(code) ? `${message} (${code})` : message;
}

/**
 * AI-SDK throws AI_APICallError (and RetryError wrapping it) on provider HTTP
 * failures. Its .message embeds the whole request (system prompt + tool defs),
 * which is useless noise in chat — surface the provider's real message instead.
 *
 * For non-provider failures (filesystem, network, IO) we append the underlying
 * syscall code — EPERM, ECONNREFUSED, ENOENT — so a cryptic machine-level error
 * is diagnosable instead of collapsing to "unknown error".
 */
export function formatError(err: unknown): string {
    // RetryError wraps the underlying failure once retries are exhausted.
    if (err && typeof err === "object" && "lastError" in err && (err as { lastError?: unknown }).lastError) {
        return formatError((err as { lastError: unknown }).lastError);
    }

    if (err && typeof err === "object") {
        const e = err as Record<string, unknown>;
        const isApiError = e.name === "AI_APICallError" || "responseBody" in e || "statusCode" in e || "url" in e;
        if (isApiError) {
            const status = typeof e.statusCode === "number" ? e.statusCode : undefined;
            const real = providerMessage(e.data) ?? providerMessage(e.responseBody);
            if (real) return status ? `${real} (HTTP ${status})` : withCode(real, e);
            // No structured body — fall back to the message, but only its first
            // line so the request dump never floods the console.
            if (typeof e.message === "string" && e.message.trim()) return withCode(firstLine(e.message), e);
            if (status) return `request failed (HTTP ${status})`;
            return withCode("request failed", e);
        }
    }

    if (err instanceof Error) return withCode(firstLine(err.message), err);
    if (typeof err === "string") return err;
    try {
        return JSON.stringify(err);
    } catch {
        return String(err);
    }
}
