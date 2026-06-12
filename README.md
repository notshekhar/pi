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
> pi-mono provides the TUI renderer (`pi-tui`) and the session format; the message/tool
> renderers, theme engine, and skills loader are our own ports of pi-mono's components.
> This repo wires those primitives to multiple model providers and external agent SDKs.
> See [Credits](#credits).

---

## What it does

- One TUI, many models. Switch with `/model`.
- Renders streaming text, reasoning, tool calls, and diffs inline.
- Stores sessions as JSONL per cwd. Compatible with pi-mono.
- Works from a one-shot prompt (`pi run "..."`) or interactively.
- Exposes a JSON-RPC server for programmatic use.

## Providers

| Provider       | Auth                         | Notes                               |
| -------------- | ---------------------------- | ----------------------------------- |
| xAI (Grok)     | OAuth (SuperGrok) or API key | Default: `xai/grok-4`               |
| Anthropic      | API key                      | Claude 4.x — Opus / Sonnet / Haiku  |
| OpenAI         | API key                      | GPT-5, GPT-5.5, o3-mini             |
| Google         | API key                      | Gemini 3.1 Pro / Flash / Flash-Lite |
| OpenRouter     | API key                      | 100+ models routed through OR       |
| GitHub Copilot | OAuth (device flow)          | Subscription-billed                 |

### External agent SDKs

The full upstream toolchain, rendered in pi's TUI.

| SDK              | Auth                            | Notes                                 |
| ---------------- | ------------------------------- | ------------------------------------- |
| Claude Agent SDK | API key or Claude Pro/Max OAuth | Anthropic's coding-agent runtime      |
| Cursor Agent SDK | API key                         | Composer 2.5 + Composer 2 + subagents |

---

## Usage

```bash
pi                              # interactive TUI
pi run "explain this repo"      # one-shot
pi run -- --session <id> "..."  # resume a session
pi login [provider]             # auth
pi sessions                     # list sessions in cwd
pi models                       # list catalog
pi upgrade                      # self-update
pi rpc [--socket]               # JSON-RPC over stdio or Unix socket
```

Flags: `--model <provider/id>`, `--provider <id>`, `--cwd <path>`, `--session <id>`.

### Slash commands

`/help` `/login` `/model` `/provider` `/thinking` `/compact` `/session` `/sessions` `/cost` `/attach` `/cwd` `/copy` `/export` `/import` `/settings` `/hotkeys` `/reload`

### Tools

`read` · `bash` · `edit` · `write` · `grep` · `find` · `ls` — colored diffs, syntax-highlighted output, file previews.

### Thinking

`/thinking off | minimal | low | medium | high | xhigh`

Mapped per provider: Anthropic budget tokens, OpenAI/xAI/OpenRouter reasoning effort, Google `thinkingConfig`.

### Vision

Paste image (Cmd+V on macOS, Ctrl+V elsewhere), `/attach <path>`, or `Ctrl+I` for the file picker.

### Skills & prompts

Drop `*.md` under `~/.pi/agent/skills/` or `.pi/skills/` — auto-registered as `/skill:<name>`.
Drop `*.md` under `~/.pi/agent/prompts/` — invokable as a slash command.

---

## Install

### macOS / Linux / WSL

```bash
curl -fsSL https://raw.githubusercontent.com/notshekhar/pi/main/install.sh | bash
```

Downloads the latest GitHub Release binary tarball (bun-compiled, ~67 MB, zero runtime), verifies sha256, symlinks `pi` and `agent` into `/usr/local/bin` or `~/.local/bin`.

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
npm install
npm run build
npm run link
```

---

## Config

Everything under `~/.pi/`:

```
~/.pi/
├── auth.json                           # provider tokens (mode 600)
├── settings.json                       # defaultModel, thinkingLevel, maxSteps
├── models.json                         # user-added models / catalog overrides
├── cost.json                           # cost tracking
├── catalog.json                        # model catalog cache
└── agent/
    ├── sessions/<cwd-slug>/<id>.jsonl  # session JSONL, per cwd
    ├── prompts/*.md                    # custom slash commands
    └── skills/*.md                     # auto-registered skills
```

Existing pi-mono sessions are read directly and fork on first write.

### Adding a model

If a model isn't in the catalog (a brand-new release, an OpenRouter `:free` variant, a private deployment), add it to `~/.pi/models.json` — either via `/model` → `+ add model…` in the TUI, or by hand:

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

Keys are full `provider/model-id` ids; entries merge over the built-in catalog, so the same file also overrides pricing or context windows of known models. `cost` is per million tokens and defaults to 0 — set it if you want cost tracking to bill the model. The id is not validated up front; a wrong one simply errors on the first request.

---

## Stack

- TypeScript (strict, ESM). Runtime: bun-compiled single binary (no Node required for users). Dev: Node ≥ 20 or bun ≥ 1.2
- [Vercel AI SDK v6](https://sdk.vercel.ai/) — agent loop, streaming, tool use, reasoning
- [`@earendil-works/pi-tui`](https://github.com/earendil-works/pi) — TUI renderer (differential rendering, editor, markdown, select lists). Message/tool components, theme engine, and skills loader are in-repo ports of pi-mono's equivalents
- [`@anthropic-ai/claude-agent-sdk`](https://github.com/anthropics/claude-agent-sdk-typescript), [`@cursor/sdk`](https://cursor.com/docs/sdk/typescript) — dynamic-imported, optional
- [`zod`](https://zod.dev/) v4 schemas, [`configstore`](https://github.com/yeoman/configstore) for config, JSONL for sessions
- `tsup` for the ESM bundle, `tsc` for `.d.ts`
- Distributed as GitHub Release tarballs

### Layout

```
packages/
  core/   @notshekhar/pi-core  — auth, providers, catalog, sessions, agent loop, tools, RPC
  cli/    @notshekhar/pi       — bin: `pi` / `agent` (TUI + run + RPC)
```

---

## Not (yet) here

- No background / cloud agents.
- No multi-device sync.
- No web / desktop UI — terminal only.
- No MCP server passthrough (external SDKs use their bundled tools).
- No package-manager distribution (Homebrew, Scoop, winget) yet.

---

## Development

```bash
npm install
npm run dev          # rebuild core, then run CLI via tsx
npm run dev:fast     # skip the core rebuild
npm run build        # both packages: gen catalog → core → cli
npm run typecheck
```

Release:

```bash
git tag v0.x.y
git push origin v0.x.y
```

CI runs **build** (version sync, `npm ci`, build, typecheck, smoke test, tar+sha256) then **github-release** (uploads tarball to the GitHub Release).

---

## Roadmap

- MCP passthrough so the canonical 7 tools replace bundled equivalents inside Cursor / Claude SDKs.
- Package-manager distribution: Homebrew tap, Scoop bucket, winget.
- Per-tool detail rendering for Cursor / Claude SDKs (stdout streaming, multi-edit previews).
- More providers — Bedrock, Azure OpenAI, Mistral, custom OpenAI-compatible endpoints.
- Web / desktop client over JSON-RPC.
- Background / async jobs.

PRs welcome.

---

## Credits

`pi` exists because other people did the hard parts first.

- **[Mario Zechner](https://github.com/badlogic) / [earendil-works](https://github.com/earendil-works)** — author of **[pi-mono](https://github.com/earendil-works/pi)** ([`@earendil-works/pi-tui`](https://www.npmjs.com/package/@earendil-works/pi-tui)). The TUI renderer and session JSONL format are his, consumed from npm unmodified. The message components, tool renderer, theme engine, and skills loader in this repo are ports of pi-mono's MIT-licensed source. Without pi-mono this CLI would be a fraction of what it is.
- **Vercel** — the [AI SDK](https://sdk.vercel.ai/) carries the agent loop, multi-provider streaming, tool use, and reasoning.
- **Anthropic** — the Claude Agent SDK (OAuth + Claude Code's tool suite).
- **Cursor** — the TypeScript SDK (Composer 2.5, subagents).
- **xAI / OpenAI / Google / OpenRouter / GitHub** — model APIs.

---

## License

MIT.
