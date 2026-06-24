# Writing loop extensions

Extensions add to or override almost everything in loop: slash commands, tools,
providers and their models, agents, skills, settings, the system prompt, and the
turn loop. They are plain **Bun/TypeScript** packages loaded in-process — no
build step (loop transpiles the entry on import) and they may carry their own npm
dependencies (resolved by the Bun runtime shipped inside the loop binary).

## Anatomy

An extension is an npm-style package: a directory (or published package, or
GitHub repo) with a `package.json` and an entry module.

```
my-ext/
  package.json
  index.ts          # default-exports { activate, deactivate? }
```

`package.json`:

```jsonc
{
    "name": "loop-ext-myext", // the extension's identity
    "version": "1.0.0",
    "type": "module",
    "dependencies": { "zod": "^3" }, // optional — its own deps
    "loop": {
        "entry": "./index.ts", // optional; default: module → main → index.ts
        "displayName": "My Extension",
        "engines": { "loop": "^0.1" }, // optional host API range (major checked)
        "permissions": ["fs", "net"], // optional, advisory for now
    },
}
```

Entry module:

```ts
import type { LoopAPI } from "@notshekhar/loop-core"; // types only (optional)

export default {
    activate(api: LoopAPI) {
        // register contributions here
    },
    deactivate() {
        // optional; loop already undoes every contribution on disable/uninstall.
        // Use this only to release your own resources (timers, subprocesses).
    },
};
```

Everything an extension registers is **owned** by it: loop tears it all down on
disable / uninstall / reload. You never unwind your own contributions.

## Install & manage

```
loop install <npm-name|@scope/pkg|github:owner/repo|https://github.com/o/r|./local-path>
loop link <path>          # dev: load a local folder in place
loop list                 # or: loop extensions
loop enable <name> / loop disable <name>
loop remove <name>
```

In-session: `/extensions` (panel: enable/disable/reload/uninstall/info) and
`/install <spec>`. Tools/providers/turn-middleware take effect immediately;
newly added **slash commands** need `/reload`.

## Built-in extensions

loop ships several extensions inside the binary — already "installed", just
**disabled by default**. They show in `/extensions` and `loop list` with no
install step; the user enables the ones they want (`loop enable <name>` or the
panel). They can be enabled/disabled/reloaded but not uninstalled.

| name       | what it does                                                                                      |
| ---------- | ------------------------------------------------------------------------------------------------- |
| `lsp`      | Appends type/lint diagnostics after `write`/`edit` via language servers (auto-provisioned).       |
| `ponytail` | "Lazy senior dev" persona — write the minimal solution. `/ponytail lite\|full\|ultra\|off`.       |
| `caveman`  | Ultra-terse replies, fewer tokens. `/caveman lite\|full\|ultra\|wenyan-…\|off`.                   |
| `rtk`      | Rewrites bash commands through the `rtk` binary to compress output. No-op if `rtk` isn't on PATH. |

These are also the reference implementations — read their source under
`packages/core/src/extensions/builtin/` to see real extensions using the API
(tool-result middleware, `onSystemPrompt` personas, `onCall` command rewriting,
external-process lifecycle). Because they're statically bundled, built-ins carry
no per-extension `node_modules`: a built-in either uses only the runtime/`ai`
SDK or embeds its assets (e.g. ponytail/caveman embed their skill text as a TS
constant). Third-party extensions you `loop install` get their own `node_modules`.

## The `api` object

```ts
api.extension; // { dir, manifest, log(...args) } — your own dir + a namespaced logger
api.version; // host API version
api.commands; // register / unregister / override slash commands
api.tools; // add / remove / grant / onCall / onResult
api.settings; // get / set (core keys) + getOwn / setOwn (your namespaced keys)
api.providers; // register / unregister model providers (+ their models)
api.models; // add catalog entries directly
api.agents; // register a named agent (prompt + optional tool allowlist)
api.skills; // addDir(dir) — contribute a skills directory
api.turn; // use(middleware) — hook the turn loop
api.ui; // select / search / prompt / note / error — interactive menus
api.auth; // getSecret / setSecret / openExternal / loopbackOAuth — secrets + OAuth
api.statusLine; // add(fn) / transform(fn) — customize the status line under the input
```

### Commands

```ts
api.commands.register({
    name: "hello",
    description: "say hi",
    handler: (ctx, args) => ctx.emit("info", `hi ${args}`),
});
api.commands.unregister("share"); // remove a builtin
api.commands.override("cost", {
    description: "...",
    handler: (ctx) => {
        /* ... */
    },
});
```

`ctx` is the CommandContext (e.g. `ctx.emit(event, data)`, `ctx.cwd`,
`ctx.setModel(id)`, `ctx.newSession()`, …).

### Tools

Tools are Vercel AI SDK tools (`tool({...})` from `ai`, with a `zod` schema). Add
`ai` and `zod` to your `dependencies`.

```ts
import { tool } from "ai";
import { z } from "zod";

api.tools.add(
    "wc",
    tool({
        description: "Count words in text",
        inputSchema: z.object({ text: z.string() }),
        execute: async ({ text }) => `words: ${text.trim().split(/\s+/).length}`,
    }),
);

api.tools.remove("bash"); // remove a builtin
api.tools.grant("plan", "wc"); // let the restricted plan agent use it
```

The `default` agent automatically gets every registered tool. Restricted agents
(plan, data-analyst, custom allowlists) only get a tool if it's listed or granted.

**Intercept a specific tool's call (pre-execute)** — rewrite args or block:

```ts
api.tools.onCall("bash", (input, ctx) => {
    if (/rm -rf \//.test(input.command)) return false; // block — model gets an error
    return { ...input, command: input.command + " # audited" }; // rewrite (or return nothing)
});
```

**Transform a tool's result (post-execute)** — append/replace text:

```ts
api.tools.onResult(["write", "edit"], (result, ctx) => result + "\n[checked]");
```

`match` is a tool name, an array, or a predicate `(name) => boolean`. Both
callbacks receive `ToolCallContext`: `{ toolName, input, toolCallId, agent,
modelId, provider, model, tools, isSubagent, cwd, sessionId, ... }`.

### Settings

```ts
api.settings.get("theme"); // read a core setting
api.settings.getOwn("level", "info"); // your own namespaced key (with default)
api.settings.setOwn("level", "debug");
```

### Providers & models

Add a whole provider and the models inside it. Two flavors.

**Declarative** (OpenAI/Anthropic/Google-compatible endpoints — loop builds the
model for you):

```ts
api.providers.register({
    id: "myvendor",
    name: "My Vendor",
    sdk: "openai-compatible", // "openai" | "anthropic" | "google" | "openai-compatible"
    baseURL: "https://api.myvendor.com/v1",
    apiKey: "$MYVENDOR_API_KEY", // literal, or $ENV / ${ENV}
    auth: { mode: "apikey", envVar: "MYVENDOR_API_KEY", loginUrl: "https://myvendor.com/keys" },
    models: [
        {
            id: "fast-1",
            name: "Fast 1",
            contextWindow: 128000,
            maxOutput: 16000,
            reasoning: false,
            modalities: ["text"],
            cost: { input: 0.2, output: 0.6, cacheRead: 0.05, cacheWrite: 0.25 },
        },
    ],
});
```

Models are addressed `myvendor/fast-1`, show in `/model`, and price correctly.
Key resolution order: config `apiKey` → loop auth store (`loop login myvendor`) →
`auth.envVar`. Apikey providers appear in the `/login` picker automatically.

**Imperative** (custom auth/fetch/SDK — you build the ai-sdk model, importing
your own deps):

```ts
api.providers.register({
    id: "myvendor",
    auth: { mode: "oauth" },
    async getModel(modelId, ctx) {
        // ctx: { apiKey, fetch }
        const { createOpenAI } = await import("@ai-sdk/openai");
        return createOpenAI({ apiKey: ctx.apiKey, baseURL: "...", fetch: ctx.fetch })(modelId);
    },
});
```

Declare `models: [...]` for catalog/picker visibility even with imperative
`getModel`. `api.models.add(...modelInfos)` adds raw catalog entries directly.

### Agents

```ts
api.agents.register({
    name: "reviewer",
    prompt: "You are a meticulous code reviewer...",
    tools: ["read", "grep", "find", "wc"], // omit for all tools (like default)
});
```

Registered agents appear in `listAgents`, get a `/<name>` command, and may name
extension tools in their allowlist. They can't shadow a builtin or a user's
file-based agent of the same name.

### Skills

```ts
import { join } from "node:path";
api.skills.addDir(join(api.extension.dir, "skills")); // dir of SKILL.md folders / *.md
```

### Turn middleware — hook the turn loop

Every seam (except `onBeforeTurn`) receives the full `TurnContext`
(`{ agent, modelId, provider, model, tools, isSubagent, cwd, sessionId, transcriptPath }`),
so you can scope by agent or model.

```ts
api.turn.use({
    // block or inspect the user input (pre-assembly)
    onBeforeTurn(ctx) {
        // ctx: { input, cwd, sessionId, agent, modelId }
        if (ctx.input.includes("SECRET")) return false; // block the turn
    },
    // UPDATE A SPECIFIC AGENT'S SYSTEM PROMPT
    onSystemPrompt(prompt, ctx) {
        if (ctx.agent === "plan") return prompt + "\n\nExtra rules for planning.";
        // return nothing → leave it unchanged
    },
    // add/remove/wrap tools just before the model call
    onAssembleTools(tools, ctx) {
        return { ...tools, extra: myTool };
    },
    // tweak provider options (thinking/caching)
    onProviderOptions(opts, ctx) {
        return opts;
    },
    // observe completion
    onAfterTurn(ctx) {},
});
```

### UI — interactive menus & prompts

The same menu/prompt primitives the built-in panels (e.g. `/mcp`) use, so your
slash commands can build rich, interactive panels. `select`/`search`/`prompt`
resolve to `null`/`""` when the user cancels (Esc).

```ts
api.commands.register({
    name: "pick",
    description: "demo picker",
    handler: async () => {
        const choice = await api.ui.search(
            [
                { value: "a", label: "Apple", description: "a fruit" },
                { value: "b", label: "Banana", description: "also a fruit" },
            ],
            "Pick one (type to filter, Esc to cancel)",
        );
        if (!choice) return; // cancelled
        const note = await api.ui.prompt("Add a note (optional)");
        api.ui.note(`you picked ${choice.label}${note ? ` — ${note}` : ""}`);
    },
});
```

> **Interactive only.** Every `api.ui.*` method **throws** in non-interactive
> (`loop -p` / print) mode. Guard interactive flows, or keep them inside command
> handlers (which only run in a real session).

### Auth & OAuth — secrets, browser, loopback login

Namespaced secret storage (kept out of `settings.json`) plus a browser opener and
a localhost-loopback OAuth helper — enough to implement a full remote-auth flow.
loop catches the redirect and hands you the `code`; you do the provider-specific
token exchange yourself and persist the result with `setSecret`.

```ts
// simple secret
api.auth.setSecret("apiKey", "sk-…");
const key = api.auth.getSecret("apiKey");

// full loopback OAuth
const { code, redirectUri } = await api.auth.loopbackOAuth({
    buildAuthorizeUrl: (redirect) =>
        `https://provider.example/oauth/authorize?response_type=code` +
        `&client_id=${CLIENT_ID}&redirect_uri=${encodeURIComponent(redirect)}&state=xyz`,
});
const tokens = await fetch("https://provider.example/oauth/token", {
    method: "POST",
    body: new URLSearchParams({ grant_type: "authorization_code", code, redirect_uri: redirectUri }),
}).then((r) => r.json());
api.auth.setSecret("refresh_token", tokens.refresh_token);
```

These primitives are exactly what's needed to build the whole MCP feature
(server menus via `api.ui`, OAuth via `api.auth`, tools via `api.tools.add`) as a
standalone extension.

### Status line — the rows under the input box

The two-row block under the prompt (`agent … · model` / `session · cost · ctx`)
is customizable. `add` appends segment(s); `transform` rewrites the whole thing.
Both run on every repaint with a fresh `StatusLineContext`
(`{ agent, modelId, provider, model, sessionId, cwd, cost, context, thinking, width }`),
so keep them cheap and synchronous. A throwing contributor/transform is caught
and ignored — it can never break the render.

```ts
import { execSync } from "node:child_process";
import chalk from "chalk";

// Append a git-branch segment to the usage row (row 1).
api.statusLine.add((ctx) => {
    try {
        const branch = execSync("git branch --show-current", { cwd: ctx.cwd }).toString().trim();
        return branch ? { text: chalk.magenta(`⎇ ${branch}`), row: 1 } : null;
    } catch {
        return null; // not a git repo
    }
});

// Or add a whole new row (row index ≥ 2 creates one):
api.statusLine.add((ctx) => ({ text: chalk.dim(ctx.cwd), row: 2 }));

// Full control: rewrite the already-rendered rows.
api.statusLine.transform((lines, ctx) => lines.map((l) => l.toUpperCase()));
```

A contributor may return a `{ text, row? }` segment, an array of them, a bare
string (appended to the usage row), or `null`/`undefined` to add nothing.
`row`: `0` = identity (agent/model), `1` = usage (default), `≥2` = a new row.

## Lifecycle notes

- `activate(api)` runs once when the extension loads (startup, enable, reload, or
  fresh install). Keep it synchronous-ish and fast; do heavy work lazily.
- `api.ui` is wired after activation, so call it from command handlers /
  middleware (a real session), not from the top of `activate`.
- `api.statusLine` contributors run on every repaint (interactive only; print
  mode has no status line).
- `deactivate()` runs on disable / uninstall / reload / app exit — close
  anything you opened (subprocesses, watchers, sockets).
- A throwing `activate` is caught: the extension is skipped and a warning is
  surfaced; it never crashes loop.
- Runtime is **Bun-only**. Use Bun/Web APIs and your own npm deps freely; there
  is no Node-compatibility shim to target.

## Minimal complete example

```ts
// index.ts
import { tool } from "ai";
import { z } from "zod";

export default {
    activate(api) {
        api.commands.register({
            name: "uppercase",
            description: "uppercase the argument",
            handler: (ctx, args) => ctx.emit("info", args.toUpperCase()),
        });

        api.tools.add(
            "reverse",
            tool({
                description: "Reverse a string",
                inputSchema: z.object({ text: z.string() }),
                execute: async ({ text }) => [...text].reverse().join(""),
            }),
        );

        api.tools.onResult("write", (result) => result + "\n[written by me]");
    },
};
```

```jsonc
// package.json
{
    "name": "loop-ext-demo",
    "version": "1.0.0",
    "type": "module",
    "dependencies": { "ai": "^5", "zod": "^3" },
    "loop": { "entry": "./index.ts" },
}
```

Install with `loop link ./my-ext` (dev) or `loop install <name|github:o/r>`.
