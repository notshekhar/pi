/**
 * Datasource configuration for the data-analyst agent's `sql` tool. Connections
 * are stored in ~/.loop/datasources.json (datasourcesStore), keyed by a
 * connectionId the agent passes to the `sql` tool.
 *
 * Passwords are stored as written. `${env:VAR}` placeholders are expanded from
 * the environment at connect time (same convention as MCP server config), so a
 * user who prefers not to keep a plaintext secret on disk can point at an env
 * var instead.
 */
import { datasourcesStore } from "../auth/storage";
import { resolveSecrets } from "../mcp/config";

export type DataSourceType = "postgres" | "mysql" | "redshift";

export interface DataSourceConfig {
    type: DataSourceType;
    host: string;
    port: number;
    database: string;
    user: string;
    /** Plaintext or a `${env:VAR}` placeholder. Optional (e.g. trust/socket auth). */
    password?: string;
    /** Enable TLS. Recommended for redshift and any remote database. */
    ssl?: boolean;
}

/** Hard cap on saved datasources. */
export const MAX_DATASOURCES = 50;

/** Slash/connectionId safe: starts alphanumeric, then alnum/dash/underscore, ≤32 chars. */
export function isValidConnectionId(id: string): boolean {
    return /^[a-z0-9][a-z0-9_-]{0,31}$/i.test(id);
}

function allConnections(): Record<string, DataSourceConfig> {
    return (datasourcesStore.get("connections") as Record<string, DataSourceConfig> | undefined) ?? {};
}

export function listDatasources(): { id: string; config: DataSourceConfig }[] {
    return Object.entries(allConnections())
        .map(([id, config]) => ({ id, config }))
        .sort((a, b) => a.id.localeCompare(b.id));
}

export function getDatasource(id: string): DataSourceConfig | undefined {
    return allConnections()[id];
}

export function datasourceExists(id: string): boolean {
    return id in allConnections();
}

/**
 * Create or overwrite a datasource. Overwriting an existing id is always
 * allowed; creating a new one is blocked once MAX_DATASOURCES is reached.
 */
export function saveDatasource(id: string, config: DataSourceConfig): void {
    if (!isValidConnectionId(id)) throw new Error(`invalid connection id: ${id}`);
    const connections = allConnections();
    const isNew = !(id in connections);
    if (isNew && Object.keys(connections).length >= MAX_DATASOURCES) {
        throw new Error(`maximum ${MAX_DATASOURCES} datasources allowed — delete one first`);
    }
    datasourcesStore.set("connections", { ...connections, [id]: config });
}

export function deleteDatasource(id: string): boolean {
    const connections = allConnections();
    if (!(id in connections)) return false;
    const next = { ...connections };
    delete next[id];
    datasourcesStore.set("connections", next);
    return true;
}

/** Resolve a config's secrets (`${env:VAR}` in the password) for connecting. */
export function resolveDatasourceSecrets(config: DataSourceConfig): DataSourceConfig {
    if (!config.password) return config;
    return { ...config, password: resolveSecrets(config.password) };
}
