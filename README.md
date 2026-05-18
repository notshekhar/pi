# pi · terminal coding agent

`pi` (alias: `agent`) is a fast, multi-provider terminal coding agent. Bring your own model — xAI, Anthropic, OpenAI, Google, OpenRouter, GitHub Copilot, or run Claude / Cursor agent SDKs natively — and work in your shell with file edits, command execution, sessions, and a TUI that doesn't fight you.

```
$ npm i -g @notshekhar/pi
$ pi
```

…or:

```
curl -fsSL https://raw.githubusercontent.com/notshekhar/pi/main/install.sh | bash
```

---

## What works today

### Language model providers

| Provider          | Auth                          | Notes                                  |
|-------------------|-------------------------------|----------------------------------------|
| xAI (Grok)        | OAuth (SuperGrok) or API key  | Default model: `xai/grok-4`            |
| Anthropic         | API key                       | Claude 4.x (Opus / Sonnet / Haiku)     |
| OpenAI            | API key                       | GPT-5, GPT-5.5, o3-mini                |
| Google            | API key                       | Gemini 3.1 Pro / Flash / Flash-Lite    |
| OpenRouter        | API key                       | 100+ models routed through OR          |
| GitHub Copilot    | OAuth (device flow)           | Subscription-billed models             |

### External agent SDKs (their full toolchain, our TUI)

| Agent             | Auth                                  | Notes                                      |
|-------------------|---------------------------------------|--------------------------------------------|
| Claude Agent SDK  | API key **or** Claude Pro/Max OAuth   | Anthropic's full coding-agent runtime      |
| Cursor Agent SDK  | API key                               | Cursor's Composer 2.5 + Composer 2 + subagents |

### Features

- **Streaming TUI** — text, thinking/reasoning, tool calls, tool results, all live.
- **Tools (canonical 7)** — `read`, `bash`, `edit`, `write`, `grep`, `find`, `ls` rendered with colored diffs, syntax-highlighted output, file previews.
- **Slash commands** — `/help`, `/login`, `/model`, `/provider`, `/thinking`, `/compact`, `/session`, `/sessions`, `/cost`, `/attach`, `/cwd`, `/copy`, `/export`, `/import`, `/settings`, `/hotkeys`, `/reload`.
- **Thinking levels** — `off / minimal / low / medium / high / xhigh`. Mapped per provider (Anthropic budget tokens, OpenAI/xAI/OpenRouter reasoning effort, Google `thinkingConfig`).
- **Sessions** — JSONL per cwd in `~/.pi/agent/sessions/`. Auto-compaction at ~80% context. Manual `/compact`. Pi-mono session format compatible (forks on first write).
- **Skills** — drop `*.md` under `~/.pi/agent/skills/` or `.pi/skills/`; auto-registered as `/skill:<name>`.
- **Prompts** — drop `*.md` under `~/.pi/agent/prompts/` to invoke as a slash command.
- **Vision** — paste an image (Cmd+V on macOS, Ctrl+V elsewhere), `/attach <path>`, or `Ctrl+I` file picker. Sent inline to vision-capable models.
- **Subagents** — Cursor's `task` lifecycle rendered as flat `▸ subagent started … ✓ subagent done` indicators in the chat.
- **Self-upgrade** — `pi upgrade` checks latest GitHub release and reinstalls only when newer.
- **JSON-RPC server** — `pi rpc` (stdio) or `pi rpc --socket` (Unix socket) for programmatic control. Same agent loop as the TUI.

### What's **not** here yet

- No background / cloud agents (no Cursor cloud runtime, no Claude background tasks).
- No multi-device session sync.
- No web UI / desktop app — terminal only.
- No MCP server passthrough yet (external agents use their bundled tools).
- No first-class macOS/Windows binary — install requires Node ≥ 20.
- No Windows-native installer; `install.ps1` is on the roadmap.

---

## Install

### Requirements

- **Node.js ≥ 20** — installer fails clearly with install hints if missing.
- macOS, Linux, or WSL (Windows native still TBD).

### Option A — npm (recommended)

```bash
npm i -g @notshekhar/pi
pi --version
pi
```

### Option B — release tarball via curl

```bash
curl -fsSL https://raw.githubusercontent.com/notshekhar/pi/main/install.sh | bash
```

The script downloads the latest GitHub Release tarball, verifies sha256, runs `npm ci --omit=dev` for runtime deps, and links `pi` + `agent` globally. Env knobs: `PI_VERSION`, `PI_FORCE`, `PI_FROM_SOURCE`, `PI_HOME`.

### Option C — source

```bash
git clone https://github.com/notshekhar/pi.git
cd pi
npm install
npm run build
npm run link
```

---

## Quickstart

```bash
pi login          # pick a provider, OAuth or paste API key
pi                # start the TUI
```

In the TUI:

```
/model            # pick a model
/thinking high    # crank reasoning effort
/attach screenshot.png   # add an image
```

One-shot, no TUI:

```bash
pi run "summarize what this repo does"
```

---

## Commands

| Command                        | Purpose                                          |
|--------------------------------|--------------------------------------------------|
| `pi`                           | Interactive TUI                                  |
| `pi run <prompt>`              | One-shot non-interactive run                     |
| `pi login [provider]`          | Configure auth (OAuth or API key)                |
| `pi logout [provider]`         | Remove auth                                      |
| `pi whoami`                    | Show active provider + authorized providers      |
| `pi sessions`                  | List sessions in current cwd                     |
| `pi models`                    | List all known models from the catalog           |
| `pi rpc [--socket]`            | Start JSON-RPC server (stdio / Unix socket)      |
| `pi upgrade [--force]`         | Self-update via the installer                    |
| `pi -v` / `pi --version`       | Print version                                    |

Flags: `--model <provider/id>`, `--provider <id>`, `--cwd <path>`, `--session <id>`.

---

## Config & data

All under `~/.pi/`:

```
~/.pi/
├── auth.json                # provider tokens (mode 600)
├── settings.json            # defaultModel, thinkingLevel, maxSteps, etc.
├── cost.json                # lifetime + per-provider cost tracking
├── catalog.json             # model catalog cache
└── agent/
    ├── sessions/<cwd-slug>/<id>.jsonl   # per-cwd session JSONL
    ├── prompts/*.md                     # custom slash commands
    └── skills/*.md                      # auto-registered skills
```

Pi-mono compatible: existing `~/.pi/agent/sessions/` files are readable and fork on first write.

---

## Tech stack

- **TypeScript** (strict, ESM), **Node ≥ 20**
- **Build**: [`tsup`](https://tsup.egoist.dev/) for the ESM bundle, `tsc` for `.d.ts`
- **AI runtime**: [Vercel AI SDK v6](https://sdk.vercel.ai/) (`ai`, `@ai-sdk/anthropic`, `@ai-sdk/openai`, `@ai-sdk/google`, `@ai-sdk/xai`, `@openrouter/ai-sdk-provider`) — provider-agnostic streaming + tool use
- **External agent SDKs**: [`@anthropic-ai/claude-agent-sdk`](https://github.com/anthropics/claude-agent-sdk-typescript), [`@cursor/sdk`](https://cursor.com/docs/sdk/typescript) — both dynamic-imported, optional
- **TUI**: [`@earendil-works/pi-tui`](https://github.com/earendil-works/pi) and [`@earendil-works/pi-coding-agent`](https://github.com/earendil-works/pi) — chat rendering, tool execution components, theme, selectors, dynamic borders
- **Schemas**: [`zod`](https://zod.dev/) v4
- **Storage**: [`configstore`](https://github.com/yeoman/configstore) for JSON config, JSONL for sessions
- **Distribution**: GitHub Releases + npm (`@notshekhar/pi`, `@notshekhar/pi-core`)

### Monorepo layout

```
packages/
  core/   @notshekhar/pi-core  — auth, providers, catalog, sessions, agent loop, tools, RPC, compaction, thinking
  cli/    @notshekhar/pi       — bin: `pi` / `agent` (interactive TUI + `run` + RPC server)
```

---

## Credits

Huge thanks to:

- **Mario Zechner / earendil-works** — [`pi-tui`](https://github.com/earendil-works/pi) and `pi-coding-agent`. The TUI primitives (renderer, tool execution component, theme, selectors, dynamic borders) and the pi-mono session JSONL format are theirs. `pi` here consumes their published packages from npm — we don't vendor or modify them. Without `pi-mono` this CLI would be a fraction of what it is.
- **Vercel** — the [AI SDK](https://sdk.vercel.ai/) is the agent loop's foundation. Multi-provider streaming, tool use, and reasoning support all ride on it.
- **Anthropic** — for the Claude Agent SDK (subscription-friendly OAuth + Claude Code's tool suite).
- **Cursor** — for the Cursor TypeScript SDK (Composer 2.5, subagents, full coding harness).
- **xAI / OpenAI / Google / OpenRouter / GitHub** — model APIs we route through.

If you use `pi`, you're standing on all of their shoulders.

---

## Development

```bash
npm install
npm run dev          # rebuild core, then run CLI via tsx
npm run dev:fast     # skip the core rebuild
npm run build        # both packages: gen catalog → core → cli
npm run typecheck
```

To release:

```bash
git tag v0.x.y
git push origin v0.x.y
```

The release workflow runs three jobs:

1. **build** — version sync, `npm ci`, `npm run build`, typecheck, smoke test, tar+sha256, upload artifact
2. **github-release** — creates / updates the GitHub Release with the tarball
3. **npm-publish** (gated on `NPM_TOKEN` secret) — publishes `@notshekhar/pi-core` then `@notshekhar/pi` with `--access public --provenance`

Both 2 and 3 run in parallel after build.

---

## Roadmap

Short list (in rough order of want):

- MCP server passthrough — register tools with Cursor / Claude SDKs so the canonical 7 (read / bash / edit / write / grep / find / ls) replace their bundled equivalents and render identically.
- Windows-native `install.ps1` + prebuilt single-file binary (bun-compile or node SEA).
- Per-tool detail rendering for cursor / claude SDKs (shell stdout streaming, multi-edit previews).
- More providers — Amazon Bedrock, Azure OpenAI, Mistral, custom OpenAI-compatible endpoints (catalog already supports the shape).
- Web UI / desktop client via the JSON-RPC server (the contract is already there).
- Background / async jobs.

PRs welcome.

---

## License

MIT.
