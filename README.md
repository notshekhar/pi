# pi

Terminal coding agent. Bring your own model.

**macOS / Linux / WSL**

```
curl -fsSL https://raw.githubusercontent.com/notshekhar/pi/main/install.sh | bash
```

**Windows (PowerShell)**

```
irm https://raw.githubusercontent.com/notshekhar/pi/main/install.ps1 | iex
```

**Windows (cmd.exe)**

```
curl -fsSLo %TEMP%\pi-install.cmd https://raw.githubusercontent.com/notshekhar/pi/main/install.cmd && %TEMP%\pi-install.cmd
```

Then:

```
pi login
pi
```

Prebuilt binaries — no Node required. Targets: `darwin-x64`, `darwin-arm64`, `linux-x64`, `linux-arm64`, `windows-x64`.

> **Built on top of [pi-mono](https://github.com/earendil-works/pi)** by Mario Zechner / earendil-works.
> The TUI renderer (`packages/tui`) is an in-repo fork of pi-mono's `pi-tui`; the message/tool
> renderers, theme engine, session tree, and skills loader are our own ports of pi-mono's
> components. This repo wires those primitives to multiple model providers via the Vercel AI SDK.
> See [Credits](#credits).

---

## What it does

- One TUI, many models. Switch with `/model`, cycle agents with Tab.
- Renders streaming text, reasoning, tool calls, and diffs inline.
- Sessions are append-only **trees**: `/tree` navigates branches, `/fork` branches from any earlier message, abandoned branches can be summarized back into context.
- Subagents: the `task` tool runs a named agent in its own context window and reports back.
- Claude Code-compatible lifecycle hooks — your existing `~/.claude` hooks and plugins load as-is.
- Works from a one-shot prompt (`pi run "..."`) or interactively.
- Exposes a JSON-RPC server for programmatic use.

## Providers

| Provider       | Auth                         | Notes                                          |
| -------------- | ---------------------------- | ---------------------------------------------- |
| xAI (Grok)     | OAuth (SuperGrok) or API key | Default: `xai/grok-build-0.1`                  |
| Anthropic      | API key                      | Claude 4.x — Opus / Sonnet / Haiku             |
| OpenAI         | API key                      | GPT-5 family                                   |
| Google         | API key                      | Gemini 3.1 Pro / Flash / Flash-Lite            |
| OpenRouter     | API key                      | 100+ models, searchable picker                 |
| GitHub Copilot | OAuth (device flow)          | Subscription-billed                            |
| Ollama         | none (local daemon)          | Auto-detected; `PI_OLLAMA_BASE_URL` to point   |
| Custom         | API key                      | Any OpenAI/Anthropic/Google-compatible gateway |

Custom providers (`/login custom`): name + base URL + key + model list, saved to `~/.pi/` and usable like any built-in — handy for gateways like Bifrost or LiteLLM.

---

## Usage

```bash
pi                              # interactive TUI
pi run "explain this repo"      # one-shot
pi login [provider]             # auth (xai, anthropic, openai, google, openrouter, github-copilot, ollama, custom)
pi logout [provider]            # remove auth
pi sessions                     # list sessions in cwd
pi models                       # list catalog
pi whoami                       # active provider + auth status
pi upgrade                      # self-update
pi rpc [--socket]               # JSON-RPC over stdio or Unix socket
pi help                         # full usage
```

Flags: `--model <provider/id>`, `--provider <id>`, `--cwd <path>`, `--session <id>`.

### Slash commands

| Group    | Commands                                                                              |
| -------- | ------------------------------------------------------------------------------------- |
| Sessions | `/new` `/clear` `/resume` `/sessions` `/session` `/name` `/export` `/import` `/compact` |
| Tree     | `/tree` `/fork` `/clone`                                                               |
| Models   | `/model` `/provider` `/thinking`                                                       |
| Agents   | `/agents` `/<agent> <message>` (one-shot)                                              |
| Setup    | `/login` `/logout` `/settings` `/hooks` `/reload`                                      |
| Misc     | `/help` `/cost` `/attach` `/paste` `/copy` `/cwd` `/hotkeys` `/changelog` `/quit`      |

`/clear` starts a fresh session (and clears the screen). Messages and slash commands typed while the agent is generating queue up and run after the turn.

### Session tree

Sessions are stored as trees — every entry has a parent, and the "leaf" is where the next message lands.

- **`/tree`** — navigate the whole session as an ASCII tree: fold branches, filter (no-tools / user-only / labeled / all), type-to-search, bookmark entries with labels (`shift+l`). Selecting an earlier point branches there; pi offers to **summarize the abandoned branch** (optionally with a custom prompt) so its context survives the switch.
- **`/fork`** — pick a previous user message; the path up to it is copied into a **new session** and the message text returns to your editor for editing.
- **`/clone`** — duplicate the current conversation into a new session.

Existing flat sessions migrate automatically on first open.

### Agents & subagents

- Built-ins: `default` (full toolset) and `plan` (read-only investigator). Create your own with `/agents` — custom system prompt + tool allowlist, registered as `/<name>` for one-shot use.
- Tab (empty prompt) or Shift+Tab cycles agents.
- The `task` tool spawns a **subagent**: a fork of the current agent (same prompt, same tools minus `task`, fresh context). Activity streams live in the task box; only the final report enters the main context. Toggle via `/settings → subagents`.

### Tools

`read` · `bash` · `edit` · `write` · `grep` · `find` · `ls` · `task` — colored diffs, syntax-highlighted output, file previews. `read` also fetches `http(s)://` URLs as readable text. `edit`/`write` enforce read-before-modify per session.

### Hooks

Lifecycle hooks (PreToolUse, PostToolUse, SessionStart, Stop, …) run shell commands with a JSON payload on stdin — **Claude Code-compatible**: hooks and plugins from `~/.claude/settings.json` and `.claude/` are imported automatically (filter with the `claudeHooksFilter` setting). Manage pi-owned hooks with `/hooks`; project hooks live in `<repo>/.pi/settings.json`. First open of a repo that ships hooks/skills asks for **project trust** before anything executes.

### Thinking

`/thinking off | minimal | low | medium | high | xhigh`

Mapped per provider: Anthropic budget tokens, OpenAI/xAI/OpenRouter reasoning effort, Google `thinkingConfig`.

### Vision

Paste image (Cmd+V / Ctrl+V), `/attach <path>`, or `Ctrl+I` for the file picker.

### Skills, prompts, workspace context

- `*.md` under `~/.pi/agent/skills/` or `.pi/skills/` — auto-registered as `/skill:<name>`.
- `*.md` under `~/.pi/agent/prompts/` — invokable as a slash command.
- `AGENTS.md` / `CLAUDE.md` in the repo are loaded as workspace context automatically.

### Cost tracking

The footer shows live cost/usage/context per step (subagents included). `/cost` breaks it down by session, directory, today, last 7 days, month, and lifetime per provider. Anthropic prompt caching is managed automatically (moving cache breakpoints across multi-step turns).

### Themes

`dark` and `light` built in; drop pi-mono-format JSON themes in `~/.pi/agent/themes/` and pick via `/settings → theme` (applies live).

---

## Install

### macOS / Linux / WSL

```bash
curl -fsSL https://raw.githubusercontent.com/notshekhar/pi/main/install.sh | bash
```

Downloads the latest GitHub Release binary tarball (bun-compiled, zero runtime), verifies sha256, symlinks `pi` and `agent` into `/usr/local/bin` or `~/.local/bin`.

Env knobs: `PI_VERSION`, `PI_FORCE`, `PI_FROM_SOURCE`, `PI_HOME`, `PI_BIN_DIR`.

### Windows

PowerShell:

```powershell
irm https://raw.githubusercontent.com/notshekhar/pi/main/install.ps1 | iex
```

cmd.exe (bootstraps PowerShell):

```cmd
curl -fsSLo %TEMP%\pi-install.cmd https://raw.githubusercontent.com/notshekhar/pi/main/install.cmd && %TEMP%\pi-install.cmd
```

Installs to `%USERPROFILE%\.pi-bin\pi.exe`, adds it to user `PATH`. Open a new terminal after install. First run shows SmartScreen — click "More info" → "Run anyway".

Env knobs: `$env:PI_VERSION`, `$env:PI_FORCE`, `$env:PI_HOME`.

### From source

```bash
git clone https://github.com/notshekhar/pi.git
cd pi
bun install
bun run build
bun run link
```

---

## Config

Everything under `~/.pi/`:

```
~/.pi/
├── auth.json                           # provider tokens + custom gateways (mode 600)
├── settings.json                       # defaultModel, thinkingLevel, hooks, …
├── models.json                         # user-added models / catalog overrides
├── cost.json                           # cost tracking buckets
├── catalog.json                        # model catalog cache
├── agents/*.md                         # custom agents (and built-in prompt overrides)
└── agent/
    ├── sessions/<cwd-slug>/<id>.jsonl  # session trees, per cwd
    ├── prompts/*.md                    # custom slash commands
    ├── skills/*.md                     # auto-registered skills
    └── themes/*.json                   # custom themes
```

Per-project config in `<repo>/.pi/` (settings + hooks + skills), gated by project trust. The last model picked in a folder is remembered per project.

Existing pi-mono sessions are read directly; `piCompatMode` controls whether they fork on first write.

### Adding a model

If a model isn't in the catalog (a brand-new release, an OpenRouter `:free` variant, a private deployment), add it via `/model` → `+ add model…` in the TUI, or by hand in `~/.pi/models.json`:

```json
{
  "openrouter/nex-agi/nex-n2-pro:free": {
    "id": "openrouter/nex-agi/nex-n2-pro:free",
    "provider": "openrouter",
    "name": "Nex AGI: Nex-N2-Pro (free)",
    "contextWindow": 262144,
    "maxOutput": 262144,
    "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 },
    "reasoning": true,
    "modalities": ["text", "image"],
    "available": true
  }
}
```

Keys are full `provider/model-id` ids; entries merge over the built-in catalog, so the same file also overrides pricing or context windows of known models. `cost` is per million tokens and defaults to 0 — set it if you want cost tracking to bill the model. The id is not validated up front; a wrong one simply errors on the first request. The catalog itself refreshes from models.dev hourly, so new public models usually just appear.

---

## Stack

- TypeScript (strict, ESM). Runtime: bun-compiled single binary (no Node required for users). Dev: bun ≥ 1.2
- [Vercel AI SDK v6](https://sdk.vercel.ai/) — agent loop, streaming, tool use, reasoning, subagents
- `@notshekhar/pi-tui` — the TUI core, **forked from [earendil-works/pi](https://github.com/earendil-works/pi)** (`pi-tui`): differential terminal rendering, editor, markdown, select lists
- [`zod`](https://zod.dev/) v4 schemas, [`configstore`](https://github.com/yeoman/configstore) for config, JSONL for sessions
- Distributed as GitHub Release tarballs

### Layout

```
packages/
  core/   @notshekhar/pi-core  — auth, providers, catalog, session trees, agent loop, tools, hooks, RPC
  tui/    @notshekhar/pi-tui   — terminal renderer, forked from earendil-works/pi (pi-tui)
  cli/    @notshekhar/pi       — bin: `pi` / `agent` (TUI + run + RPC)
```

---

## Not (yet) here

- No MCP servers.
- No background / cloud agents.
- No multi-device sync.
- No web / desktop UI — terminal only.
- No package-manager distribution (Homebrew, Scoop, winget) yet.

---

## Development

```bash
bun install
bun run dev          # run the CLI from source
bun run build        # gen catalog → core → tui → cli
bun test             # unit tests (hooks, agents, cost, compaction, sessions)
bun run format       # prettier
```

Release:

```bash
git tag v0.x.y
git push origin v0.x.y
```

CI runs **build** (version sync, install, build, typecheck, smoke test, tar+sha256) then **github-release** (uploads tarball to the GitHub Release).

---

## Roadmap

- MCP server support.
- Package-manager distribution: Homebrew tap, Scoop bucket, winget.
- More providers — Bedrock, Azure OpenAI, Mistral.
- Web / desktop client over JSON-RPC.
- Background / async jobs.

PRs welcome.

---

## Credits

`pi` exists because other people did the hard parts first.

- **[Mario Zechner](https://github.com/badlogic) / [earendil-works](https://github.com/earendil-works)** — author of **[earendil-works/pi](https://github.com/earendil-works/pi)** (pi-mono). The TUI core in `packages/tui` is a **direct fork of his MIT-licensed `pi-tui`** — the differential renderer, editor, and component model are his work. The session tree format, message components, tool renderer, theme engine, and skills loader are ports of pi-mono's source. Without pi-mono this CLI would be a fraction of what it is.
- **Vercel** — the [AI SDK](https://sdk.vercel.ai/) carries the agent loop, multi-provider streaming, tool use, and reasoning.
- **xAI / Anthropic / OpenAI / Google / OpenRouter / GitHub / Ollama** — model APIs and runtimes.

---

## License

MIT.
