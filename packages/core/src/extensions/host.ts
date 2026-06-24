/**
 * The ExtensionHost loads enabled extensions once at startup, hands each an
 * `api` object, and aggregates their contributions for the rest of the app to
 * read (commands, tools, providers, models, agents, skills, turn middleware).
 *
 * Ownership is tracked per extension, so disable/uninstall/reload tears down
 * exactly what an extension added — extensions never clean up after themselves.
 * Loading is in-process dynamic import: the embedded Bun runtime transpiles the
 * extension's TypeScript entry on import (verified to work inside a
 * `bun --compile` binary), and resolves the extension's own node_modules deps.
 */
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { settingsStore } from "../auth/storage";
import type {
    AgentPlugin,
    ExtensionManifest,
    ExtensionModule,
    LoopAPI,
    ProviderPlugin,
    ToolCallMiddleware,
    ToolResultMiddleware,
    TurnMiddleware,
} from "./api";
import { EXTENSION_API_VERSION } from "./api";
import { collectProviderModelInfos } from "./providers";
import { extensionDir, getBuiltinEnabled, listRecords, type ExtensionRecord } from "./store";
import { BUILTIN_EXTENSIONS, getBuiltin, type BuiltinExtension } from "./builtin";

type Tool = unknown; // ai-sdk Tool; kept loose here to avoid a hard ai import in the host
type SlashCommand = import("../commands").SlashCommand;
type ModelInfo = import("../types").ModelInfo;

type CommandOp =
    | { kind: "register"; cmd: SlashCommand }
    | { kind: "override"; name: string; cmd: Partial<SlashCommand> & { handler: SlashCommand["handler"] } }
    | { kind: "unregister"; name: string };

interface Contributions {
    commandOps: CommandOp[];
    tools: Map<string, Tool>;
    toolRemovals: Set<string>;
    toolGrants: { agent: string; tool: string }[];
    toolCallMws: { match: (name: string) => boolean; mw: ToolCallMiddleware }[];
    toolResultMws: { match: (name: string) => boolean; mw: ToolResultMiddleware }[];
    providers: Map<string, ProviderPlugin>;
    modelInfos: ModelInfo[];
    agents: AgentPlugin[];
    skillDirs: string[];
    turnMws: TurnMiddleware[];
}

interface Loaded {
    record: ExtensionRecord;
    manifest: ExtensionManifest;
    pkgDir: string;
    module: ExtensionModule;
    contributions: Contributions;
}

function emptyContributions(): Contributions {
    return {
        commandOps: [],
        tools: new Map(),
        toolRemovals: new Set(),
        toolGrants: [],
        toolCallMws: [],
        toolResultMws: [],
        providers: new Map(),
        modelInfos: [],
        agents: [],
        skillDirs: [],
        turnMws: [],
    };
}

function toMatcher(match: string | string[] | ((name: string) => boolean)): (name: string) => boolean {
    if (typeof match === "function") return match;
    const set = new Set(Array.isArray(match) ? match : [match]);
    return (name: string) => set.has(name);
}

/** Resolve the on-disk directory holding the extension's package + its entry. */
function resolvePkgDir(record: ExtensionRecord): string {
    if (record.linkPath) return record.linkPath;
    const wrapper = extensionDir(record.name);
    // Installed-as-dependency: the package lives under the wrapper's node_modules.
    const wrapperPkg = join(wrapper, "package.json");
    if (existsSync(wrapperPkg)) {
        try {
            const deps = (JSON.parse(readFileSync(wrapperPkg, "utf8")) as { dependencies?: Record<string, string> })
                .dependencies;
            const dep = deps && Object.keys(deps)[0];
            if (dep) return join(wrapper, "node_modules", dep);
        } catch {
            /* fall through */
        }
    }
    return wrapper;
}

function resolveEntry(pkgDir: string, manifest: ExtensionManifest): string {
    const rel = manifest.loop?.entry ?? manifest.module ?? manifest.main ?? "index.ts";
    return join(pkgDir, rel);
}

/** Major-version compat check against the host API version. */
function isCompatible(manifest: ExtensionManifest): boolean {
    const want = manifest.loop?.engines?.loop;
    if (!want) return true; // unspecified = assume compatible
    const wantMajor = want.replace(/^[\^~]/, "").split(".")[0];
    const haveMajor = EXTENSION_API_VERSION.split(".")[0];
    return wantMajor === haveMajor;
}

export class ExtensionHost {
    private loaded = new Map<string, Loaded>();
    /** Per-extension status reporters (api.extension.setStatus), for the banner/panel. */
    private statusFns = new Map<string, () => string | undefined>();
    private initialized = false;
    private warnings: string[] = [];

    /** Load every enabled extension. Safe to call once per session. */
    async init(): Promise<void> {
        if (this.initialized) return;
        this.initialized = true;
        // Built-in (bundled) extensions first, so an external install can still
        // override/extend afterwards. Each is opt-in (default disabled).
        for (const b of BUILTIN_EXTENSIONS) {
            if (!getBuiltinEnabled(b.name, b.defaultEnabled)) continue;
            try {
                await this.loadBuiltin(b);
            } catch (err) {
                this.warnings.push(`built-in extension "${b.name}" failed to load: ${(err as Error).message}`);
            }
        }
        for (const record of listRecords()) {
            if (!record.enabled) continue;
            try {
                await this.loadOne(record);
            } catch (err) {
                this.warnings.push(`extension "${record.name}" failed to load: ${(err as Error).message}`);
            }
        }
    }

    /** Activate a bundled extension from its statically-imported module. */
    private async loadBuiltin(b: BuiltinExtension): Promise<void> {
        const record: ExtensionRecord = {
            name: b.name,
            version: "built-in",
            source: "built-in",
            sourceKind: "builtin",
            enabled: true,
            installedAt: 0,
        };
        const manifest: ExtensionManifest = { name: b.name, loop: { displayName: b.displayName } };
        const contributions = emptyContributions();
        const api = this.makeApi(record, manifest, "", contributions);
        await b.module.activate?.(api);
        this.loaded.set(b.name, { record, manifest, pkgDir: "", module: b.module, contributions });
    }

    private async loadOne(record: ExtensionRecord): Promise<void> {
        const pkgDir = resolvePkgDir(record);
        const pkgJsonPath = join(pkgDir, "package.json");
        if (!existsSync(pkgJsonPath)) throw new Error(`missing package.json at ${pkgDir}`);
        const manifest = JSON.parse(readFileSync(pkgJsonPath, "utf8")) as ExtensionManifest;
        if (!isCompatible(manifest)) {
            throw new Error(
                `requires loop API ${manifest.loop?.engines?.loop}, host is ${EXTENSION_API_VERSION}`,
            );
        }
        const entry = resolveEntry(pkgDir, manifest);
        if (!existsSync(entry)) throw new Error(`entry not found: ${entry}`);

        const imported = (await import(entry)) as { default?: ExtensionModule } & ExtensionModule;
        const module: ExtensionModule = imported.default ?? imported;
        const contributions = emptyContributions();
        const api = this.makeApi(record, manifest, pkgDir, contributions);
        await module.activate?.(api);

        this.loaded.set(record.name, { record, manifest, pkgDir, module, contributions });
    }

    private makeApi(
        record: ExtensionRecord,
        manifest: ExtensionManifest,
        pkgDir: string,
        c: Contributions,
    ): LoopAPI {
        // Info-level: routes to the chat as a dim system line, not a red error.
        const log = (...args: unknown[]) => console.log(`[${record.name}]`, ...args);
        // Per-extension settings live under one un-dotted top-level key as a
        // nested object: settingsStore uses dot-prop for `.set`, so a dotted key
        // would write a nested path that flat reads can't see. Read-modify-write
        // the whole bag keeps it consistent with CachedStore's flat get.
        const OWN = "extensionSettings";
        type Bag = Record<string, Record<string, unknown>>;
        const readOwn = (k: string) => ((settingsStore.get(OWN) as Bag)?.[record.name] ?? {})[k];
        const writeOwn = (k: string, v: unknown) => {
            const bag = { ...((settingsStore.get(OWN) as Bag) ?? {}) };
            bag[record.name] = { ...(bag[record.name] ?? {}), [k]: v };
            settingsStore.set(OWN, bag);
        };
        return {
            version: EXTENSION_API_VERSION,
            extension: {
                dir: pkgDir,
                manifest,
                log,
                setStatus: (fn) => this.statusFns.set(record.name, fn),
            },
            commands: {
                register: (cmd) => c.commandOps.push({ kind: "register", cmd }),
                unregister: (name) => c.commandOps.push({ kind: "unregister", name }),
                override: (name, cmd) => c.commandOps.push({ kind: "override", name, cmd }),
            },
            tools: {
                add: (name, tool) => c.tools.set(name, tool),
                remove: (name) => c.toolRemovals.add(name),
                grant: (agent, tool) => c.toolGrants.push({ agent, tool }),
                onCall: (match, mw) => c.toolCallMws.push({ match: toMatcher(match), mw }),
                onResult: (match, mw) => c.toolResultMws.push({ match: toMatcher(match), mw }),
            },
            settings: {
                get: (key) => settingsStore.get(key as string) as never,
                set: (key, value) => settingsStore.set(key as string, value),
                getOwn: ((key: string, fallback?: unknown) => {
                    const v = readOwn(key);
                    return (v === undefined ? fallback : v) as never;
                }) as LoopAPI["settings"]["getOwn"],
                setOwn: ((key: string, value: unknown) => writeOwn(key, value)) as never,
            },
            providers: {
                register: (provider) => c.providers.set(provider.id, provider),
                unregister: (id) => c.providers.delete(id),
            },
            models: { add: (...infos) => c.modelInfos.push(...infos) },
            agents: { register: (agent) => c.agents.push(agent) },
            skills: { addDir: (dir) => c.skillDirs.push(dir) },
            turn: { use: (mw) => c.turnMws.push(mw) },
        };
    }

    /** Unload one extension, running its deactivate and dropping its contributions. */
    async unload(name: string): Promise<void> {
        const l = this.loaded.get(name);
        if (!l) return;
        try {
            await l.module.deactivate?.();
        } catch (err) {
            this.warnings.push(`extension "${name}" deactivate threw: ${(err as Error).message}`);
        }
        this.loaded.delete(name);
        this.statusFns.delete(name);
    }

    /** Reload one extension (pick up edits / re-enable). Handles built-ins too. */
    async reload(name: string): Promise<void> {
        await this.unload(name);
        const builtin = getBuiltin(name);
        if (builtin) {
            if (getBuiltinEnabled(builtin.name, builtin.defaultEnabled)) await this.loadBuiltin(builtin);
            return;
        }
        const record = listRecords().find((r) => r.name === name);
        if (record?.enabled) await this.loadOne(record);
    }

    /** Deactivate every loaded extension (app shutdown). Safe to re-init after. */
    async close(): Promise<void> {
        for (const name of [...this.loaded.keys()]) await this.unload(name);
        this.initialized = false;
    }

    // ---- aggregate getters (consumed across the app) ----

    /** Apply every extension's command ops to a registry (after builtins). */
    applyCommands(reg: import("../commands").CommandRegistry): void {
        for (const l of this.loaded.values()) {
            for (const op of l.contributions.commandOps) {
                if (op.kind === "register") reg.register(op.cmd);
                else if (op.kind === "unregister") reg.unregister(op.name);
                else reg.register({ name: op.name, description: op.cmd.description ?? op.name, handler: op.cmd.handler });
            }
        }
    }

    /** Extension-added tools, minus any an extension asked to remove. */
    getTools(): { add: Map<string, Tool>; remove: Set<string> } {
        const add = new Map<string, Tool>();
        const remove = new Set<string>();
        for (const l of this.loaded.values()) {
            for (const [k, v] of l.contributions.tools) add.set(k, v);
            for (const r of l.contributions.toolRemovals) remove.add(r);
        }
        return { add, remove };
    }

    getToolCallMiddleware(): { match: (name: string) => boolean; mw: ToolCallMiddleware }[] {
        return [...this.loaded.values()].flatMap((l) => l.contributions.toolCallMws);
    }

    getToolResultMiddleware(): { match: (name: string) => boolean; mw: ToolResultMiddleware }[] {
        return [...this.loaded.values()].flatMap((l) => l.contributions.toolResultMws);
    }

    /** Tool names extensions granted to a specific agent's allowlist. */
    getToolGrants(agent: string): string[] {
        const out = new Set<string>();
        for (const l of this.loaded.values()) {
            for (const g of l.contributions.toolGrants) if (g.agent === agent) out.add(g.tool);
        }
        return [...out];
    }

    getProvider(id: string): ProviderPlugin | undefined {
        for (const l of this.loaded.values()) {
            const p = l.contributions.providers.get(id);
            if (p) return p;
        }
        return undefined;
    }

    getModelInfos(): ModelInfo[] {
        return [...this.loaded.values()].flatMap((l) => l.contributions.modelInfos);
    }

    /** All registered provider plugins (last registration of an id wins). */
    private allProviders(): Map<string, ProviderPlugin> {
        const merged = new Map<string, ProviderPlugin>();
        for (const l of this.loaded.values()) {
            for (const [id, p] of l.contributions.providers) merged.set(id, p);
        }
        return merged;
    }

    /** Catalog entries from provider declarative models + direct model adds. */
    getProviderModelInfos(): ModelInfo[] {
        return collectProviderModelInfos(this.allProviders().values(), this.getModelInfos());
    }

    /** Login/picker descriptors for registered providers (id, name, auth). */
    getProviderDescriptors(): { id: string; name: string; auth?: ProviderPlugin["auth"] }[] {
        return [...this.allProviders().values()].map((p) => ({ id: p.id, name: p.name ?? p.id, auth: p.auth }));
    }

    getAgents(): AgentPlugin[] {
        return [...this.loaded.values()].flatMap((l) => l.contributions.agents);
    }

    getSkillDirs(): string[] {
        return [...this.loaded.values()].flatMap((l) => l.contributions.skillDirs);
    }

    getTurnMiddleware(): TurnMiddleware[] {
        return [...this.loaded.values()].flatMap((l) => l.contributions.turnMws);
    }

    /**
     * One line per currently-loaded extension for the startup banner / panel:
     * "displayName — status" (status from api.extension.setStatus, if set).
     */
    activeStatuses(): string[] {
        const out: string[] = [];
        for (const name of this.loaded.keys()) {
            let status: string | undefined;
            try {
                status = this.statusFns.get(name)?.();
            } catch {
                status = undefined;
            }
            // Short name + terse status, e.g. "ponytail (full)" — stays compact
            // even with many extensions loaded.
            out.push(status ? `${name} (${status})` : name);
        }
        return out;
    }

    getWarnings(): string[] {
        return this.warnings;
    }

    isLoaded(name: string): boolean {
        return this.loaded.has(name);
    }

    /** Unified list for the /extensions panel: built-ins + installed externals. */
    listAll(): {
        name: string;
        displayName: string;
        description?: string;
        enabled: boolean;
        builtin: boolean;
        version?: string;
        source?: string;
        linkPath?: string;
    }[] {
        const out = BUILTIN_EXTENSIONS.map((b) => ({
            name: b.name,
            displayName: b.displayName,
            description: b.description,
            enabled: getBuiltinEnabled(b.name, b.defaultEnabled),
            builtin: true,
        }));
        for (const r of listRecords()) {
            out.push({
                name: r.name,
                displayName: r.name,
                description: undefined,
                enabled: r.enabled,
                builtin: false,
                version: r.version,
                source: r.source,
                linkPath: r.linkPath,
            } as never);
        }
        return out;
    }
}

let singleton: ExtensionHost | undefined;

export function getExtensionHost(): ExtensionHost {
    if (!singleton) singleton = new ExtensionHost();
    return singleton;
}
