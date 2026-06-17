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

/** A denied command: a bare string, or one with a custom reason for the model. */
export type BashDenyEntry = string | { pattern: string; reason?: string };

/**
 * Seeded when the user hasn't configured `bashDeny`. Commit/push are agent
 * decisions that should stay with the human (see project convention: commit or
 * push only when explicitly asked). Users override the whole list via settings.
 */
export const DEFAULT_BASH_DENY: BashDenyEntry[] = [
    { pattern: "git commit", reason: "committing is the user's call — let them commit, or ask them to." },
    { pattern: "git push", reason: "pushing is the user's call — ask the user to push." },
];

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

export interface DeniedMatch {
    /** The denylist pattern that matched (e.g. "git commit"). */
    pattern: string;
    /** Optional human-authored reason to relay to the model. */
    reason?: string;
}

function normalizeEntry(entry: BashDenyEntry): { pattern: string; reason?: string } {
    return typeof entry === "string" ? { pattern: entry } : entry;
}

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
    for (let peeled = true; peeled && tokens.length > 0; ) {
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
 * Return the first denylist entry the command would trigger, or null if none.
 * The first matching entry wins, in denylist order.
 */
export function findDeniedCommand(command: string, denylist: BashDenyEntry[]): DeniedMatch | null {
    if (denylist.length === 0) return null;
    const segments = splitSegments(command);
    const resolved = segments.map(resolveSegment).filter((s): s is { command: string; rest: string[] } => s !== null);

    for (const entry of denylist) {
        const { pattern, reason } = normalizeEntry(entry);
        if (resolved.some((segment) => segmentMatchesPattern(segment, pattern))) {
            return { pattern, reason };
        }
    }
    return null;
}

/**
 * The refusal handed back to the model. Tone is deliberate: this reads as a
 * settled decision by the user (an authority above the model), not an error or
 * a transient failure — so the model stops and redirects instead of hunting for
 * a workaround. It names the specific evasions models tend to reach for and
 * shuts them down, then offers the sanctioned exits (ask the user / move on).
 */
export function formatDenyRefusal(match: DeniedMatch): string {
    const lines = [
        `\`${match.pattern}\` is blocked by the user's bash policy (configured in ~/.pi/settings.json).`,
        `This is an intentional restriction chosen by the user — not an error, a sandbox glitch, or something to work around.`,
        ``,
        `Do not retry this, rewrite it, or run an equivalent command (different flags, a full path, piping through another tool, or writing a script) to achieve the same effect — equivalent forms are refused too, by design.`,
        ``,
        `If \`${match.pattern}\` is genuinely required to finish the task, stop and ask the user to run it themselves or to update their denylist. Otherwise, continue with an approach that doesn't use it.`,
    ];
    if (match.reason) {
        lines.push(``, `Reason: ${match.reason}`);
    }
    return lines.join("\n");
}
