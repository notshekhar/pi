# Loop Extensions — Design

Everything in loop is extendable by user-written or installed JavaScript:
settings, slash commands, tools, **providers and the models inside them**,
agents, skills, and the turn loop itself — add, remove, or override any of it.
Extensions may carry their own npm dependencies. The runtime is Bun-only (the
loop binary ships the Bun runtime); there is no Node-compat layer to worry about.

No GUI for now — management is `loop <verb>` from the shell plus the
`/extensions` panel in-session.

---

## 0. Proven foundations

Three Bun facts (verified against a real `bun build --compile` binary) make the
whole design work in-process, with no second binary shipped:

1. **Runtime dynamic import in a compiled binary** — the embedded Bun runtime
   transpiles and `import()`s an external `.ts` file at runtime. Extensions need
   no build step.
2. **External `node_modules` resolution** — that imported file resolves its own
   dependencies from its own `node_modules`. Extensions get real npm deps.
3. **`BUN_BE_BUN=1 <loop> install`** — a compiled binary run with this env var
   behaves as the full `bun` CLI, so `loop install` runs a real `bun add` using
   the runtime already inside the binary.

---

## 1. Anatomy of an extension

An extension is an npm-publishable package (or a local folder, or a GitHub repo):

```
my-loop-ext/
  package.json
  index.ts            # entry — export default { activate, deactivate? }
  node_modules/       # its own deps, installed by the embedded bun
```

`package.json` carries a `loop` field plus standard npm fields:

```jsonc
{
    "name": "loop-ext-myvendor",
    "version": "1.2.0",
    "type": "module",
    "dependencies": { "@ai-sdk/openai-compatible": "^1" },
    "loop": {
        "entry": "./index.ts", // default: module → main → index.ts
        "displayName": "My Vendor",
        "engines": { "loop": "^0.3" }, // host API range (major checked; minor too while 0.x)
        "permissions": ["net", "fs"], // advisory in v1
    },
}
```

The entry default-exports an `ExtensionModule`:

```ts
export default {
    activate(api) {
        /* register contributions */
    },
    deactivate() {
        /* optional; host already tears down contributions */
    },
};
```

Every registration an extension makes is **owned** by that extension. Disable,
uninstall, or reload tears down exactly what it added — extensions never have to
unwind their own contributions.

---

## 2. Install, sources, storage

`loop install <spec>` accepts three source kinds:

| Spec                                                               | Kind   | Resolution                                        |
| ------------------------------------------------------------------ | ------ | ------------------------------------------------- |
| `camelcase`, `@scope/pkg@1.2`                                      | npm    | `bun add <spec>` in a wrapper dir                 |
| `github:owner/repo`, `https://github.com/owner/repo`, `owner/repo` | github | `bun add github:…` (pulls transitive deps)        |
| `./path`, `/abs`, `file:…`                                         | local  | `loop link` — loaded in place after `bun install` |

**Install model (wrapper dir):** each non-local extension gets
`~/.loop/extensions/<name>/`, whose `package.json` declares the extension as its
single dependency. `bun add` pulls the extension **and its transitive deps**
into that dir's `node_modules`; the entry resolves from
`node_modules/<pkg>`. `loop link` instead records a `linkPath` and loads the
local folder directly (dev workflow).

`~/.loop/extensions.json` is the registry: one record per extension — source,
resolved name/version, `enabled`, optional `linkPath`.

Shell verbs: `loop install` · `loop link` · `loop remove`/`uninstall` ·
`loop extensions` (list) · `loop enable`/`disable`. Bare `loop install` repairs
deps for all installed extensions. In-session: the `/extensions` panel +
`/install <spec>`.

---

## 3. The `LoopAPI` surface

One object handed to `activate(api)`. Stable, additive contract.

| Group           | Methods                                                                               | Notes                                                                                                                                                                                                                                                                                                                                 |
| --------------- | ------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `api.commands`  | `register` · `unregister` · `override`                                                | add/remove/replace any slash command, incl. builtins                                                                                                                                                                                                                                                                                  |
| `api.tools`     | `add` · `remove` · `grant(agent, tool)` · `onCall(match, mw)` · `onResult(match, mw)` | add/remove any tool (default gets it automatically); `grant` lets a restricted agent use it; `onCall` rewrites/blocks a matched tool's input pre-exec (e.g. sanitize `bash`); `onResult` transforms its result post-exec (LSP/linters/formatters). Both target a specific tool via `match` and receive `ToolCallContext` — **see §5** |
| `api.settings`  | `get`/`set` (core keys) · `getOwn`/`setOwn` (namespaced)                              | own keys stored under one `extensionSettings` bag                                                                                                                                                                                                                                                                                     |
| `api.providers` | `register(provider)`                                                                  | **see §4**                                                                                                                                                                                                                                                                                                                            |
| `api.models`    | `add(...ModelInfo)`                                                                   | contribute catalog entries directly                                                                                                                                                                                                                                                                                                   |
| `api.agents`    | `register(agent)`                                                                     | named agent; omit `tools` → all tools, supply `tools` → restricted (may name extension tools) — **see §5**                                                                                                                                                                                                                            |
| `api.skills`    | `addDir(dir)`                                                                         | contribute a skills directory                                                                                                                                                                                                                                                                                                         |
| `api.turn`      | `use(middleware)`                                                                     | **see §5**                                                                                                                                                                                                                                                                                                                            |
| `api.extension` | `dir` · `manifest` · `log`                                                            | the extension's own context                                                                                                                                                                                                                                                                                                           |

---

## 4. Providers and models (the centerpiece)

A provider extension adds a **new provider id** and the **models inside it**, so
they appear in `/model`, price correctly, authenticate via `loop login`, and run
through `runTurn` like any builtin. Two flavors, because most providers are
OpenAI-compatible but some need bespoke wiring.

### 4.1 Declarative (covers ~90%)

Reuses loop's existing custom-provider machinery (`customModel`,
`normalizeBaseURL`, `customFetch` in `providers/index.ts`). The author supplies
an SDK kind, a base URL, auth, and a model list:

```ts
api.providers.register({
    id: "myvendor",
    name: "My Vendor",
    sdk: "openai-compatible", // "openai" | "anthropic" | "google" | "openai-compatible"
    baseURL: "https://api.myvendor.com/v1",
    auth: { mode: "apikey", envVar: "MYVENDOR_API_KEY", loginUrl: "https://myvendor.com/keys" },
    models: [
        {
            id: "fast-1",
            name: "Fast 1",
            contextWindow: 128_000,
            maxOutput: 16_000,
            reasoning: false,
            modalities: ["text"],
            cost: { input: 0.2, output: 0.6, cacheRead: 0.05, cacheWrite: 0.25 },
        },
        {
            id: "think-1",
            name: "Think 1",
            contextWindow: 256_000,
            maxOutput: 32_000,
            reasoning: true,
            modalities: ["text", "image"],
            cost: { input: 1.0, output: 3.0, cacheRead: 0.1, cacheWrite: 1.25 },
        },
    ],
});
```

Models are addressed as `myvendor/fast-1`.

### 4.2 Imperative (full control)

For custom auth, custom `fetch`, OAuth, or a non-standard SDK, supply `getModel`
directly and return an ai-sdk `LanguageModel`:

```ts
api.providers.register({
    id: "myvendor",
    name: "My Vendor",
    auth: { mode: "oauth" },
    async getModel(modelId, ctx) {
        const { createOpenAI } = await import("@ai-sdk/openai"); // extension's own dep
        return createOpenAI({ apiKey: ctx.apiKey, baseURL: "…", fetch: ctx.fetch })(modelId);
    },
    async listModels() {
        /* fetch /models, map to ModelInfo[] */
    },
});
```

`ctx: ProviderRuntime` carries the resolved `apiKey` (from loop's auth store /
env) and a `fetch` helper.

### 4.3 How it threads through (seams)

- **Model construction** — `providers/getModel(fullId)` (exported as
  `getLanguageModel`) gains one branch _before_ the builtin switch: if the host
  has a provider for the parsed id, use it (declarative → reuse `customModel`;
  imperative → call its `getModel`). Order: `custom:` config → host provider →
  builtin switch.
- **Catalog / pricing** — `getCatalog()` merges `host.getModelInfos()` (from
  `models.add` + each provider's `listModels`). Mirrors the existing
  `addCustomModel`/`listCustomModelIds` precedent, so cost tracking, context
  windows, reasoning/modality flags all "just work" in the footer and picker.
- **Auth** — the auth store is already generic over arbitrary provider ids
  (`getApiKey(provider: ProviderId)`, `ProviderId = BuiltinProviderId | (string
& {})`). So `loop login myvendor` (apikey) stores and `getApiKey("myvendor")`
  retrieves with zero new storage. Provider extensions just need to appear in the
  login provider list (today `PROVIDER_IDS` in `login-flow.ts`) — the host will
  expose registered provider ids + their `auth` descriptor so the picker and
  `loop login <id>` include them. OAuth providers use the imperative flavor and
  drive their own flow in `activate`/a command.
- **Picker & availability** — `/model` and `provider-availability.ts` read the
  catalog + authorized providers; extension providers show as available once they
  have creds (or an env var, or `auth.mode === "none"`).

### 4.4 `ProviderPlugin` (target shape, P3)

```ts
interface ProviderPlugin {
    id: string;
    name?: string;
    auth?: { mode: "apikey" | "oauth" | "none"; envVar?: string; loginUrl?: string };
    // Declarative:
    sdk?: "openai" | "anthropic" | "google" | "openai-compatible";
    baseURL?: string;
    headers?: Record<string, string>;
    models?: ModelInfo[];
    // Imperative (overrides declarative if present):
    getModel?(modelId: string, ctx: ProviderRuntime): LanguageModel | Promise<LanguageModel>;
    listModels?(): ModelInfo[] | Promise<ModelInfo[]>;
}
interface ProviderRuntime {
    apiKey?: string;
    fetch: typeof fetch;
}
```

(The current `api.ts` has a minimal `ProviderPlugin`; P3 expands it to the
above.)

---

## 5. Tools × agents access model

Tools register into **one global tool registry** (builtins + extension tools),
independent of any agent. Which agent gets which tool is then decided exactly as
it is today (`agents.ts`):

- **`default` agent gets EVERY tool, always** — `tools: undefined` means "all",
  so the moment an extension registers a tool it is available to default. The
  author does **not** have to "give it to default"; default has all tools by
  definition. This is the headline rule.
- **Restricted agents** (`plan`, `data-analyst`, custom agents with a
  `tools:` allowlist) get **only what they list**. Extension tools reach them
  only if named.
- **Subagents** inherit the spawning turn's tools, so extension tools flow down.
- An extension tool that nobody lists is still **registered** (visible, usable by
  default, namespaced) — it is never lost just because no restricted agent opted
  in.

Two capabilities this requires (both are gaps in the current code, fixed in P5):

1. **Allowlists must accept extension tool names.** Today an agent's `tools:`
   line is validated against a fixed `AGENT_TOOL_NAMES` (`TOOL_NAMES + task`), so
   a restricted/custom agent naming `myext_tool` would have it silently dropped.
   `AGENT_TOOL_NAMES` must become dynamic = builtin names + registered extension
   tool names.
2. **An extension can grant a specific tool to a specific agent.** Beyond
   `tools.add`, the API gets a targeted grant so an extension can extend a
   restricted agent's allowlist without the user editing the agent file:

```ts
api.tools.add("myext_review", reviewTool); // registered; default has it automatically
api.tools.grant("plan", "myext_review"); // also let the read-only plan agent use it
```

**Agents via extensions.** `api.agents.register({ name, prompt, tools? })` adds a
named agent (merged into `listAgents()`, gets a `/<name>` command like any
other). Omit `tools` → that agent gets all tools (like default); supply a
`tools` allowlist → restricted, and the allowlist may name extension tools
(capability #1 above). So an extension can ship _both_ a tool and an agent wired
to use it.

---

## 6. Turn & tool context

Every middleware and tool hook receives a unified **`TurnContext`** so an
extension always knows what is running — which agent, model, tool, and step:

```ts
interface TurnContext {
    sessionId: string;
    transcriptPath: string;
    cwd: string;
    agent: string; // "default" | "plan" | custom/extension agent
    modelId: string;
    provider: string;
    model: string;
    tools: string[]; // tool names available this turn (post agent-filter + extensions)
    isSubagent: boolean; // true inside a task subagent run
    step?: number; // current step index within the turn
}

// tool hooks (onResult, future onCall) extend it with the live call:
interface ToolCallContext extends TurnContext {
    toolName: string;
    toolCallId: string;
    input: unknown;
}
```

This lets an extension scope behavior precisely — e.g. an `onResult` that only
appends diagnostics `if (ctx.agent !== "plan")`, or a tool that refuses to run
inside a subagent. The pieces exist scattered today (`runTurn` knows `agent` /
`modelId` / `session`; subprocess hooks get `tool_name` / `tool_input`; the P1
`onResult` already gets `{ toolName, input, cwd, signal }`); P4 consolidates them
into `TurnContext` threaded through the seams.

---

## 7. Turn middleware

In-process middleware at the real assembly points of `runTurn`
(`agent/index.ts`), able to _mutate_, not just observe, each receiving the
`TurnContext` from §6. Complements the existing Claude-compatible **subprocess**
hooks (`agent/hooks.ts`), which stay for portability.

```ts
api.turn.use({
    onBeforeTurn(ctx) {
        /* inspect/replace input; return false to block */
    },
    onSystemPrompt(p, ctx) {
        return p + "\n<extra/>";
    }, // agent/index.ts:332
    onAssembleTools(t, ctx) {
        return { ...t, extra: myTool };
    }, // agent/index.ts:287
    onProviderOptions(o, c) {
        /* tweak thinking/caching */
    }, // agent/index.ts:391
    onAfterTurn(ctx) {
        /* post-turn side effects */
    },
});
```

---

## 8. Lifecycle, ownership, trust

- **Load order:** builtins register first; the host loads after, so extensions
  can override/unregister builtins deterministically. Conflicts between
  extensions: last-writer-wins with a surfaced warning (a `priority` field is a
  later option).
- **Init points:** `host.init()` at startup in interactive, print, and rpc
  paths. MCP-style singleton.
- **Teardown / reload:** `unload(name)` runs `deactivate` and drops the
  extension's contributions; `reload(name)` re-runs `activate` (picks up edits
  for linked extensions via the panel's reload).
- **Trust:** global extensions are trusted by virtue of explicit install.
  Project-local extensions (`<cwd>/.loop/extensions`) gate on the existing
  `isTrusted(cwd)`, exactly like skills / MCP / hooks. `permissions` in the
  manifest are advisory in v1; capability-scoping is a later phase.

---

## 9. Roadmap

- **P1 — Foundation (DONE, verified).** Host, `LoopAPI`, install/link/remove/sync
  (npm + github + local, transitive deps via `BUN_BE_BUN`), `extensions.json`,
  shell verbs. E2E-verified: command add + builtin removal, tool add + builtin
  removal, `onResult` middleware, namespaced settings.
- **P2 — Commands + tools + settings live (DONE, verified).** `host.init()` in
  interactive / print / rpc startup + `/reload`; `applyCommands` after
  `registerBuiltins`; extension tools merge (add/remove) for unrestricted agents
  (gated like MCP) + `onResult` middleware run in `tool-hooks.ts`. Zero-regression
  proven: with no extensions, commands/tools/results are byte-identical (locked by
  `test/extensions.test.ts`); full suite 134 pass / 0 fail.
- **P3 — Providers + models (DONE, verified; login-list pending).** `ProviderPlugin`
  expanded to declarative (`sdk`/`baseURL`/`apiKey`/`models`) + imperative
  (`getModel`/`listModels`), inspired by pi-mono's `registerProvider`. One
  consultation branch in `getModel` (before the switch) + key resolution
  (config `$ENV` → auth store → env var); declarative reuses `customModel`. Pure
  `extensions/providers.ts` maps specs→`ModelInfo`, merged into `getCatalog`.
  Zero-regression: empty host → catalog/`getModel` unchanged (unit-tested). SOLID:
  SRP (pure mapper module), OCP (no switch edits to add a provider), LSP
  (ext model == builtin at call site), DIP (catalog/providers depend on the host's
  narrow getters, no cycle). Login: apikey ext providers appear in the `/login`
  picker + are valid `loop login <id>` targets (generic auth store); oauth ext
  providers self-manage via imperative `getModel` + their own command.
- **P4 — Turn middleware + `TurnContext` (DONE, verified).** `TurnContext`/`ToolCallContext`
  (§6) threaded through `runTurn`; the five seams (`onBeforeTurn` block,
  `onSystemPrompt`, `onAssembleTools`, `onProviderOptions`, `onAfterTurn`) run via
  gated helpers in `agent/turn-middleware.ts` (no-op when no extensions); `onResult`
  now receives the full `ToolCallContext` (agent/model/tools). Zero-regression
  unit-tested; suite 146 pass / 0 fail.
- **P5 — Skills + agents + tool/agent grants (DONE, verified).** Extension skill
  dirs merge into `loadProjectSkills`; extension agents merge into `listAgents` /
  `getAgentPrompt` / `getAgentTools` / `agentExists` (can't shadow builtin/file
  agents); `agentToolNames()` is dynamic (builtins + registered ext tools) so
  allowlists/`parseAgentFile`/`saveAgent` accept ext tool names; `api.tools.grant
(agent, tool)` augments a restricted agent's allowlist; `default` stays all-tools.
  `host.init()` reordered before `registerBuiltins` so ext agents get `/<name>`
  commands. Zero-regression unit-tested; suite 146 pass / 0 fail.
- **P6 — `/extensions` panel + `/install` (DONE, verified).** Interactive panel
  (`extension-handlers.ts`, mirrors `/mcp`): browse, enable/disable, reload,
  uninstall, info; `+ install` row + `/install <spec>` shortcut. Tools/providers/
  turn-middleware take effect immediately; slash commands need `/reload` (surfaced
  in messages). Suite 148 pass / 0 fail. **Verified on the compiled standalone
  binary**: `bun build --compile` succeeds (762 modules); the binary installs from
  npm + `github:` (deps resolved via its own embedded Bun, `BUN_BE_BUN`) and lists
  them — confirming requirements #2 (external deps) and #3 (Bun-only, no second
  binary) on the shipped artifact.
- **Completeness pass (DONE, verified).** Extension tools + `onResult` now apply
  in **subagents** too (candidate pool + `turnContext.isSubagent`), mirroring MCP;
  `host.close()` runs every extension's `deactivate()` on app/print exit; load
  failures (version mismatch, throw in `activate`) are collected and **surfaced**
  in the interactive startup. Suite 150 pass / 0 fail; binary build verified.

    The core system is functionally complete: commands, tools (+grants, +subagents),
    settings, providers + models + login, agents, skills, turn middleware +
    `TurnContext`, the `/extensions` panel, and full lifecycle (activate / reload /
    deactivate / close). Deliberate deferrals (with rationale, not gaps):
    - **Project-local auto-load** (`<cwd>/.loop/extensions`) — auto-running JS from a
      cloned repo is a security decision; folded into P7, not rushed. Extensions are
      global-install-only for now (explicit `loop install`).
    - **Imperative `listModels()` in the catalog** — declarative `models` is the
      catalog/picker path; imperative providers declare `models` for visibility.
    - **Turn-prompt middleware on subagents** — `onResult` applies; the prompt-level
      seams stay main-turn only.

- **P7 — Permissions / sandboxing / project-local / GUI.** Capability enforcement,
  project-trust auto-load, future UI.

**Acceptance test for the system: PASSED.** The LSP feature is re-implemented as
a bundled extension at `extensions/lsp/` (settings via `getOwn` + `onResult` on
`write`/`edit` + language-server deps provisioned through the embedded Bun +
subprocess lifecycle via `deactivate`). Verified end-to-end: it provisions
`typescript-language-server`, spawns it, and appends a real `<diagnostics>` block
("Type 'string' is not assignable to type 'number'. (2322)") after an edit —
identical to the former built-in. The extension system is real.

---

## 10. Open decisions

1. **Login list source of truth** — make `PROVIDER_IDS` consult the host so
   extension providers appear in `loop login` / `/login`, or keep a separate
   "extension providers" section in the picker? (Leaning: merge.)
2. **OAuth providers** — v1 imperative-only (extension drives its own flow), or
   offer a generic device-flow helper in the API?
3. **Model discovery caching** — cache `listModels()` results (TTL) like the
   Ollama/custom-provider discovery, or fetch per catalog build?
4. **Conflict policy** — last-writer-wins + warning vs. explicit `priority`.
5. **Bundling** — stay transpile-on-import, or add optional `Bun.build`
   snapshot-on-install for faster cold start once many extensions are common?
