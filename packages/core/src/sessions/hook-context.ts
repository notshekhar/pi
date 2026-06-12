/**
 * SessionStart hook context rides the first user prompt inside a tagged
 * wrapper so the model keeps it in history on every later turn. This module
 * is the single source of truth for that format: the turn runner wraps,
 * display surfaces (chat replay, session list, tree selector, fork selector,
 * editor text restoration) unwrap. Only the transcript and the model context
 * keep the raw text.
 */
const HOOK_CONTEXT_RE = /^<session-start-hook-context>\n([\s\S]*?)\n<\/session-start-hook-context>\n*/;

export function wrapSessionHookContext(context: string, text: string): string {
    return `<session-start-hook-context>\n${context}\n</session-start-hook-context>\n\n${text}`;
}

/** Split a user message into hook context and the user's own text, if wrapped. */
export function matchSessionHookContext(text: string): { context: string; rest: string } | null {
    const m = text.match(HOOK_CONTEXT_RE);
    if (!m) return null;
    return { context: m[1], rest: text.slice(m[0].length) };
}

export function stripSessionHookContext(text: string): string {
    return matchSessionHookContext(text)?.rest ?? text;
}
