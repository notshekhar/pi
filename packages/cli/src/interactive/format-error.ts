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

/**
 * AI-SDK throws AI_APICallError (and RetryError wrapping it) on provider HTTP
 * failures. Its .message embeds the whole request (system prompt + tool defs),
 * which is useless noise in chat — surface the provider's real message instead.
 */
export function formatError(err: unknown): string {
    // RetryError wraps the underlying failure once retries are exhausted.
    if (err && typeof err === "object" && "lastError" in err && (err as { lastError?: unknown }).lastError) {
        return formatError((err as { lastError: unknown }).lastError);
    }

    if (err && typeof err === "object") {
        const e = err as Record<string, unknown>;
        const isApiError =
            e.name === "AI_APICallError" || "responseBody" in e || "statusCode" in e || "url" in e;
        if (isApiError) {
            const status = typeof e.statusCode === "number" ? e.statusCode : undefined;
            const real = providerMessage(e.data) ?? providerMessage(e.responseBody);
            if (real) return status ? `${real} (HTTP ${status})` : real;
            // No structured body — fall back to the message, but only its first
            // line so the request dump never floods the console.
            if (typeof e.message === "string") return firstLine(e.message);
            if (status) return `request failed (HTTP ${status})`;
        }
    }

    if (err instanceof Error) return firstLine(err.message);
    if (typeof err === "string") return err;
    try {
        return JSON.stringify(err);
    } catch {
        return String(err);
    }
}
