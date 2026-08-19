# pi

A coding agent that lives in your terminal. One Go module, no runtime to
install, and a TUI that draws inline so your conversation stays in the
terminal's own scrollback when the process exits.

```bash
go install github.com/notshekhar/pi/cmd/pi@latest
pi
```

Or from a clone:

```bash
go run ./cmd/pi -provider kimi
go run ./cmd/pi -model kimi/k3 run "what is in ./internal/modules/core/tools?"
```

Building needs a C toolchain — sessions live in SQLite and the driver is cgo.

## What it does

- **Agentic edits.** Read, write, edit, glob, grep, ls, tree, bash, web
  search and fetch, plus subagents for delegated work.
- **Permissions you can actually express.** `allow`/`ask`/`deny` rules per
  tool and glob, strictest match wins, with a plan mode that makes the whole
  session read-only.
- **Sessions that resume.** Every turn is appended to a WAL SQLite database:
  fork a conversation, rewind it, rename it, export it. Tool calls and signed
  reasoning round-trip, so a resumed session continues rather than merely
  replaying.
- **Cost you can see.** A per-turn ledger behind `/cost`, sliced by day,
  project, model and provider.
- **Extensions and MCP.** Seven builtin extensions, Claude Code-compatible
  hooks (ten events, matchers, exit 2 blocks), and MCP servers whose tools
  join the set.
- **A real TUI.** Markdown with syntax highlighting, a hand-rolled width model
  that measures the terminal instead of guessing, 60fps differential
  rendering, and a transcript you can navigate, fold and copy from.

## Providers

Full ids are `provider/model` — `kimi/k3`, `xai/grok-4-fast`,
`openrouter/anthropic/claude-sonnet-4-6`. `/model` and `/provider` open a
searchable picker; `/login` signs in.

Credentials come from the environment or `~/.loop/auth.json`. xAI offers a
SuperGrok subscription sign-in as well as an API key; the subscription wins
when both are present.

| Flag | Meaning |
| --- | --- |
| `-provider` | catalog id (`kimi`, `anthropic`, `xai`, …) |
| `-model` | short id or `provider/model` |
| `-reasoning` | `none`, `low`, `medium`, `high`, `xhigh` |
| `-cwd` | working directory (default: current) |
| `-max-steps` | agent step cap (default 24) |

## In the TUI

The editor is multiline (Alt+Enter for a newline), keeps history on ↑/↓,
completes `/commands` and `@paths`, runs shell with a leading `!`, and Esc
interrupts a running turn. `ctrl+e` hands the keyboard to the transcript so
you can walk back through it, fold runs of tool calls, and copy; Esc leaves.
`shift+tab` cycles agents, `ctrl+p` cycles your scoped models.

`/help` lists the rest.

## Layout

```
internal/modules/ai     model layer — providers, streaming, tool calling
internal/modules/tui    terminal rendering: markdown, theme, width, editor
internal/modules/core   agent loop, tools, sessions, config, catalog
cmd/pi                  the app that glues them together
```

The dependency graph is the design: `ai` imports nothing of ours, `tui` and
`core` each import only `ai`, and nothing imports `tui` except `cmd`. Core has
no idea a terminal exists. See `AGENTS.md`.

## Status

Early, and honest about it — `PARITY.md` tracks what is done and what is not,
including the passes where a feature existed and still behaved wrong.

## License

MIT
