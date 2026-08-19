<div align="center">

```
█▀▀█ ░▀░
█░░█ ▀█▀
█▀▀▀ ▀▀▀
```

**A coding agent that lives in your terminal.**

One Go binary. No Node, no Python, no runtime to install.

[Install](#install) · [Quick start](#quick-start) · [Providers](#providers) · [Using it](#using-it) · [Configuration](#configuration)

</div>

---

pi reads your code, edits it, runs your tests, and tells you what it did. It
draws **inline** rather than on the alternate screen, so the conversation is
still in your terminal's own scrollback after you quit — scrollable,
selectable, greppable, like any other command you ran.

## Install

**macOS and Linux**

```bash
curl -fsSL https://raw.githubusercontent.com/notshekhar/pi/main/install.sh | bash
```

Prebuilt for macOS (Apple Silicon and Intel) and Linux (x64 and arm64). The
installer picks the right one, verifies its checksum, puts `pi` on your PATH,
and runs it once before telling you it worked.

<details>
<summary>Options</summary>

```bash
# pin a version
curl -fsSL .../install.sh | bash -s -- --version v0.1.3

# build here instead of downloading (needs Go and a C toolchain)
curl -fsSL .../install.sh | bash -s -- --from-source

# remove it — your sessions and settings in ~/.pi-agent are kept
curl -fsSL .../install.sh | bash -s -- --uninstall
```

| Flag | Env | Does |
| --- | --- | --- |
| `--version <tag>` | `PI_VERSION` | install a specific release |
| `--force` | `PI_FORCE=1` | reinstall even when up to date |
| `--from-source` | `PI_FROM_SOURCE=1` | clone and build |
| `--uninstall` | `PI_UNINSTALL=1` | remove the install and its symlinks |
| `--no-modify-path` | `PI_NO_MODIFY_PATH=1` | don't touch your shell rc |
| | `PI_HOME` | install dir (default `~/.pi-bin`) |
| | `PI_BIN_DIR` | where to symlink (default `/usr/local/bin`) |

</details>

**With Go**

```bash
go install github.com/notshekhar/pi/cmd/pi@latest
```

Building needs a C toolchain: sessions live in SQLite and the driver is cgo.

**Windows** is not supported yet. The code compiles for it, but the TUI has no
console raw-mode implementation and the shell-backed tools assume a POSIX
shell — so there is no Windows binary rather than one that exits on launch.
WSL works today.

## Quick start

```bash
cd your-project
pi
```

Then `/login`, pick a provider, paste a key. That's it.

One-shot, for scripts and pipes:

```bash
pi run "what does the retry logic in ./internal/http do?"
pi -model kimi/k3 run "add a test for ParseDuration and run it"
```

| Flag | Meaning |
| --- | --- |
| `-provider` | provider id (`anthropic`, `kimi`, `xai`, …) |
| `-model` | short id or `provider/model` |
| `-reasoning` | `none`, `low`, `medium`, `high`, `xhigh` |
| `-cwd` | working directory (default: current) |
| `-max-steps` | agent step cap (default 24) |

## What it does

**Edits code.** 14 tools — read, write, edit, ls, tree, glob, grep, find,
bash, websearch, plus `todo` for a visible plan, `ask` when a decision is
genuinely yours, `memory` for facts worth keeping, and `skill` (below).
Subagents take delegated work on their own context.

**Asks before it does damage.** Permission rules are `allow` / `ask` / `deny`
per tool and glob, strictest match wins:

```
deny  bash(rm -rf *)
ask   write(**/*.sql)
allow read(**)
```

Plan mode (`/plan`, or shift+tab to the plan agent) makes the whole session
read-only — subagents included.

**Remembers.** Every turn is appended to a WAL SQLite database, so a session
resumes rather than replays: tool calls and signed reasoning round-trip
intact. Fork a conversation at any point, rewind it, rename it, export it to
Markdown, JSONL or HTML.

**Shows what it costs.** A per-turn ledger, sliced the ways the question is
actually asked:

```
 cost
   session          $0.0121   in:48.2k out:1.9k
   directory        $0.4416   ~/src/pi
   today            $1.2088
   last 7 days      $6.9310
   this month      $21.4471
   lifetime         $84.0217
     anthropic      $71.5648
     deepseek       $12.4569
```

**Skills — progressive disclosure.** A skill is a directory with a `SKILL.md`
in it. At startup pi reads only each one's *name and description* into
context; when a task matches, the agent loads the full instructions with the
`skill` tool. So you can have fifty skills without spending fifty skills'
worth of context on every turn.

```bash
/skills new          # write one — pi handles the frontmatter
/skills              # browse what you have
```

```markdown
---
name: commit-style
description: Use when writing a commit message for this repo.
---

Subject line in the imperative, under 60 characters. Explain WHY in the
body, never what — the diff already says what.
```

**Extends.** Seven builtin extensions (`git`, `lsp`, `wayfinder`, `rtk`,
`caveman`, `ponytail`, `statusline-themes`), MCP servers whose tools join the
set, and Claude Code-compatible hooks — ten events, matchers, JSON on stdin,
exit 2 to block a call.

**Feels right.** Markdown with syntax highlighting, a width model that
*measures* your terminal instead of guessing (Devanagari and emoji land where
they should), 60fps differential rendering, and a transcript you can navigate,
fold and copy from.

## Providers

16 built in, 777 models:

`anthropic` · `openai` · `google` · `xai` · `deepseek` · `kimi` · `mistral` ·
`groq` · `cerebras` · `openrouter` · `zenmux` · `glm` · `zai` · `vercel` ·
`bedrock` · `ollama`

Full ids are `provider/model` — `anthropic/claude-sonnet-4-6`, `kimi/k3`,
`xai/grok-4-fast`. `/model` and `/provider` open a searchable picker.

Credentials come from the environment (`ANTHROPIC_API_KEY`, …) or from
`~/.loop/auth.json`, which pi shares with loop — sign in to one and the other
sees it. xAI also offers a **SuperGrok subscription sign-in**, which wins over
an API key when both are present, so a subscriber isn't billed twice.

**Any OpenAI-, Anthropic- or Gemini-compatible endpoint** works too — a
gateway, a proxy, a local server:

```
/login custom
```

It asks the endpoint what models it serves before asking you, and takes a key,
an environment variable, a command whose output is the key (vault, SSO), or
no credential at all.

## Using it

The composer is multiline (Alt+Enter for a newline), keeps history on ↑/↓,
completes `/commands` and `@paths`, and runs a shell command with a leading
`!`. Esc interrupts a turn.

| Key | Does |
| --- | --- |
| `ctrl+e` | hand the keyboard to the transcript — walk back, fold, copy. Esc leaves |
| `shift+tab` | cycle agents (including plan mode) |
| `ctrl+p` | cycle your scoped models |
| `ctrl+g` | resume interrupted work |
| `ctrl+l` | clear the screen |
| `ctrl+c` ×2 | quit |

67 slash commands. The ones worth knowing first:

| | |
| --- | --- |
| `/login` `/model` | providers and models |
| `/resume` `/fork` `/new` | sessions |
| `/cost` `/context` | what you're spending, and on what |
| `/permissions` `/plan` | what the agent may do |
| `/skills` `/memory` | what it knows |
| `/settings` `/theme` | everything else |

`/help` lists them all.

## Configuration

```
~/.pi-agent/
├── agent.db          sessions, messages, cost ledger (SQLite, WAL)
├── settings.json     preferences
├── skills/           your skills, in every project
├── memory/           per-repo facts the agent keeps
└── AGENTS.md         instructions for every project

~/.loop/auth.json     credentials (shared with loop)

<project>/
├── AGENTS.md         instructions for this repo (CLAUDE.md works too)
└── .pi-agent/skills/ skills committed with the repo
```

`AGENTS.md` is read from your home directory, the repo root, and every
directory down to where you launched pi — nearest wins. Unlike skills and
memory, it goes in whole: it is the one document whose purpose is to be in the
model's head *before* the first tool call, not fetched after a wrong turn.

`/settings` edits the rest from inside the TUI, and every toggle actually
gates the thing it names.

## Building

```bash
git clone https://github.com/notshekhar/pi
cd pi
go build ./cmd/pi        # needs a C toolchain for the SQLite driver
go test ./...
```

```
internal/modules/ai     model layer — providers, streaming, tool calling
internal/modules/tui    markdown, theme, width model, editor, renderer
internal/modules/core   agent loop, tools, sessions, config, catalog
cmd/pi                  the app that glues them together
```

The dependency graph is the design: `ai` imports nothing of ours, `tui` and
`core` each import only `ai`, and nothing imports `tui` except `cmd` — core
has no idea a terminal exists. See [AGENTS.md](AGENTS.md).

One external dependency: `mattn/go-sqlite3`. Everything else — the markdown
lexer, the width model, the theme layer — is hand-rolled.

## Status

Early, and honest about it. [PARITY.md](PARITY.md) tracks what is done and
what is not, including the passes where a feature existed and still behaved
wrong — those are the interesting ones.

## License

MIT
