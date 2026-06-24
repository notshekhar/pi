/**
 * The public extension SDK surface — the single `api` object an extension's
 * `activate(api)` receives. This is the stable contract third-party extensions
 * compile against; keep it additive. Authoring is pure Bun/TypeScript — no build
 * step (the host transpiles the entry on import) and no Node compat layer.
 */
import type { Tool } from "ai";
import type { LanguageModel } from "ai";
import type { SlashCommand } from "../commands";
import type { LoopSettings } from "../settings";
import type { ModelInfo } from "../types";

/** Current extension API version — bumped on breaking changes to this surface. */
export const EXTENSION_API_VERSION = "0.1.0";

/** The `loop` field of an extension's package.json, plus the npm fields we read. */
export interface ExtensionManifest {
    /** npm package name (the extension's identity). */
    name: string;
    version?: string;
    description?: string;
    /** ESM/main entry (TS allowed). Falls back to `module` → `main` → index.ts. */
    module?: string;
    main?: string;
    loop?: {
        /** Override the entry the host imports. */
        entry?: string;
        /** Friendly name for the /extensions panel. */
        displayName?: string;
        /** Required host API range, semver (e.g. "^0.1"). */
        engines?: { loop?: string };
        /** Declared capabilities, shown to the user (advisory in v1). */
        permissions?: string[];
    };
}

/**
 * What is running right now — handed to turn/tool hooks so an extension can scope
 * its behavior (which agent, model, tool, step). Built once per turn in runTurn.
 */
export interface TurnContext {
    sessionId: string;
    transcriptPath: string;
    cwd: string;
    /** "default" | "plan" | a custom/extension agent. */
    agent: string;
    modelId: string;
    provider: string;
    model: string;
    /** Tool names available this turn (after agent filter + extensions). */
    tools: string[];
    /** True inside a task subagent run. */
    isSubagent: boolean;
}

/** TurnContext plus the live tool call, for tool-level hooks. */
export interface ToolCallContext extends TurnContext {
    toolName: string;
    toolCallId?: string;
    input: unknown;
    signal?: AbortSignal;
}

/**
 * Runs before a matched tool executes — intercept or rewrite its arguments, or
 * block the call. Return a replacement input object to rewrite the call, `false`
 * to block it (the model gets an error result and can react), or void/undefined
 * to leave the input unchanged. Runs after Claude-compatible PreToolUse hooks,
 * so the two compose. Targets a specific tool via the `match` argument (e.g.
 * `onCall("bash", …)`); `ctx.toolName` is also available.
 */
export type ToolCallMiddleware = (
    input: unknown,
    ctx: ToolCallContext,
) => unknown | false | void | Promise<unknown | false | void>;

/**
 * Runs after a tool executes; can append to or transform its text result.
 * Return new text to replace the result, or void/undefined to leave it as-is.
 * This is the seam LSP/linters/formatters/test-runners hook (e.g. append a
 * `<diagnostics>` block after `write`/`edit`). The ctx tells the extension which
 * agent/model/tool produced the result.
 */
export type ToolResultMiddleware = (
    result: string,
    ctx: ToolCallContext,
) => string | void | Promise<string | void>;

/** SDK families loop knows how to drive declaratively (Vercel AI SDK). */
export type ProviderSdk = "openai" | "anthropic" | "google" | "openai-compatible";

/** One model exposed by a provider. Missing fields get sane catalog defaults. */
export interface ProviderModelSpec {
    /** Id within the provider — addressed as `<providerId>/<id>`. */
    id: string;
    name?: string;
    contextWindow?: number;
    maxOutput?: number;
    reasoning?: boolean;
    /** Input modalities, e.g. ["text", "image"]. Defaults to ["text"]. */
    modalities?: string[];
    cost?: { input?: number; output?: number; cacheRead?: number; cacheWrite?: number };
}

/** How a provider authenticates — drives `loop login` + key resolution. */
export interface ProviderAuth {
    mode: "apikey" | "oauth" | "none";
    /** Fallback env var read when no key is stored/configured. */
    envVar?: string;
    /** Where the user obtains a key (shown in login UI). */
    loginUrl?: string;
}

/** Runtime passed to an imperative provider's getModel. */
export interface ProviderRuntime {
    /** Resolved key (config → loop auth store → env), if any. */
    apiKey?: string;
    fetch: typeof fetch;
}

/**
 * A provider plugin. Two flavors (inspired by pi-mono's `registerProvider`):
 *
 * - **Declarative** — give `sdk` + `baseURL` (+ `apiKey`/`headers`) + `models`,
 *   and loop builds the ai-sdk model with its existing custom-provider machinery.
 *   Covers OpenAI/Anthropic/Google-compatible endpoints.
 * - **Imperative** — supply `getModel` (and optionally `listModels`) for full
 *   control: custom auth, fetch, or a bespoke SDK the extension imports itself.
 *   `getModel`, when present, takes precedence over the declarative fields.
 */
export interface ProviderPlugin {
    /** Provider id — the `<id>` in `<id>/<model>`. Must be unique. */
    id: string;
    name?: string;
    auth?: ProviderAuth;
    // Declarative:
    sdk?: ProviderSdk;
    baseURL?: string;
    /** Literal, or `$ENV` / `${ENV}` interpolation. */
    apiKey?: string;
    headers?: Record<string, string>;
    models?: ProviderModelSpec[];
    // Imperative (overrides declarative when present):
    getModel?(modelId: string, ctx: ProviderRuntime): LanguageModel | Promise<LanguageModel>;
    listModels?(): ProviderModelSpec[] | Promise<ProviderModelSpec[]>;
}

/** An agent plugin: a named system prompt + optional tool allowlist. */
export interface AgentPlugin {
    name: string;
    description?: string;
    prompt: string;
    /** Tool names this agent may use; omit for all. */
    tools?: string[];
}

/**
 * Per-turn middleware seams, mirroring the assembly points in runTurn. Every
 * seam (except onBeforeTurn, which runs before tools are assembled) receives the
 * full `TurnContext`, so middleware can scope by `ctx.agent` / `ctx.model` —
 * e.g. update the system prompt of one specific agent:
 *
 *   onSystemPrompt(prompt, ctx) {
 *     if (ctx.agent === "plan") return prompt + "\n\nExtra rule for plan…";
 *   }
 */
export interface TurnMiddleware {
    /** Inspect the input, or block the turn by returning false. Runs pre-assembly. */
    onBeforeTurn?(ctx: {
        input: string;
        cwd: string;
        sessionId: string;
        agent: string;
        modelId: string;
    }): boolean | void | Promise<boolean | void>;
    /** Transform the assembled system prompt for the running agent (`ctx.agent`). */
    onSystemPrompt?(prompt: string, ctx: TurnContext): string | void | Promise<string | void>;
    /** Add/remove/wrap tools right before the model call. */
    onAssembleTools?(
        tools: Record<string, Tool>,
        ctx: TurnContext,
    ): Record<string, Tool> | void | Promise<Record<string, Tool> | void>;
    /** Tweak provider options (thinking/caching) before the model call. */
    onProviderOptions?(opts: unknown, ctx: TurnContext): unknown | void;
    /** Observe turn completion. */
    onAfterTurn?(ctx: TurnContext): void | Promise<void>;
}

export interface ExtensionInfo {
    /** The extension's own directory (~/.loop/extensions/<name>). */
    readonly dir: string;
    readonly manifest: ExtensionManifest;
    /** Namespaced logger (prefixes [<name>]). Routed into the chat as a note. */
    readonly log: (...args: unknown[]) => void;
    /**
     * Report a one-line status (e.g. current mode) for the startup banner and
     * `/extensions` panel — so the user can see the extension is active and how.
     * Pass a function; it's read fresh each time. Return undefined to show none.
     */
    readonly setStatus: (fn: () => string | undefined) => void;
}

/**
 * The object handed to `activate(api)`. Every registration is undone
 * automatically when the extension is disabled/uninstalled/reloaded — the host
 * tracks ownership, so an extension never has to clean up its own contributions.
 */
export interface LoopAPI {
    readonly extension: ExtensionInfo;
    readonly version: string;

    commands: {
        register(cmd: SlashCommand): void;
        unregister(name: string): void;
        /** Replace an existing command (override a builtin). */
        override(name: string, cmd: Partial<SlashCommand> & Pick<SlashCommand, "handler">): void;
    };

    tools: {
        add(name: string, tool: Tool): void;
        /** Remove a tool by name (including builtins like `bash`). */
        remove(name: string): void;
        /**
         * Grant a tool to a specific (restricted) agent's allowlist — e.g. let
         * the read-only `plan` agent use a custom tool. `default` already has all
         * tools, so granting to it is a no-op.
         */
        grant(agent: string, tool: string): void;
        /** Intercept/rewrite a matched tool's input, or block it, before it runs. */
        onCall(match: string | string[] | ((name: string) => boolean), mw: ToolCallMiddleware): void;
        onResult(match: string | string[] | ((name: string) => boolean), mw: ToolResultMiddleware): void;
    };

    settings: {
        get<K extends keyof LoopSettings>(key: K): LoopSettings[K];
        set<K extends keyof LoopSettings>(key: K, value: LoopSettings[K]): void;
        /** The extension's own namespaced settings (stored under `ext.<name>`). */
        getOwn<T = unknown>(key: string, fallback?: T): T;
        setOwn<T = unknown>(key: string, value: T): void;
    };

    providers: { register(provider: ProviderPlugin): void; unregister(id: string): void };
    models: { add(...infos: ModelInfo[]): void };
    agents: { register(agent: AgentPlugin): void };
    skills: { addDir(dir: string): void };
    turn: { use(mw: TurnMiddleware): void };
}

/** The shape an extension's entry module must export (default or named). */
export interface ExtensionModule {
    activate?(api: LoopAPI): void | Promise<void>;
    deactivate?(): void | Promise<void>;
}
