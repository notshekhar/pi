# Changelog

## [0.5.0] - 2026-06-18

### Changed

- **Renamed `pi` → `loop`.** The command is now `loop`, with `lp` (short alias) and `agent` also starting it. Config moved from `~/.pi` to `~/.loop`, and all `PI_*` environment variables are now `LOOP_*` (`LOOP_KEY`, `LOOP_DIR`, `LOOP_MCP_*`, `LOOP_SANDBOX_*`, `LOOP_FROM_SOURCE`, …). Package names are now `@notshekhar/loop{,-core,-tui,-sandbox}`.
- **Automatic, lossless config migration.** On first run, an existing `~/.pi` from a pre-0.5.0 install is moved to `~/.loop` (copied, then the old dir removed only after the copy succeeds) so auth, sessions, and settings carry over with no loss. Handled by both the installers and the app itself, so npm/source/binary installs are all covered.
- Build is now pure Bun — dropped the `tsc` declaration-emit step (and the `typescript` dependency); `bun build.ts` handles every package.

### Removed

- Dropped upstream `pi`-session compatibility: the `piCompatMode` setting and its special fork-on-open handling are gone. `loop` reads and writes its own sessions only. (The `/fork` command and per-entry forking are unaffected.)

## [0.4.5] - 2026-06-18

### Added

- Global instructions: `~/.loop/AGENTS.md` and `~/.loop/CLAUDE.md` are now loaded into workspace context in every session, regardless of the working directory — mirroring Claude's `~/.claude/CLAUDE.md`. Previously context files were only read from the cwd up to the repo root, so there was no place for user-wide rules. pi writes `AGENTS.md` by default, but a user-authored global `CLAUDE.md` is honored too. Workspace `AGENTS.md`/`CLAUDE.md` files still apply on top.

## [0.4.4] - 2026-06-18

### Added

- xAI **Composer 2.5** (`xai/composer-2.5`) is now in the model catalog — xAI's agentic coding model. It's callable via the xAI API even though it isn't listed by `/v1/models` (subscription/preview).

### Changed

- The thinking level is now hidden for models that don't reason. composer-2.5 reasons internally but rejects the `reasoningEffort` parameter, so the footer no longer shows a thinking level and `/thinking` reports "current model does not support thinking" — matching pi-mono, which gates both on the model's `reasoning` capability. (Also applies to other non-reasoning models like grok-3.)

## [0.3.51] - 2026-06-17

### Added

- The `read` tool now shows its line range in the tool title when called with `offset`/`limit` (e.g. `read src/app.ts:200-249`), so partial reads of large files are visible at a glance — matching pi-mono.
- README now documents the bash OS sandbox (the `sandbox` setting: network/filesystem boundaries, fail-open for normal agents vs. fail-closed for the read-only `plan` agent) and the bash denylist (`bashDeny`, wrapper/`sh -c`/substitution resolution, guardrail-not-a-sandbox).

### Changed

- Internal clean-up with no behavior change: the interactive app orchestrator and turn runner were split into focused modules (footer refresh, working indicator, ticker, turn-emitter wiring, subagent streaming), and a dead duplicated copy of the tool utils was removed.

## [0.3.50] - 2026-06-17

### Added

- OS-level sandbox for the bash tool, in a new `@notshekhar/loop-sandbox` package (ported from anthropic-experimental/sandbox-runtime, Apache-2.0). On macOS it generates a Seatbelt profile and runs commands under `sandbox-exec`; filesystem writes are confined to the working directory (+ temp), and network is deny / allow / per-domain allowlist (HTTP + SOCKS5 filtering proxies). Off by default — enable via `sandbox` in `~/.loop/settings.json`. Fails open with a warning for normal agents when it can't be enforced. (Linux bubblewrap + socat bridge + seccomp are written but UNVERIFIED; Windows is a stub.)
- The plan agent now gets the `bash` tool for read-only investigation — but only where the OS sandbox can enforce it (macOS/Linux). Its bash is forced into a fail-closed, kernel-enforced read-only sandbox (no writable cwd), so it physically cannot mutate the filesystem; on platforms without sandbox support, bash is withheld entirely. The same guarantee applies to any agent (or subagent) allowed bash but not write/edit.
- `/bashdeny` — an interactive, searchable UI to add/remove bash commands the agent is refused, also reachable from `/settings` ("bash denylist"). No more hand-editing JSON.

### Changed

- An unrecognized `/command` is now treated as a normal message to the model instead of erroring (fall-through), so messages that merely start with `/` just work.
- The bash denylist refusal is now a short 2-3 line message framed as the user's intentional policy. Denylist entries are plain command strings (the per-entry `reason` field was removed); legacy `{pattern,reason}` entries in existing settings are tolerated and migrated to strings on next edit.

## [0.3.49] - 2026-06-17

### Fixed

- The bash denylist now sees through command wrappers and proxies, closing the easiest bypass. `rtk git commit` (and `rtk proxy git commit`), `sudo`/`env`/`xargs` prefixes, interleaved `VAR=value` assignments, and inline `sh -c "…"` / `bash -c "…"` scripts all resolve to the real command before matching — so `rtk git commit` is blocked just like `git commit`. This raises the bar against accidental and lazy evasions; it is still a guardrail, not a sandbox, and a determined model can defeat string matching by design.

## [0.3.48] - 2026-06-17

### Added

- Configurable bash command denylist (`bashDeny` in `~/.loop/settings.json`). Entries match by command name, optionally plus a subcommand prefix (`"git commit"` blocks `git commit -m …` but not `git status`); a `{ "pattern": …, "reason": … }` form attaches guidance the agent sees. Matching resolves full paths to their basename (`/bin/rm` → `rm`), looks past leading env assignments and wrappers (`sudo`, `env`, `xargs`, …), and scans every command in a pipeline or `$(…)` substitution. When blocked, the agent gets a refusal framed as a deliberate user policy — naming the command and explicitly ruling out workarounds — so it stops and redirects instead of hunting for an equivalent. Defaults to blocking `git commit` and `git push` (commit/push stay with the human); set the key to `[]` to allow everything. This is a guardrail, not a sandbox — bypassable by a determined model, by design.

## [0.3.47] - 2026-06-17

### Changed

- An unrecognized `/command` is no longer rejected with `unknown command`. If the leading `/token` doesn't match a registered slash command, the input falls through and is sent to the model as a normal message — so messages that merely start with a slash (paths, options, off-hand `/notes`) just work. Registered commands, including one-shot `/<agent> <message>`, still run inline as before.

## [0.3.46] - 2026-06-16

### Fixed

- Internal anchor links in rendered markdown (e.g. `[Section](#heading)`) no longer render as broken clickable links. The terminal has no way to act on a `#fragment` target — it would try to "open" it as a URL — and the TUI has no app-owned viewport to scroll, so these now render as plain styled text. External `http(s)`/`mailto:` links are unchanged and still open from hyperlink-capable terminals.

## [0.3.26] - 2026-06-12

### Added

- Searchable model picker: type to filter (substring over id/name/description) — practical for OpenRouter's huge list. `+ add model…` registers any `provider/id` to `~/.loop/models.json`, usable immediately; a wrong id just errors at chat time. Custom models are marked and removable from the picker.
- Subagents are now a fork of the spawning agent by default: same system prompt (including workspace context and skills), same tools (minus `task` — no nesting), fresh context window. The turn's agent is what forks — including one-shot `/<agent> message` turns. Passing `agent` to the task tool still runs a named agent, with its tools capped to the parent's. This replaces the `subagent-tools:` cap config (frontmatter line is ignored if present, the `/agents` cap picker is gone): the parent's own tools are the cap, so delegation can never widen access with zero configuration.

### Fixed

- Subagent reports now actually reach the main agent. The AI SDK invokes `toModelOutput` with an options object (`{ toolCallId, input, output }`); we read the wrapper as the output, so every report degraded to the "(subagent finished without a final response)" placeholder — the main agent would dismiss the run and redo the work itself. Pinned with a regression test matching the SDK's exact call shape.
- Aborting a turn mid-run (Esc) no longer loses its cost on resume: both the main loop and subagent loop keep a per-step usage sum and persist it when the run never reaches `finish`, so a resumed session seeds the real spend instead of $0. (The lifetime/daily store was always abort-safe — it bills per step.)
- Subagent runs on Anthropic-shaped providers (including anthropic-sdk custom gateways) now use prompt caching. The subagent loop sent no `cache_control` breakpoints, so every step re-billed its entire accumulated context at full input price — quadratic in steps; one long run burned 2.4M uncached input tokens (~$7 on Sonnet). The system prompt is anchored once and a moving breakpoint re-anchors the last message every step, so each step re-reads prior context at the 90%-discounted cache price. The same per-step moving breakpoint now also applies to long multi-step main turns.

### Changed

- Subagents no longer bloat the main context: the parent model receives only the subagent's final report (bounded to 24k chars via the AI SDK `toModelOutput` pattern) — never the subagent's intermediate tool calls or file contents. The full activity log stays UI-only.
- Subagent activity is now stored as structured parts (text / reasoning / tool, in stream order) instead of one flat string — display order matches the real run, and renderers can style each kind independently. Old sessions with string activity still replay. The report handed to the parent is the AI SDK's final response text, with a stand-in when the subagent never produced one (abort, tool-only finish).

## [0.3.25] - 2026-06-12

### Added

- The `read` tool now also fetches URLs: pass an `http(s)://` URL and it returns the page as readable text (HTML stripped, truncated, timeout + size caps). Available to every agent that has `read` — including plan and subagents — with no extra tool to wire.
- `/settings → subagents` toggle: master on/off switch for the task tool (subagents). Off → no agent gets `task`.

### Fixed

- Subagent activity log (the streamed `> read …` tool lines) now persists with the run and replays on session resume, above the report — previously only the final report came back.

## [0.3.24] - 2026-06-11

### Added

- Subagent tool caps: every agent now has a second tool config — what the subagents it spawns may use (`subagent-tools:` frontmatter, asked in `/agents` create/edit when task is selected). The cap intersects with the target agent's own tools, so delegation can never widen access (verified: plan's subagents physically have no write/edit/bash even when targeting an unrestricted agent). `task` is now selectable per-agent, and the built-in plan agent can delegate while staying read-only end to end.
- Shift+Tab cycles agents anytime (even with text typed); plain Tab still cycles on an empty prompt and stays autocomplete while typing. Cycle = active custom agent plus all built-ins.
- Read-before-modify enforcement in the tools: `edit` rejects files not read this session and stale edits after on-disk changes; `write` guards overwrites of existing unread files while new files/paths pass freely. Session-scoped — nothing persists, `/new` clears the slate.

### Changed

- Sharper built-in prompts: default agent (verify-before-done working style, scope discipline), plan agent (investigation method + delegation, hard read-only rules), and subagent run rules (self-contained reports, no scope creep, honest partials)

- Performance: cost tracking writes one file per step instead of three (configstore rewrites the whole file per set), hook config merging is cached between events, subagent streaming coalesces repaints on a 50ms tick, and the auto-compact estimate no longer re-stringifies the whole history every turn
- Internals: agent core split into focused modules (subagent, tool-hooks, model-messages, events), turn events and settings access are fully typed (typos fail the build), and a `bun test` suite now covers hooks matching, agent files, cost seeding, compaction context, and changelog parsing (runs in CI)

## [0.3.23] - 2026-06-11

### Added

- Per-project model memory: the last model/provider picked in a folder is restored next time pi starts there (CLI flag and resumed sessions still win; global default remains the fallback). Applies to `pi run` too.
- Live cost, usage, and context: the footer updates after every step (each API round-trip), including subagent steps — and aborted turns keep the cost of completed steps

### Fixed

- Resumed sessions no longer show `$0.0000 · in:0 out:0 · ctx 0` until the next message — cost, token usage, and the context meter are restored from the transcript's usage entries on resume (startup `-s` and `/sessions` alike), without double-billing lifetime totals
- Subagent runs persist in the session: resuming replays the task box (agent, prompt, report), counts the subagent's tokens in the restored cost, and keeps the report in the model's context so it remembers subagent findings across resumes

### Changed

- Subagents run on the AI SDK's native `ToolLoopAgent` (same pattern as the official subagents guide); the task tool returns a plain-text report instead of JSON, expanding the task box shows the full activity log (tool calls with arg summaries) above the report, subagent input rewrites no longer leak into the main chat, and the model is instructed to call `task` alone in its step

## [0.3.19] - 2026-06-11

### Added

- Subagents: the `task` tool lets the main agent launch any named agent (default, plan, customs) for a self-contained job in its own context window — subagent activity streams live inside the task tool's box, usage/cost aggregates into the session totals, tool calls run the same hooks tagged with `agent_id`, and `SubagentStop` hooks fire on completion. Restricted agents (plan) don't get the task tool, and subagents can't nest. `subagentMaxSteps` setting caps the loop (default 50).
- Tab on an empty prompt toggles between built-in agents (default ⇆ plan); hinted in the footer (`agent default (tab ⇆)`), startup banner, and `/hotkeys`. Autocomplete keeps Tab while typing.
- Subagent rendering: the task tool gets a purple box with live state in the title (`task plan read · <prompt>` → `done`/`failed`), streamed activity collapses/expands like any tool output
- Model catalog refreshes at runtime: new models and pricing are re-fetched from models.dev on the hourly stale-while-revalidate cycle and warmed up at startup — release binaries keep learning about new models
- `/reload` is a hard reload: theme, commands, prompts, skills, agents re-read from disk, and the model catalog force-refreshed from the network
- `/hooks` management: lists every loaded hook with its source (pi-user, pi-project, claude-user, claude-plugins, claude-project), adds/removes pi-owned hooks in `~/.loop/settings.json`, and copies imported Claude hooks into pi so they keep working without a Claude Code install

### Fixed

- Anthropic `text content blocks must be non-empty` (400) on sessions with aborted turns: empty assistant messages are no longer persisted, and existing ones are filtered out of the model context on read

## [0.3.17] - 2026-06-11

### Added

- Per-agent tools: pick the allowed tool subset when creating or editing an agent (`/agents`, stored as `tools:` frontmatter in `~/.loop/agents/<name>.md`); the model only receives the allowed tools, and the system prompt lists exactly what's available
- `/plan` built-in agent: read-only planning agent (read, ls, grep, find) that explores the codebase and produces step-by-step implementation plans without being able to modify anything; prompt overridable like default, tool set fixed
- Built-in agents show their fixed tool set everywhere (agent list, action menu, edit flow) — visible but not editable
- Toggle-style multi-select for the tool picker: Enter or Space flips an entry with the cursor staying in place; "done" confirms

## [0.3.16] - 2026-06-11

### Added

- Custom agents: `/agents` creates, selects, edits, and deletes named system prompts (stored in `~/.loop/agents/<name>.md`); each registers as a `/<name>` command, and the built-in default prompt can be overridden and reset
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

- Claude Code–compatible lifecycle hooks (`SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Stop`, `SessionEnd`) with imports from `~/.claude/settings.json`, project `.claude`/`.loop` settings, and enabled Claude Code plugins (`${CLAUDE_PLUGIN_ROOT}` expansion included)
- Active hooks summary in the startup banner
- `/cost` detailed breakdown: session, current directory, today, last 7 days, this month, lifetime by provider
- Theme setting now applies: `/settings → theme` picks dark, light, or custom `~/.loop/agent/themes/*.json` and switches live
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
