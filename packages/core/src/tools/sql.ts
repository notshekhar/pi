/**
 * `sql` tool — read-only SQL access for the data-analyst agent. Takes a saved
 * datasource connectionId and a query, runs it through the two-layer read-only
 * guard (see datasources/validate.ts + client.ts), and returns the rows.
 *
 * Not part of createTools()/TOOL_NAMES and not in AGENT_TOOL_NAMES: it is wired
 * into a turn only for an agent whose fixed tool set opts in (the data-analyst
 * built-in), so no other agent can ever reach it. Any failure (a rejected
 * mutation, an unknown connection, a database error) throws, surfacing as a
 * tool-error.
 */
import { tool } from "ai";
import { z } from "zod";
import { runReadOnlyQuery } from "../datasources/client";
import { listDatasources } from "../datasources/config";

export interface SqlToolContext {
    abortSignal?: AbortSignal;
}

function knownConnections(): string {
    const ids = listDatasources().map((d) => d.id);
    return ids.length ? ids.join(", ") : "(none configured — add one with /datasource)";
}

/** Enumerate configured connections (id · type · host/db) for the tool description. */
function connectionCatalog(): string {
    const sources = listDatasources();
    if (sources.length === 0) {
        return "No datasources are configured yet — tell the user to add one with /datasource.";
    }
    const lines = sources.map(({ id, config }) => `  - ${id} (${config.type} · ${config.host}/${config.database})`);
    return `Available connectionId values:\n${lines.join("\n")}`;
}

function formatRows(rows: unknown): string {
    if (Array.isArray(rows)) {
        if (rows.length === 0) return "(0 rows)";
        const body = JSON.stringify(rows, null, 2);
        return `${rows.length} row(s):\n${body}`;
    }
    return JSON.stringify(rows ?? null, null, 2);
}

export function createSqlTool(ctx: SqlToolContext) {
    return tool({
        description:
            "Run a READ-ONLY SQL query against a configured datasource and return the rows. " +
            "Only SELECT / WITH / EXPLAIN / SHOW / DESCRIBE statements are allowed — any " +
            "INSERT/UPDATE/DELETE/ALTER/DDL is rejected. Queries run inside a rolled-back " +
            "read-only transaction. Use information_schema (or the dialect's catalog) to " +
            "discover tables and columns before querying; add a LIMIT to exploratory queries.\n\n" +
            connectionCatalog(),
        inputSchema: z.object({
            connectionId: z.string().describe("Id of a datasource configured via /datasource (see the list above)"),
            query: z.string().describe("A single read-only SQL statement"),
        }),
        execute: async ({ connectionId, query }, options) => {
            const signal = options?.abortSignal ?? ctx.abortSignal;
            if (signal?.aborted) throw new Error("Operation aborted");
            if (!connectionId?.trim()) {
                throw new Error(`connectionId is required. Known connections: ${knownConnections()}`);
            }
            const rows = await runReadOnlyQuery(connectionId.trim(), query);
            return formatRows(rows);
        },
    });
}
