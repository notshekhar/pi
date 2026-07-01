/**
 * Bash command denylist — a configurable guardrail, NOT a security boundary.
 *
 * It parses the command line and refuses anything whose resolved command name
 * (optionally + subcommand) matches a denied entry. This reliably stops an
 * honest model and ordinary invocations; it does NOT stop a determined bypass
 * (base64-pipe-to-sh, write-a-script-then-run-it, eval, …). String inspection
 * is fundamentally bypassable — that's what an OS sandbox is for, and we're
 * deliberately deferring that. Keep this in mind before trusting it as a jail.
 */

/** A denied command — a command name, optionally + subcommand ("git commit"). */
export type BashDenyEntry = string;

/**
 * Coerce a stored entry to its pattern string. Tolerates the legacy
 * `{ pattern, reason }` object form that older builds persisted to settings, so
 * an existing ~/.loop/settings.json doesn't crash after the type changed.
 */
export function denyPattern(entry: unknown): string {
    if (typeof entry === "string") return entry;
    if (entry && typeof entry === "object" && typeof (entry as { pattern?: unknown }).pattern === "string") {
        return (entry as { pattern: string }).pattern;
    }
    return "";
}

/**
 * Seeded when the user hasn't configured `bashDeny`. Commit/push stay with the
 * human (commit or push only when explicitly asked). Users override the whole
 * list via settings.
 */
export const DEFAULT_BASH_DENY: BashDenyEntry[] = ["git commit", "git push"];

/**
 * Commands that run another command; we look past them to the real one so
 * `sudo rm` or `rtk git commit` resolve to `rm` / `git commit`. This closes the
 * easy wrapper bypass — it does not (and cannot) close every bypass.
 * `rtk` is the user's token-proxy that rewrites every command (`rtk <cmd>`, or
 * `rtk proxy <cmd>` for raw passthrough); treat both forms as transparent.
 */
const WRAPPERS = new Set(["sudo", "env", "command", "nohup", "time", "nice", "stdbuf", "xargs", "rtk"]);

/** Shell operators that separate one executed command from the next. */
const SEGMENT_SEPARATORS = /\|\||&&|[;|&\n]/;

/** `sh -c "<script>"` (and bash/zsh/…) runs an inline script — pull it out. */
const SHELL_DASH_C = /\b(?:sh|bash|zsh|dash|ksh)\s+-c\s+(['"])([\s\S]*?)\1/g;

/** Strip surrounding quotes, then reduce a path to its basename (/bin/rm → rm). */
function normalizeToken(token: string): string {
    const unquoted = token.replace(/^['"]+/, "").replace(/['"]+$/, "");
    const base = unquoted.split("/").pop();
    return base && base.length > 0 ? base : unquoted;
}

/**
 * Break a command line into the individual commands it would run. Command
 * substitutions ($(...) and `...`) are flattened into their own segments so a
 * denied command hidden inside one is still seen.
 */
function splitSegments(command: string): string[] {
    const flattened = command
        .replace(SHELL_DASH_C, " ; $2 ; ")
        .replace(/\$\(([\s\S]*?)\)/g, " ; $1 ; ")
        .replace(/`/g, " ; ");
    return flattened
        .split(SEGMENT_SEPARATORS)
        .map((segment) => segment.trim())
        .filter((segment) => segment.length > 0);
}

/**
 * Resolve one segment to its real command token plus the following tokens.
 * Leading `VAR=value` assignments and wrapper commands (sudo/env/…) are peeled
 * off so `sudo FOO=1 /bin/rm -rf` resolves to command `rm`.
 */
function resolveSegment(segment: string): { command: string; rest: string[] } | null {
    let tokens = segment.split(/\s+/).filter(Boolean);
    // Peel leading env-assignments and wrappers until the real command surfaces.
    // Either kind can come first (`env FOO=1 sudo rm`), so loop over both.
    for (let peeled = true; peeled && tokens.length > 0;) {
        peeled = false;
        if (/^[A-Za-z_][A-Za-z0-9_]*=/.test(tokens[0])) {
            tokens = tokens.slice(1);
            peeled = true;
            continue;
        }
        const head = normalizeToken(tokens[0]);
        if (WRAPPERS.has(head)) {
            tokens = tokens.slice(1);
            // `rtk proxy <cmd>` — the proxy subcommand is part of the wrapper.
            if (head === "rtk" && tokens.length > 0 && normalizeToken(tokens[0]) === "proxy") {
                tokens = tokens.slice(1);
            }
            peeled = true;
        }
    }
    if (tokens.length === 0) return null;
    return {
        command: normalizeToken(tokens[0]),
        rest: tokens.slice(1).map(normalizeToken),
    };
}

/**
 * Whether a resolved segment matches a denylist pattern. Single-word patterns
 * ("rm") match on the command name; multi-word patterns ("git commit") also
 * require the following tokens to match as a prefix.
 */
function segmentMatchesPattern(resolved: { command: string; rest: string[] }, pattern: string): boolean {
    const words = pattern.trim().split(/\s+/).map(normalizeToken);
    if (words.length === 0 || words[0] !== resolved.command) return false;
    const subcommands = words.slice(1);
    return subcommands.every((word, index) => resolved.rest[index] === word);
}

/**
 * Return the first denylist pattern the command would trigger, or null if none.
 * The first matching entry wins, in denylist order.
 */
export function findDeniedCommand(command: string, denylist: BashDenyEntry[]): string | null {
    if (denylist.length === 0) return null;
    const segments = splitSegments(command);
    const resolved = segments.map(resolveSegment).filter((s): s is { command: string; rest: string[] } => s !== null);

    for (const entry of denylist) {
        const pattern = denyPattern(entry);
        if (!pattern) continue;
        if (resolved.some((segment) => segmentMatchesPattern(segment, pattern))) {
            return pattern;
        }
    }
    return null;
}

/**
 * The refusal handed back to the model. Kept to 2-3 lines but deliberately
 * framed as the user's settled decision (not an error) and ruling out
 * equivalents, so the model redirects instead of hunting for a workaround.
 */
export function formatDenyRefusal(pattern: string): string {
    return (
        `\`${pattern}\` is blocked by the user's bash policy — intentional, not an error. ` +
        `Don't retry it or run an equivalent (different flags, full path, piping, a script). ` +
        `If it's truly required, ask the user to run it or to remove it from their denylist; otherwise continue without it.`
    );
}
