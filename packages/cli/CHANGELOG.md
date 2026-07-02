# Changelog

## [0.8.0-canary.0] - 2026-07-02

> **Canary prerelease** — opt-in testing build for the new session storage engine. Stable installs are unaffected: `loop update`, the installer, and brew keep resolving the latest stable release. Install with `LOOP_VERSION=v0.8.0-canary.0 LOOP_FORCE=1 curl -fsSL https://raw.githubusercontent.com/notshekhar/loop/main/install.sh | bash`; when stable 0.8.0 lands, `loop update` moves you back to stable.

### Changed

- **Sessions now live in a single SQLite database (`~/.loop/loop.db`) instead of one JSONL file per transcript.** On first launch, every existing transcript is migrated automatically — your sessions, names, branches, and usage history all come along, and `/resume` ordering is preserved. The original `.jsonl` files are **left untouched on disk**, so downgrading to 0.7.x just works (new canary-era sessions won't appear there, but nothing is lost or rewritten). This retires the whole class of file-corruption bugs patched over the last releases — torn-tail writes, per-append lockfiles, cross-process lost updates — by construction: appends are transactions, `/resume` is an indexed query instead of a directory scan, and two loop processes can write concurrently under WAL. `/export` and `/import` still speak JSONL, unchanged.

## [0.7.19] - 2026-07-02

### Fixed

- **A crash mid-write can no longer corrupt the next message you send.** If loop (or the machine) died halfway through appending a transcript entry, the file was left with a torn final line and no trailing newline — and the _next_ append glued its JSON straight onto that fragment, so one crash silently destroyed a second, perfectly valid entry. Appends now check the file tail under the write lock and start on a fresh line, keeping the torn fragment isolated (recovery already skips it). Relatedly, a single corrupt line no longer hides an entire session from `/resume` — the picker now skips the bad line the same way session loading does, instead of dropping the whole transcript from the list.
- **Cost tracking is now safe across concurrent loop instances and corrupt stores.** Two sessions running at once could silently erase each other's lifetime/daily spend (each accumulated onto its own stale cache and rewrote the whole file); every write now re-reads the store first. A corrupted number in `cost.json` used to turn the lifetime total into `NaN` forever; stored values are now sanitized on read. Also fixed on resume: a step's usage could be double-billed if it produced two assistant messages, and the context meter adopted a _subagent's_ context size when the transcript ended on an aborted task — it now tracks the main conversation's last turn. (And the test suite no longer writes into your real `~/.loop/cost.json`.)
- **Extension installs fail fast and load reliably.** `loop install owner/repo#branch` actually installs that branch now (the `#ref` was silently dropped — you always got the default branch); incompatible or broken extensions are rejected at install time with the reason, instead of surfacing as a cryptic warning next session; the API compat check now treats 0.x minors as breaking (an extension built for API 0.1 no longer loads on the 0.3 host and misbehaves at runtime); a hung registry can no longer wedge `loop install` forever (5-minute timeout, and the installer's output now appears in error messages); crashed installs no longer leave `.staging-*` junk behind; and `/reload` genuinely reloads an edited extension — the module cache is busted per load, where before it silently re-activated the old code.
- **Extension host cleanups:** load warnings are replaced per reload instead of accumulating duplicates forever; overriding a built-in command no longer wipes the fields your override didn't specify (e.g. its description); and the extensions/settings stores re-read from disk before each write so two loop processes can't clobber each other's records.

### Added

- **`loop rpc stop`, and a daemon that cleans up after itself.** The socket daemon now removes its socket and pid file on SIGTERM/SIGINT, refuses to start when another live daemon already owns the pid file instead of silently stealing its socket, and `loop rpc stop` (previously "not implemented") terminates it via the pid file. Requests also wait for startup (extensions, commands) to finish, and sending to a session that already has a turn running is rejected cleanly instead of interleaving two turns into the same transcript.
- **`LOOP_DEBUG=1` breadcrumbs.** Errors loop deliberately swallows (best-effort persistence, corrupt-line skips) now leave a trace in `~/.loop/debug.log` when enabled — a failed transcript write was previously invisible, full stop.

### Changed

- **Session housekeeping is faster.** The `/resume` list no longer re-reads and re-parses every transcript on every open (per-file cache, invalidated by mtime — an append always busts it), and each turn step now persists all its messages under a single file lock and write instead of locking per message.

## [0.7.18] - 2026-07-01

### Added

- **`loop mcp` — manage MCP servers from the command line, like `claude mcp` / `codex mcp`.** Previously MCP servers could only be added through the interactive `/mcp` panel; you can now add and manage them in one shot from your shell. `loop mcp add --transport http docs https://code.claude.com/docs/mcp` adds a remote server; `loop mcp add fs -- npx -y @modelcontextprotocol/server-filesystem ~/code` adds a local stdio one (everything after `--` is the command, verbatim). Auth follows the usual conventions: `--header "Authorization: Bearer ${env:TOKEN}"` for static-header servers (the `${env:VAR}` placeholder resolves at connect time, so secrets stay out of the config file), `--oauth` for servers that use browser sign-in (then `loop mcp login <name>` runs discovery → consent → token exchange; `--client-id`/`--client-secret`/`--oauth-scopes` cover providers like Figma that block anonymous registration), and `--env KEY=VALUE` for stdio environments. `--scope user` (default, `~/.loop/settings.json`) or `--scope project` (`./.loop/mcp.json`, shareable via the repo). Rounded out with `list`, `get`, `remove`, `enable`/`disable`, `login`, and `add-json`.

### Fixed

- **The `/tree` view now renders tool steps legibly.** Tool rows previously showed an empty `[tool]:` and tool-call turns read as `assistant: (no content)`, because a tool result only stores a `toolCallId` reference and the renderer only knew how to display plain text. Each tool result now resolves back to its originating call and shows the same one-line summary as the live view — `read src/foo.ts`, `bash git status`, `grep TODO in src` — so a session's tool activity is readable and, crucially, you can once again see clearly where a branch was taken (the connector art was always there; it was just surrounded by blank rows). Tool rows are also searchable by tool name and arguments now.

### Changed

- **The `loop rpc` JSON-RPC server now streams the full turn.** It previously forwarded only 7 event types, so a client saw no reasoning/thinking, no subagent activity, no live per-step cost, and tool boxes that appeared late; it now forwards every turn event, and the list is checked at build time so it can't silently fall behind again. Added a `session.history` method (transcript replay for reconnecting clients) and a `server.info` capabilities handshake.

## [0.7.9] - 2026-06-28

### Added

- **`/steak` — a GitHub-contributions-style heatmap of your token usage.** A calendar wall of one square per day, shaded by total tokens (input + output) consumed, with relative quartile intensity so it reads the same whether you burn 10k or 10M a day. It's reconstructed from your session transcripts on disk, so the graph is full of history on first run rather than starting blank. `/steak` shows the trailing 52 weeks; `/steak <year>` (e.g. `/steak 2026`) shows a specific calendar year as a complete frame, with not-yet-happened days drawn as empty cells to fill in. The same heatmap now also leads the `/cost` output (trailing year), above the dollar breakdown.

## [0.7.7] - 2026-06-27

### Fixed

- **`/reload` now actually picks up MCP server and settings changes from disk.** The "hard reload" claimed to re-read every config surface but silently served stale data: it never refreshed the cached `settings.json` (the in-memory `CachedStore` is only invalidated via `refresh()`, which nothing called), and it never touched MCP at all. So servers added, removed, or edited in `settings.json` stayed invisible after `/reload`, and theme / `mcp` toggle / user-level hook edits made directly on disk were ignored — every `getSetting()` kept returning the value cached at startup. `/reload` now drops the settings cache first (so theme, hooks, MCP gating, and the server list all re-read from disk) and tears down + reconnects MCP (`close()` resets the manager so `init()` runs again instead of no-op'ing on its already-initialized flag). MCP reconnects in the background, same as the `/settings` mcp toggle.

## [0.7.6] - 2026-06-27

### Fixed

- **No more stray escape sequences in the shell after an interrupted exit.** The terminal-restore on teardown (kitty keyboard protocol + modifyOtherKeys) only ran on loop's normal exit path, so an _uncaught_ `SIGINT` — exit code 130, e.g. a Ctrl+C that lands during startup, while a child process owns the terminal, or forwarded by a parent like `bun run dev` — killed the process before the reset ran, leaving the protocols enabled and the shell echoing raw escapes like `^[[27;5;13~` on the next keypress. The TUI now installs a synchronous exit/signal safety net (`exit`/`SIGINT`/`SIGTERM`/`SIGHUP`) that restores the terminal via `fs.writeSync` even when the normal teardown never runs, then exits with the conventional `128+signo` status. (The earlier v0.7.1 fix made the reset unconditional but couldn't help when teardown wasn't reached at all.)

## [0.7.5] - 2026-06-27

### Added

- **Tool calls show a pending box the moment they start, then resolve to done.** Previously a tool with a large input — most visibly `write` (the full file content) and `edit` (the old/new strings) — only appeared once its entire input had finished streaming, so it popped in late instead of reading as in-progress. Every tool now renders a pending (grey) box as soon as the call begins (on the AI SDK's `tool-input-start`), fills in its arguments when they arrive, and turns green on completion — consistent across all tools.

### Fixed

- **`bash` commands can no longer run unbounded.** The timeout was optional with no default, so a command run without one — a hung process, a server left in the foreground — could run forever (in one case 30+ minutes). Bash now defaults to a **120-second** timeout, capped at **600 seconds**; the model can still request a longer timeout up to the cap for builds or installs. On timeout (or interrupt) the entire process tree is SIGKILLed, so nothing is left running in the background.

## [0.7.4] - 2026-06-27

### Changed

- **The thinking level now sits right after the model in every status-line layout.** It was previously scattered — after the context bar in `compact`, mid-dashboard in `vitals`, at the very end in `tokens`/`minimal` — and the `bar` layout omitted it entirely. Every layout (`compact`, `vitals`, `tokens`, `flex`, `powerline`, `minimal`, `bar`) now places it immediately next to the model. It stays gated on whether the model actually reasons and a non-off level being selected, so non-reasoning models (e.g. `composer-2.5`) still show nothing.

### Fixed

- **Errors now surface the real failure code instead of collapsing to a vague message.** loop reads the underlying syscall code — `EPERM`, `ECONNREFUSED`, `ENOENT`, `ETIMEDOUT` — and shows it like `fetch failed (ECONNREFUSED)`, walking the `.cause` chain that Bun/Node bury the real error inside (a flat read missed it). Machine-level failures (blocked permissions, refused network) are now diagnosable instead of reading as "unknown error". Provider/HTTP-status error formatting is unchanged.

## [0.7.3] - 2026-06-26

### Fixed

- **The `vitals` status-line layout no longer bloats memory.** Its background sampler spawned a `pmset` subprocess on every 1-second tick to read the battery level. Under Bun a per-second child-process spawn keeps inflating the allocator's high-water mark — RSS that's never returned to the OS — so a session sitting on the `vitals` layout crept from the usual ~50–60 MB up to ~190–200 MB. Battery has been removed from the dashboard entirely; the sampler is now a pure in-process read (`cpu` · `mem`) and never spawns a subprocess, keeping the layout at baseline memory.

## [0.7.2] - 2026-06-26

### Fixed

- **Reopened sessions no longer lose most of a turn's content.** A turn that used a tool (so the model answered across multiple steps) only ever persisted its first step — the final answer, and its token usage, were dropped on the way to disk. Reopening the session (or even the next turn in it) showed the turn truncated to the tool call. Every step's messages are now persisted, so the full turn survives a reopen and feeds back into the model's context intact.
- **Interrupting a response is now remembered.** When you interrupt mid-answer, the partial response is kept and the turn is marked interrupted, so the next turn's context tells the model its previous answer was cut off instead of silently dropping it. The `interrupted` flag (and the per-message model stamp used for cost) now survive a reload from disk.
- **Interrupted responses are no longer billed at $0.** The model provider charges for the input and the partial output of a request you interrupt, but the SDK reports no usage on abort, so loop counted it as free. The interrupted request's cost is now estimated — output from the partial text, input anchored to the adjacent real step's actual token/cache split — added to the session total (never the persistent lifetime/daily totals) and shown with a leading `~` so it reads as an estimate.

## [0.7.1] - 2026-06-26

### Added

- **Status-line layouts in the `statusline-themes` extension.** Beyond recoloring, the built-in now offers full custom layouts via `/statusline`: `compact` (model · context bar · tokens), `vitals` (a live dashboard with ctx% · tokens · cached · cache-hit% · cost · clock · cpu · mem · battery), `tokens` (in/out/cached/total economics), `flex` (a three-row powerline dashboard), `powerline`, `minimal`, and `bar`. Color themes (now `/statuscolor`) compose on top of any layout. Every layout leads with the selected agent and the model, gates the thinking level on whether the model actually reasons, and is responsive — dense layouts wrap onto extra rows and others shed lowest-priority segments so nothing runs off a narrow terminal.
- **`api.statusLine.refresh()`** lets an extension request a repaint for live fields (e.g. the vitals clock/CPU) that change without user action; no-op in print mode. `StatusLineContext` gained a `reasoning` flag. Extension API bumped to `0.3.0`.

### Fixed

- **No more stray escape sequences in the shell after Ctrl+C.** On teardown the terminal now resets the kitty keyboard protocol and modifyOtherKeys unconditionally instead of gating on tracked flags, which a fast exit racing the startup negotiation could leave out of sync — stranding modifyOtherKeys enabled so the shell echoed raw escapes like `^[[27;5;13~` on the next keypress.

## [0.6.4] - 2026-06-25

### Fixed

- **"Update available" notice now sits under the welcome banner.** The async update check used to append its line to chat history, so it landed at the bottom (below the conversation) whenever the network resolved. It's now shown as a line in the welcome masthead, and is preserved across `/new` and `/clear`.

## [0.6.3] - 2026-06-25

### Added

- **Extensions can drive interactive UI and auth.** New `api.ui` (`select` / `search` / `prompt` / `note` / `error`) gives extensions the same menus/prompts the built-in panels use, and `api.auth` (`getSecret` / `setSecret` / `openExternal` / `loopbackOAuth`) adds namespaced secret storage plus a localhost OAuth flow — enough to build the whole MCP feature as an extension. `api.ui` throws in non-interactive (`-p`) mode.
- **Customizable status line.** The block under the input box (formerly `CostFooter`, now `StatusLine`) is extensible via `api.statusLine.add(fn)` (append segments to a row) and `api.statusLine.transform(fn)` (rewrite the rendered rows). Contributors get a `StatusLineContext` (agent, model, session, cost, context, cwd, width) and are sandboxed so a throwing extension can't break the render.
- **New built-in extension: `statusline-themes`.** Enable it for a `/statusline` command that opens a searchable menu to recolor the status line — 12 themes including `matrix`, `ocean`, `sunset`, `synthwave`, `fire`, `rainbow`, plus `heat`/`neon`/`gold`/`cyber` adapted from the AKCodez status-line palette. `/statusline <name>` switches directly. Disabled by default.

## [0.6.2] - 2026-06-25

### Fixed

- **Tab is now reserved for completion.** While typing a slash command (or an `@` file reference), Tab completes it. Cycling through agents is now **Shift+Tab** only — plain Tab no longer cycles. This also fixes a bug where pressing Tab on terminals without the Kitty keyboard protocol opened the macOS file picker (Tab and Ctrl+I share the same byte, `0x09`).

## [0.6.1] - 2026-06-25

### Changed

- **Active extensions are visible.** Enabled extensions show in the startup status block (with workspace context) — e.g. `extensions: lsp · ponytail (full) · caveman (full) · rtk (on)` — and reappear after `/new` and `/clear`. Extensions can report a one-line status via `api.extension.setStatus`.
- `rtk` shows `rtk (no binary)` when the `rtk` CLI isn't installed, with `/rtk` linking to the installer.

## [0.6.0] - 2026-06-24

### Added

- **Extensions.** loop now has a JavaScript/TypeScript extension system — write or install extensions that add or override almost everything: slash commands, tools, providers and the models inside them, agents, skills, settings, the system prompt, and the turn loop. Extensions are plain Bun/TS packages (no build step) and may carry their own npm dependencies, resolved by the Bun runtime shipped inside the loop binary.
    - Install from npm, GitHub (`github:owner/repo`), or a local path: `loop install <spec>`, `loop link <path>` (dev), `loop list`, `loop enable`/`disable`, `loop remove`. In-session: the `/extensions` panel and `/install`.
    - Tool control: add/remove any tool (the default agent gets every tool automatically), `onCall` to rewrite or block a tool's input pre-execution, `onResult` to transform its output, and grant a tool to a restricted agent.
    - Providers: register a whole provider plus its models — declarative (OpenAI/Anthropic/Google-compatible) or imperative — appearing in `/model`, `/login`, and cost tracking like a built-in.
    - Turn middleware can shape the system prompt per agent, add/remove tools, and tweak provider options each turn.
    - Authoring guide: `read loop://docs/extensions.md`.
- **Built-in extensions** (pre-installed, disabled by default — enable with `loop enable <name>` or `/extensions`):
    - `lsp` — appends type/lint diagnostics after `write`/`edit` via auto-provisioned language servers.
    - `ponytail` — the "lazy senior dev" persona: write the minimal solution (`/ponytail lite|full|ultra`).
    - `caveman` — ultra-terse replies for fewer tokens (`/caveman lite|full|ultra|wenyan-…`).
    - `rtk` — rewrites bash commands through the `rtk` binary to compress output (no-op if `rtk` isn't installed).

## [0.5.3] - 2026-06-19

### Changed

- **Dropped the `lp` short alias.** `lp` collided with the preinstalled CUPS printer command (`/usr/bin/lp`), so depending on PATH order `lp` could run the printer instead of loop. The command is now just `loop` (with `agent` as the alias). On upgrade, the installer removes any old `lp` symlink and strips the `lp` shell alias a previous version may have added to your shell rc.

## [0.5.2] - 2026-06-19

### Fixed

- **`lp` no longer collides with the system printer.** The short `lp` alias shares a name with the preinstalled CUPS `lp` command (`/usr/bin/lp`); on machines where loop's bin dir sat behind `/usr/bin` in PATH, typing `lp` ran the printer instead of loop. The installer now adds an `lp` shell alias as a fallback when its symlink doesn't win the PATH lookup, and the install summary only advertises the command names that actually resolve to loop on your machine.

## [0.5.1] - 2026-06-18

### Added

- **Animated welcome banner.** Startup now shows a masthead with a pixelated `loop` ring on the left — a bright "comet" head chases its way clockwise around the square (corners filled), spins for two rotations, then settles into a static hollow ring that reads as a loop. Beside it: the greeting, model · session · agent, cwd, and the tips line. The banner also re-appears on `/new` and `/clear`, which previously left the screen empty.

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
