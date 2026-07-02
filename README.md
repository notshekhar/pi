# loop

Terminal coding agent. Bring your own model.

**macOS / Linux / WSL**

```
curl -fsSL https://raw.githubusercontent.com/notshekhar/loop/main/install.sh | bash
```

**Windows (PowerShell)**

```
irm https://raw.githubusercontent.com/notshekhar/loop/main/install.ps1 | iex
```

**Windows (cmd.exe)**

```
curl -fsSLo %TEMP%\loop-install.cmd https://raw.githubusercontent.com/notshekhar/loop/main/install.cmd && %TEMP%\loop-install.cmd
```

Then:

```
loop login
loop
```

Prebuilt binaries — no Node required. Targets: `darwin-x64`, `darwin-arm64`, `linux-x64`, `linux-arm64`, `windows-x64`.

> **Built on top of [pi](https://github.com/earendil-works/pi)** by Mario Zechner / earendil-works.
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
- Works from a one-shot prompt (`loop run "..."`) or interactively.
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
| Ollama         | none (local daemon)          | Auto-detected; `LOOP_OLLAMA_BASE_URL` to point |
| Custom         | API key                      | Any OpenAI/Anthropic/Google-compatible gateway |

Custom providers (`/login custom`): name + base URL + key + model list, saved to `~/.loop/` and usable like any built-in — handy for gateways like Bifrost or LiteLLM.

---

## Usage

```bash
loop                              # interactive TUI
loop run "explain this repo"      # one-shot
loop login [provider]             # auth (xai, anthropic, openai, google, openrouter, github-copilot, ollama, custom)
loop logout [provider]            # remove auth
loop sessions                     # list sessions in cwd
loop models                       # list catalog
loop whoami                       # active provider + auth status
loop upgrade                      # self-update
loop rpc [--socket]               # JSON-RPC over stdio or Unix socket
loop help                         # full usage
```

Flags: `--model <provider/id>`, `--provider <id>`, `--cwd <path>`, `--session <id>`.

### Slash commands

| Group    | Commands                                                                                |
| -------- | --------------------------------------------------------------------------------------- |
| Sessions | `/new` `/clear` `/resume` `/sessions` `/session` `/name` `/export` `/import` `/compact` |
| Tree     | `/tree` `/fork` `/clone`                                                                |
| Models   | `/model` `/provider` `/thinking`                                                        |
| Agents   | `/agents` `/<agent> <message>` (one-shot)                                               |
| Setup    | `/login` `/logout` `/settings` `/hooks` `/reload` `/update`                             |
| Misc     | `/help` `/cost` `/attach` `/paste` `/copy` `/cwd` `/hotkeys` `/changelog` `/quit`       |

`/clear` starts a fresh session (and clears the screen). Messages and slash commands typed while the agent is generating queue up and run after the turn.

### Session tree

Sessions are stored as trees — every entry has a parent, and the "leaf" is where the next message lands.

- **`/tree`** — navigate the whole session as an ASCII tree: fold branches, filter (no-tools / user-only / labeled / all), type-to-search, bookmark entries with labels (`shift+l`). Selecting an earlier point branches there; loop offers to **summarize the abandoned branch** (optionally with a custom prompt) so its context survives the switch.
- **`/fork`** — pick a previous user message; the path up to it is copied into a **new session** and the message text returns to your editor for editing.
- **`/clone`** — duplicate the current conversation into a new session.

Existing flat sessions migrate automatically on first open.

### Agents & subagents

- Built-ins: `default` (full toolset) and `plan` (read-only investigator). Create your own with `/agents` — custom system prompt + tool allowlist, registered as `/<name>` for one-shot use.
- Tab (empty prompt) or Shift+Tab cycles agents.
- The `task` tool spawns a **subagent**: a fork of the current agent (same prompt, same tools minus `task`, fresh context). Activity streams live in the task box; only the final report enters the main context. Toggle via `/settings → subagents`.

### Tools

`read` · `bash` · `edit` · `write` · `grep` · `find` · `ls` · `task` — colored diffs, syntax-highlighted output, file previews. `read` also fetches `http(s)://` URLs as readable text (and takes `offset`/`limit` for large files). `edit`/`write` enforce read-before-modify per session.

### Bash sandbox

`bash` can run each command inside an OS-enforced sandbox (`@notshekhar/loop-sandbox`), so the model's shell access is bounded by the kernel, not just by trust. Enable and shape it via the `sandbox` setting in `settings.json`:

- `enabled` — turn the sandbox on.
- `network` — `"deny"` (default) blocks outbound network, or allow it per your config.
- `allowWrite` / `denyWrite` / `allowRead` / `denyRead` — filesystem boundaries (the working directory is writable by default; everything else is read-only unless allowed).
- `allowGitConfig` — let commands read your git config.

It is **fail-open** by design for the normal case: if the boundary can't be applied on your platform (unsupported OS, missing dependency, wrap failure) the command still runs, but loop appends a `[loop sandbox] … ran WITHOUT isolation` warning — never a silent downgrade. The read-only `plan` agent is the exception: it is **fail-closed** — bash there runs in a kernel-enforced read-only, network-denied sandbox, and is **refused** outright if that can't be enforced.

### Bash denylist

Before anything runs, `bash` checks the command against the `bashDeny` list and refuses a match — a **guardrail, not a security boundary**. It resolves each command to its real name + subcommand, looking past wrappers (`sudo`, `env`, `nohup`, `time`, `xargs`, `rtk`, …), `sh -c "…"` scripts, and `$(…)`/backtick substitutions, and normalizes full paths to their basename (`/bin/rm` → `rm`). Patterns match by name (`rm`) or name + subcommand prefix (`git commit`).

It defaults to `git commit` and `git push` (commits/pushes stay with the human). Override the whole list in `settings.json` or manage it with `/bashdeny`. String inspection is fundamentally bypassable (base64-pipe-to-sh, write-a-script-then-run-it, …) — that's what the sandbox above is for; the denylist just reliably stops honest, ordinary invocations.

### Hooks

Lifecycle hooks (PreToolUse, PostToolUse, SessionStart, Stop, …) run shell commands with a JSON payload on stdin — **Claude Code-compatible**: hooks and plugins from `~/.claude/settings.json` and `.claude/` are imported automatically (filter with the `claudeHooksFilter` setting). Manage loop-owned hooks with `/hooks`; project hooks live in `<repo>/.loop/settings.json`. First open of a repo that ships hooks/skills asks for **project trust** before anything executes.

### Thinking

`/thinking off | minimal | low | medium | high | xhigh`

Mapped per provider: Anthropic budget tokens, OpenAI/xAI/OpenRouter reasoning effort, Google `thinkingConfig`.

### Vision

Paste image (Cmd+V / Ctrl+V), `/attach <path>`, or `Ctrl+I` for the file picker.

### Skills, prompts, workspace context

- `*.md` under `~/.loop/agent/skills/` or `.loop/skills/` — auto-registered as `/skill:<name>`.
- `*.md` under `~/.loop/agent/prompts/` — invokable as a slash command.
- `AGENTS.md` / `CLAUDE.md` in the repo are loaded as workspace context automatically.

### Cost tracking

The footer shows live cost/usage/context per step (subagents included). `/cost` breaks it down by session, directory, today, last 7 days, month, and lifetime per provider. Anthropic prompt caching is managed automatically (moving cache breakpoints across multi-step turns).

### Themes

`dark` and `light` built in; drop pi-mono-format JSON themes in `~/.loop/agent/themes/` and pick via `/settings → theme` (applies live).

---

## Install

### macOS / Linux / WSL

```bash
curl -fsSL https://raw.githubusercontent.com/notshekhar/loop/main/install.sh | bash
```

Downloads the latest GitHub Release binary tarball (bun-compiled, zero runtime), verifies sha256, runs the binary once to confirm it works on your machine, and symlinks `loop` and `agent` into `/usr/local/bin` or `~/.local/bin` (with an exact PATH line for your shell if needed). musl distros (Alpine) get a clear pointer to `gcompat` or a source build.

Env knobs: `LOOP_VERSION` (pin a tag), `LOOP_FORCE`, `LOOP_FROM_SOURCE`, `LOOP_HOME`, `LOOP_BIN_DIR`, `LOOP_UNINSTALL=1` (clean removal — keeps `~/.loop` config).

### Windows

PowerShell:

```powershell
irm https://raw.githubusercontent.com/notshekhar/loop/main/install.ps1 | iex
```

cmd.exe (bootstraps PowerShell):

```cmd
curl -fsSLo %TEMP%\loop-install.cmd https://raw.githubusercontent.com/notshekhar/loop/main/install.cmd && %TEMP%\loop-install.cmd
```

Installs to `%USERPROFILE%\.loop-bin\loop.exe`, adds it to user `PATH` **and the current session** — `loop` works immediately, no new terminal needed. Windows on ARM gets the x64 build (runs under Windows 11's emulation). First run shows SmartScreen — click "More info" → "Run anyway".

Env knobs: `$env:LOOP_VERSION`, `$env:LOOP_FORCE`, `$env:LOOP_HOME`, `$env:LOOP_UNINSTALL = '1'` (clean removal — keeps `~\.loop` config).

### Updating

`/update` inside the TUI, or `loop update` from the shell — both check the latest release and run the platform installer in place (self-update works while loop is running, on Windows too). The TUI also tells you at startup when a newer release exists. `LOOP_SKIP_VERSION_CHECK=1` silences the startup check.

### Canary channel

Prerelease tags (e.g. `v0.8.0-canary.0`) publish as GitHub prereleases with the same per-platform binaries. Stable never sees them — the installer, `loop update`, and brew all resolve `releases/latest`, which skips prereleases. To try a canary, pin its tag:

```bash
LOOP_VERSION=v0.8.0-canary.0 LOOP_FORCE=1 \
  curl -fsSL https://raw.githubusercontent.com/notshekhar/loop/main/install.sh | bash
```

(Windows: `$env:LOOP_VERSION = 'v0.8.0-canary.0'; $env:LOOP_FORCE = '1'` before the installer.) Once the matching stable lands, `loop update` moves you back to the stable channel. The list of prereleases lives on the [releases page](https://github.com/notshekhar/loop/releases).

### From source

```bash
git clone https://github.com/notshekhar/loop.git
cd loop
bun install
bun run build
bun run link
```

---

## Config

Everything under `~/.loop/`:

```
~/.loop/
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

Per-project config in `<repo>/.loop/` (settings + hooks + skills), gated by project trust. The last model picked in a folder is remembered per project.

### Adding a model

If a model isn't in the catalog (a brand-new release, an OpenRouter `:free` variant, a private deployment), add it via `/model` → `+ add model…` in the TUI, or by hand in `~/.loop/models.json`:

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
- `@notshekhar/loop-tui` — the TUI core, **forked from [earendil-works/pi](https://github.com/earendil-works/pi)** (`pi-tui`): differential terminal rendering, editor, markdown, select lists
- [`zod`](https://zod.dev/) v4 schemas, [`configstore`](https://github.com/yeoman/configstore) for config, JSONL for sessions
- Distributed as GitHub Release tarballs

### Layout

```
packages/
  core/   @notshekhar/loop-core  — auth, providers, catalog, session trees, agent loop, tools, hooks, RPC
  tui/    @notshekhar/loop-tui   — terminal renderer, forked from earendil-works/pi (pi-tui)
  cli/    @notshekhar/loop       — bin: `loop` / `agent` (TUI + run + RPC)
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

`loop` exists because other people did the hard parts first.

- **[Mario Zechner](https://github.com/badlogic) / [earendil-works](https://github.com/earendil-works)** — author of **[earendil-works/pi](https://github.com/earendil-works/pi)** (pi-mono). The TUI core in `packages/tui` is a **direct fork of his MIT-licensed `pi-tui`** — the differential renderer, editor, and component model are his work. The session tree format, message components, tool renderer, theme engine, and skills loader are ports of pi-mono's source. Without pi-mono this CLI would be a fraction of what it is.
- **Vercel** — the [AI SDK](https://sdk.vercel.ai/) carries the agent loop, multi-provider streaming, tool use, and reasoning.
- **xAI / Anthropic / OpenAI / Google / OpenRouter / GitHub / Ollama** — model APIs and runtimes.

---

## License

MIT.
