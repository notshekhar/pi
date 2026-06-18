/**
 * /datasource panel: an interactive list of database connections for the
 * data-analyst agent's `sql` tool. Selecting a connection (or "+ new") opens a
 * single-page form — every field is a row you can edit in place, with test /
 * save / delete on the same page — so fixing one wrong value never means
 * re-walking the whole prompt chain. Configs persist in ~/.loop/datasources.json.
 */
import type { SelectItem } from "@notshekhar/loop-tui";
import chalk from "chalk";
import {
    MAX_DATASOURCES,
    closePool,
    datasourceExists,
    deleteDatasource,
    getDatasource,
    isValidConnectionId,
    listDatasources,
    saveDatasource,
    testConnection,
    type CommandContext,
    type DataSourceConfig,
    type DataSourceType,
} from "@notshekhar/loop-core";
import type { AppDeps } from "../deps";
import type { AppState } from "../state";

type DatasourceHandlers = Pick<CommandContext, "manageDatasources">;

const DEFAULT_PORT: Record<DataSourceType, number> = {
    postgres: 5432,
    redshift: 5439,
    mysql: 3306,
};

function defaultConfig(): DataSourceConfig {
    return { type: "postgres", host: "localhost", port: DEFAULT_PORT.postgres, database: "", user: "", ssl: false };
}

function missingField(cfg: DataSourceConfig): string | null {
    if (!cfg.host.trim()) return "host is required";
    if (!cfg.database.trim()) return "database is required";
    if (!cfg.user.trim()) return "user is required";
    if (!Number.isFinite(cfg.port) || cfg.port <= 0) return "port must be a positive number";
    return null;
}

export function createDatasourceHandlers(_state: AppState, deps: AppDeps): DatasourceHandlers {
    const { tui, history, selectOnce, promptOnce } = deps;

    function describe(cfg: DataSourceConfig): string {
        const secret = cfg.password ? "" : " · no password";
        return `${cfg.type} · ${cfg.user || "?"}@${cfg.host || "?"}:${cfg.port}/${cfg.database || "?"}${cfg.ssl ? " · ssl" : ""}${secret}`;
    }

    function passwordLabel(password: string | undefined): string {
        if (!password) return "(none)";
        if (password.startsWith("${env:")) return password; // keep placeholders visible
        return "(set)";
    }

    async function runTest(cfg: DataSourceConfig): Promise<void> {
        history.addSystem(chalk.dim(`testing ${cfg.type} ${cfg.host}:${cfg.port}/${cfg.database}…`));
        tui.requestRender();
        const result = await testConnection(cfg);
        history.addSystem(result.ok ? chalk.green("connection ok") : chalk.red(`connection failed: ${result.error}`));
        tui.requestRender();
    }

    async function editType(cfg: DataSourceConfig): Promise<void> {
        const pick = await selectOnce(
            [
                { value: "postgres", label: "postgres", description: "PostgreSQL" },
                { value: "mysql", label: "mysql", description: "MySQL / MariaDB" },
                {
                    value: "redshift",
                    label: "redshift",
                    description: "Amazon Redshift (postgres-compatible, TLS required)",
                },
            ],
            "Datasource type",
        );
        if (!pick) return;
        const next = pick.value as DataSourceType;
        // Bump the port to the new type's default only if it's still the old default.
        if (cfg.port === DEFAULT_PORT[cfg.type]) cfg.port = DEFAULT_PORT[next];
        cfg.type = next;
    }

    async function editPort(cfg: DataSourceConfig): Promise<void> {
        const raw = (await promptOnce("port", String(cfg.port))).trim();
        const port = Number.parseInt(raw, 10);
        if (!Number.isFinite(port) || port <= 0) {
            history.addSystem(chalk.red(`invalid port: ${raw}`));
            tui.requestRender();
            return;
        }
        cfg.port = port;
    }

    /**
     * Single-page form. `isNew` controls the available actions (no delete when
     * creating). Saves / deletes inline; returns when the form is dismissed.
     */
    async function editForm(id: string, initial: DataSourceConfig, isNew: boolean): Promise<void> {
        const cfg: DataSourceConfig = { ...initial };
        while (true) {
            const fields: SelectItem[] = [
                { value: "type", label: `type      ${cfg.type}`, description: "postgres / mysql / redshift" },
                { value: "host", label: `host      ${cfg.host || "—"}` },
                { value: "port", label: `port      ${cfg.port}` },
                { value: "database", label: `database  ${cfg.database || "—"}` },
                { value: "user", label: `user      ${cfg.user || "—"}` },
                { value: "password", label: `password  ${passwordLabel(cfg.password)}` },
                { value: "ssl", label: `ssl       ${cfg.ssl ? "on" : "off"}`, description: "toggle TLS" },
            ];
            const actions: SelectItem[] = [
                { value: "test", label: "test connection", description: "verify it connects (stays here to retry)" },
                { value: "save", label: "save", description: describe(cfg) },
            ];
            if (!isNew) actions.push({ value: "delete", label: "delete", description: "remove this connection" });
            actions.push({ value: "cancel", label: "cancel", description: "discard changes" });

            const pick = await selectOnce([...fields, ...actions], `${id} — edit (Esc to cancel)`);
            if (!pick || pick.value === "cancel") return;

            switch (pick.value) {
                case "type":
                    await editType(cfg);
                    break;
                case "host":
                    cfg.host = (await promptOnce("host", cfg.host)).trim();
                    break;
                case "port":
                    await editPort(cfg);
                    break;
                case "database":
                    cfg.database = (await promptOnce("database", cfg.database)).trim();
                    break;
                case "user":
                    cfg.user = (await promptOnce("user", cfg.user)).trim();
                    break;
                case "password":
                    cfg.password =
                        (await promptOnce("password (blank for none, or ${env:VAR})", cfg.password ?? "")).trim() ||
                        undefined;
                    break;
                case "ssl":
                    cfg.ssl = !cfg.ssl;
                    break;
                case "test":
                    // Fire-and-forget so the form re-opens immediately instead
                    // of vanishing while the connect (up to 15s) runs — the
                    // result streams into the history above the still-open form.
                    // Snapshot cfg so an in-flight test ignores later edits.
                    void runTest({ ...cfg });
                    break;
                case "save": {
                    const err = missingField(cfg);
                    if (err) {
                        history.addSystem(chalk.red(err));
                        tui.requestRender();
                        break;
                    }
                    saveDatasource(id, cfg);
                    // Drop any stale pool so the next query reconnects with new config.
                    await closePool(id);
                    history.addSystem(`datasource "${id}" saved — ${describe(cfg)}`);
                    tui.requestRender();
                    return;
                }
                case "delete": {
                    deleteDatasource(id);
                    await closePool(id);
                    history.addSystem(`datasource "${id}" deleted`);
                    tui.requestRender();
                    return;
                }
            }
        }
    }

    async function newDatasource(): Promise<void> {
        if (listDatasources().length >= MAX_DATASOURCES) {
            history.addSystem(chalk.red(`maximum ${MAX_DATASOURCES} datasources reached — delete one first`));
            tui.requestRender();
            return;
        }
        const id = (await promptOnce("connection id (e.g. warehouse)")).trim();
        if (!id) return;
        if (!isValidConnectionId(id)) {
            history.addSystem(chalk.red(`invalid id: ${id} (alphanumeric, dashes, ≤32 chars)`));
            tui.requestRender();
            return;
        }
        if (datasourceExists(id)) {
            history.addSystem(chalk.red(`"${id}" already exists`));
            tui.requestRender();
            return;
        }
        await editForm(id, defaultConfig(), true);
    }

    return {
        async manageDatasources() {
            // Loop so Esc in the form returns to the list (like /agents, /mcp).
            while (true) {
                const datasources = listDatasources();
                const items: SelectItem[] = [
                    {
                        value: "+new",
                        label: "+ new datasource",
                        description: "add a postgres / mysql / redshift connection",
                    },
                    ...datasources.map(({ id, config }) => ({
                        value: id,
                        label: id,
                        description: describe(config),
                    })),
                ];
                const pick = await selectOnce(items, "Datasources (Esc to close)");
                if (!pick) return;
                if (pick.value === "+new") {
                    await newDatasource();
                } else {
                    const cfg = getDatasource(pick.value);
                    if (cfg) await editForm(pick.value, cfg, false);
                }
            }
        },
    };
}
