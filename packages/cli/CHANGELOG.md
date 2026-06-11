# Changelog

## [0.3.16] - 2026-06-11

### Added

- Custom agents: `/agents` creates, selects, edits, and deletes named system prompts (stored in `~/.pi/agents/<name>.md`); each registers as a `/<name>` command, and the built-in default prompt can be overridden and reset
- One-shot agent runs: `/<agent> <message>` runs that single message under the agent's prompt without changing the session's selected agent
- Slash commands highlight cyan in the input as you type, and executed commands echo highlighted into the chat
- Footer split into two rows: active agent + model on top, session/cost/context below

### Changed

- PreToolUse `updatedInput` rewrites (e.g. rtk's bash command compression) update the rendered tool call in place — the chat shows the command that actually executed, instead of a separate hook line
- Hook hardening: dispatcher never throws (corrupt config degrades to a warning), timeouts clamped, hook output capture capped at 1MB, chat-facing hook messages clipped, block decisions are strictly first-wins, Windows uses cmd.exe

## [0.3.15] - 2026-06-11

### Added

- Agent-state watcher support (herdr, Warp, …): `Notification`, `PermissionRequest`, `PreCompact` hook events, `terminalSequence` hook output for TUI-safe OSC notifications, `async` fire-and-forget hooks, parallel hook execution per event, and hook `statusMessage` shown in the loader while running
- `claudeHooksFilter` setting — allowlist which imported Claude Code hooks load (e.g. `["caveman", "herdr", "warp"]`); unset imports everything
- Errors always surface in chat: agent stream errors, slash-command failures, and uncaught exceptions render as red messages instead of disappearing

### Fixed

- `/changelog` and the what's-new banner work in release binaries — changelog content is embedded at build time (standalone binaries ship no CHANGELOG.md on disk)
- Hook `statusMessage` no longer prints a chat line on every prompt; it rides the loader while the hook runs

## [0.3.14] - 2026-06-11

### Added

- Claude Code–compatible lifecycle hooks (`SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Stop`, `SessionEnd`) with imports from `~/.claude/settings.json`, project `.claude`/`.pi` settings, and enabled Claude Code plugins (`${CLAUDE_PLUGIN_ROOT}` expansion included)
- Active hooks summary in the startup banner
- `/cost` detailed breakdown: session, current directory, today, last 7 days, this month, lifetime by provider
- Theme setting now applies: `/settings → theme` picks dark, light, or custom `~/.pi/agent/themes/*.json` and switches live
- `/changelog` shows release notes; new entries appear once after an upgrade
- `bun run format` (prettier) for the monorepo

### Changed

- Hook messages render with an orange accent, separate from tool grey/green
- Session-start hook context collapses to a one-line notice instead of rendering inside the first user message

### Fixed

- Hook commands exiting without reading stdin no longer crash the TUI (EPIPE)
- Hook timeouts now kill the whole process group, not just the shell
- Hook payloads match Claude Code field names (`tool_response`, `transcript_path`, `stop_hook_active`, `source`)
- Hook-injected context lands on the latest user message even when images are attached

## [0.3.13] - 2026-06-10

- Releases up to here predate changelog tracking: interactive TUI (`pi`), print mode (`pi -p`), sessions with fork/resume, multi-provider models via Vercel AI SDK v6, auto-compaction, cost tracking, project skills, workspace context
