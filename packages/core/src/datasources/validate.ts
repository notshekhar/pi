/**
 * Layer 1 read-only guard for the `sql` tool: a cheap, conservative static
 * check that rejects obvious mutations before a query is sent to the database.
 *
 * This is NOT the authoritative guard — string analysis cannot fully out-parse
 * SQL (a Postgres CTE like `WITH x AS (DELETE ... RETURNING *) SELECT ...`
 * mutates while looking like a read). The real enforcement is Layer 2 in
 * client.ts: every query runs inside a rolled-back READ ONLY transaction, so
 * the database engine itself refuses any write. This layer just turns the
 * common cases into a clear, fast tool-error.
 */

const ERROR_MESSAGE = "insert/update/alter operations are not allowed";

/** Statements permitted as the leading keyword of a query. */
const ALLOWED_LEADING = new Set(["select", "with", "explain", "show", "describe", "desc", "values", "table"]);

/**
 * Mutating / DDL / side-effecting tokens rejected anywhere in the query (after
 * comments and string literals are removed). Maintenance verbs like VACUUM and
 * ANALYZE are intentionally absent — they're only dangerous as a leading
 * keyword (already blocked by the allowlist), and listing ANALYZE here would
 * break the legitimate `EXPLAIN ANALYZE SELECT ...`.
 */
const FORBIDDEN_TOKENS = [
    "insert",
    "update",
    "delete",
    "merge",
    "upsert",
    "replace",
    "drop",
    "alter",
    "create",
    "truncate",
    "grant",
    "revoke",
    "call",
    "do",
    "copy",
    "lock",
    "pg_read_file",
    "pg_read_binary_file",
    "pg_ls_dir",
    "lo_import",
    "lo_export",
];
const FORBIDDEN_RE = new RegExp(`\\b(${FORBIDDEN_TOKENS.join("|")})\\b`, "i");
// `... INTO OUTFILE` / `INTO DUMPFILE` (MySQL writes a file to the server).
const INTO_FILE_RE = /\binto\s+(out|dump)file\b/i;

/**
 * Remove comments and blank out string/identifier literals so keyword scanning
 * can't be fooled by a value like '... delete ...' or an identifier "drop".
 */
function stripForAnalysis(query: string): string {
    return query
        .replace(/\/\*[\s\S]*?\*\//g, " ") // block comments
        .replace(/--[^\n]*/g, " ") // -- line comments
        .replace(/#[^\n]*/g, " ") // # line comments (MySQL)
        .replace(/'(?:[^'\\]|\\.|'')*'/g, " '' ") // single-quoted strings
        .replace(/"(?:[^"\\]|\\.|"")*"/g, ' "" ') // double-quoted identifiers
        .replace(/`(?:[^`]|``)*`/g, " `` "); // MySQL backtick identifiers
}

/**
 * Throw if the query is anything other than a single read-only statement.
 * Layer 1 of the read-only guard (see file header).
 */
export function assertReadOnly(query: string): void {
    const stripped = stripForAnalysis(query).trim();
    if (!stripped) throw new Error("empty query");

    // Single statement only — `sql.unsafe` would happily run a stacked
    // `SELECT 1; DROP TABLE t`, so reject multiple statements outright.
    const statements = stripped
        .split(";")
        .map((s) => s.trim())
        .filter((s) => s.length > 0);
    if (statements.length > 1) {
        throw new Error("only a single SQL statement is allowed");
    }

    // Leading keyword (skip any wrapping parentheses, e.g. `(SELECT ...)`).
    const leadingMatch = /^[(\s]*([a-z]+)/i.exec(stripped);
    const leading = leadingMatch?.[1]?.toLowerCase();
    if (!leading || !ALLOWED_LEADING.has(leading)) {
        throw new Error(ERROR_MESSAGE);
    }

    if (FORBIDDEN_RE.test(stripped) || INTO_FILE_RE.test(stripped)) {
        throw new Error(ERROR_MESSAGE);
    }
}
