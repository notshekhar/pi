# pi-agent → loop parity

What loop does, what pi-agent does, and what is left. The target is **noir
only** — loop's default look — with night/day as the two themes inside it.
There is no `loop` UI mode to build and no mode-plugin registry: noir *is* the
renderer.

Legend: **[x]** done · **[~]** partial · **[ ]** not started · **[—]** out of scope

Scale, honestly: loop is ~33k lines of TS across `packages/tui` + `packages/cli`
+ `packages/core`. pi-agent is ~9k lines of Go (excluding the vendored model
layer). The renderer foundations are done; the transcript and the command
surface are the bulk of what remains.

---

## 0. Architecture

| | loop | pi-agent | |
|---|---|---|---|
| Language | TypeScript / Bun | Go | |
| Model layer | Vercel AI SDK v7 | vendored, `modules/ai` | [x] |
| Layout | `packages/{tui,core,cli,…}` | `internal/modules/{ai,tui,core}` | [x] |
| Dependencies | ~40 npm | **zero** | [x] |
| Binary | compiled Bun | `go build` | [x] |

Module boundaries, enforced by the import graph:

```
ai   → (nothing)          the model layer, self-contained
tui  → ai                 rendering; knows nothing of the agent
core → ai                 agent/tools/session; knows nothing of the terminal
cmd  → ai, tui, core      the app that wires them
```

---

## 1. Text rendering foundations

The layer everything else draws on. **Done.**

- [x] `visibleWidth` — ANSI-free, tab=3, real cell widths
- [x] Grapheme clustering (UAX #29 subset: marks, ZWJ, regional pairs, keycaps)
- [x] East Asian Wide/Fullwidth table — CJK is two cells
- [x] Combining marks are zero-cell; Indic spacing marks (Mc) are one
- [x] ZWJ emoji sequences are one glyph, not the sum of their bases
- [x] Width modes: per-codepoint sum / shaped / clamped
- [x] `wrapTextWithAnsi` — word wrap carrying SGR state across breaks
- [x] `truncateToWidth`, `fitRow` (the row-overflow crash guard), `padToWidth`
- [x] `applyBackgroundToLine` — wash reaches the right edge
- [x] OSC 8 / truecolor capability probe
- [x] CPR width probe — three canaries, deadline-bounded, before the key decoder

## 2. Markdown

**Done.** A real lexer, not regex substitution.

Blocks:
- [x] ATX + setext headings
- [x] Paragraphs with lazy continuation
- [x] Fenced code (incl. **unclosed fences**, the streaming case) + indented code
- [x] Lists: nested, ordered w/ `start`, task checkboxes, tight vs loose
- [x] Blockquotes containing block content
- [x] GFM tables with per-column alignment
- [x] Thematic breaks, HTML blocks, link reference definitions

Inline:
- [x] Emphasis via CommonMark's **delimiter-stack algorithm** (`*` vs `_` flanking, rule of three)
- [x] Codespans (markup inside stays literal)
- [x] Links: inline, reference, collapsed, autolink, bare URL, `mailto:`
- [x] Images (alt text + URL), strikethrough, hard/soft breaks, escapes, inline HTML

Rendering:
- [x] Width-aware table layout with graceful narrow-terminal degradation
- [x] Blockquote border + style re-entry after nested resets
- [x] OSC 8 hyperlinks, with URL-in-parens fallback
- [x] Anchor links render as text (nothing to click, no viewport to scroll)
- [x] **Streaming freeze** — settled prefix never re-lexed; verified against source at runtime
- [x] Partial-closing-fence trimming (no flicker as ``` arrives)
- [x] Freeze never cuts across a list (would renumber an ordered list from 1)
- [x] 53 markdown tests incl. streamed-in-chunks ≡ rendered-whole

## 3. Colour & theme

- [x] Slot-based theme — renderers name roles, never hex
- [x] Noir **night** + **day** palettes (loop's exact values)
- [x] Derived surfaces via `Mix`/tint; `Luminance`
- [x] Truecolor with 256-colour cube fallback
- [x] Canvas wash (OSC 11/10) + restore
- [x] Syntax highlighting — go/js/ts/python/rust/sh/json + generic
- [x] `/theme` command to switch night ↔ day at runtime
- [x] Auto light/dark — COLORFGBG then an OSC 11 query
- [x] User themes from JSON — `~/.pi-agent/themes/*.json`, primitives in, derivation shared

## 4. The noir transcript

**Done.**

- [x] Animation clock — 20fps, sin² wave + pulse curves, `accentAtBrightness`
- [x] **Rail** — `┃`/`│` column, `RailWidth` 3, `WithRail`
- [x] Rail motion: wave while running, pulse while blocked, static flash on finish, settled outcome colour held toward canvas
- [x] **`◆` diamond rows** — yellow running / green ok / red failed / muted folded
- [x] Bullet synced to the head of its own rail's wave
- [x] **Thinking as its own block** — 3-line live tail → `◆ Thought for 4.2s`, foldable
- [x] `❯` user prompts with right-aligned timestamp
- [x] Turn summary line
- [x] Block gaps — every block owns exactly one leading blank
- [x] Per-entry render caching; the frame clock only runs while something is live
- [x] Prose renders railless — it is content, not an event
- [x] User prompt is a filled PANEL — raised background across a column and a row of padding, not bare text
- [x] Nav selection spine — `▌` down every line of the selected block, blanks included, replacing the first cell rather than shifting the text
- [x] **Inline rendering** — the live region is painted into the normal screen buffer and grows downward, as loop's does; the alternate screen is never entered, so the masthead starts at the top with the composer beneath it, and the chrome is erased on exit leaving the conversation behind
- [x] Block retirement — a finished block above the fold is printed above the live region and dropped from the model, so it becomes real terminal scrollback (verified: 35 lines of history after four turns on a 24-row terminal)
- [x] Layout gaps — `history → Spacer(2) → todo → composer → status → Spacer(1)`, with the working indicator occupying the two-line gap rather than adding to it

## 5. Tool rendering

**Done**, except grouping.

- [x] Per-tool arg summaries (`read src/app.ts:120-180`, `grep pat in dir`)
- [x] Output hidden when folded, shown on expand — failures fold too
- [x] **Diff colouring** for edit/write
- [x] **Absolute line-number gutters** for read output, aligned, notice-aware
- [x] Streaming tool-input tail, decoded out of half-arrived JSON
- [x] Interrupted state; open calls settled when a turn is cut short
- [x] Grouping runs of finished calls (`Read 3 files`) — a second fold level
      above each call's own expand, reopened with nav's `→`

## 6. Input & editor

Mostly inherited from what was already built.

- [x] Multiline editor, Alt+Enter newline
- [x] History on ↑/↓
- [x] `/command` and `@path` completion with inline menu
- [x] `!` shell escape
- [x] Bracketed paste
- [x] Word navigation, kill-ring basics (ctrl+a/e/k/u/w)
- [x] Esc interrupts a running turn
- [x] PageUp/PageDown scrollback
- [~] Keys: 26 handled vs loop's ~35 bindings
- [x] Undo stack — ctrl+z / ctrl+y, snapshot-based, coalescing by edit kind
- [x] Image drag-drop attachment — path-based, unquotes the three forms terminals send
- [x] Configurable keybindings — `~/.pi-agent/keybindings.json`; essential keys are not rebindable

## 7. Modes

- [x] **Nav mode** (ctrl+e) — transcript takes the keyboard, ↑/↓ select, → expand, ← collapse
- [x] Expand-all toggle (`e`), NAV shown in the status bar, cursor hidden while navigating
- [x] **Plan mode** — enforced read-only, `/plan`, PLAN badge in the status bar
- [x] **Goal mode** — planner → work → adversarial verifier with read-only tools, bounded retries
- [x] Ask-user review screen — the `ask` tool's picker

## 8. Slash commands — 69 registered, plus three DYNAMIC sources

All three consumers — dispatch, `/help`, and the `/` palette — are generated
from ONE table in `cmd/pi/registry.go`. They used to be three
hand-maintained lists and had drifted: the palette was missing a third of the
commands the switch accepted, and `/help` described `/clear` as keeping the
session when it does not. A command that is not in all three is a command
nobody finds.

**Behaviour brought to loop's, not just the names**
- [x] `/clear` — clears the screen AND starts a new session (it used to keep it)
- [x] Enter on a slash completion ACCEPTS AND RUNS it; Tab accepts without running; Enter on an `@file` completion accepts and stays in the draft
- [x] An exact command name always sorts first — typing `/session` used to run `/sessions`
- [x] `/fork` — branches from a CHOSEN earlier prompt; `/clone` is the whole-session copy
- [x] `/tree` — a real fork tree that nests branches and switches to one, backed by `Parent`/`ForkedAt` in the session header
- [x] `/export` — format by extension: `.md`, `.jsonl` (round-trips with `/import`), `.html`
- [x] `/share` — secret GitHub gist via the `gh` CLI, url copied to the clipboard
- [x] `/cost all` — every stored session, read from headers alone

**Commands added to close the gap**
- [x] `/session` — id, title, model, cwd, message count, tokens, spend
- [x] `/timer` — countdown in the status line; it is also what keeps the frame clock running with nothing else animating
- [x] `/reminder` `/reminders` — one-time or repeating, persisted, fired by the daemon; a repeat missed while the machine slept fires once and rolls past now
- [x] `/daemon` — the scheduler, OFF by default because it can start turns that cost money
- [x] `/background` `/bg` — a full agent turn in its OWN session, reporting when it finishes
- [x] `/steak` — token heatmap, GitHub-contributions style, built from session headers
- [x] `/attach` — image by path or from the clipboard; `/paste` attaches an image or drafts text
- [x] `/import` — a JSONL session, validated before anything is created

**Aliases loop teaches, so the name a user knows always works**
- [x] `/effort` `/resume` `/sessions` `/name` `/rename` `/cwd` `/bg` `/reminders`
      `/exit` `/release-notes` `/models` `/providers` `/perms` `/config` `/skill`

**Commands that are not in the table** (2026-08-19) — loop registers three
more sources at startup, and their absence read as the features not existing:
- [x] `/skill:<name>` — every installed skill, callable directly
- [x] `/<agent>` — one message under an agent's prompt, without switching
- [x] `/<prompt>` — every `.md` under `~/.pi-agent/prompts`
Resolved on every dispatch, not snapshotted at boot, so a skill written during
the session is callable without a restart. `/help` and the palette list them.

**Out of scope, with reasons**
- [x] `/extensions` `/install` — see §12
- [—] `/datasource` `/data` — database drivers are dependencies by definition
- [—] `/artifacts` — needs a publishing target
- [—] `/update` — needs a release pipeline this repo does not have

## 9. Tools — 12 of 16

- [x] `read` `write` `edit` `bash` `ls` `grep` `glob`
- [x] `todo` — task list + pinned panel above the editor, retires at turn end
- [x] `task` — subagents with their own context window; read-only, cannot
      recurse, cannot prompt; live status on the parent's row
- [x] `websearch` — DuckDuckGo HTML scrape, opt-in, ad-filtered
- [x] `memory` — per-repo markdown facts; the INDEX is injected, never bodies
- [x] `skill` — user + project directories, project wins; index-only injection
- [x] `plan` — presents a plan and waits for approval
- [x] `ask` — structured user questions
- [—] `artifact` — needs a publishing target
- [—] `sql` / datasources — database drivers are dependencies by definition
- [x] `find` — walks the tree, unlike glob

Notes:
- [x] read-before-edit registry — pi-agent has this, and it's the *fixed* version
- [x] Fuzzy edit matcher rewrites only the matched span (loop's inherited bug is already avoided)

## 10. Providers & models

- [x] Catalog with `provider/model` full ids
- [x] Anthropic, Google, Vertex, Bedrock, DeepSeek, Kimi, OpenAI-compatible
- [x] Kimi key-kind switching (`sk-` vs `sk-kimi-`)
- [x] Reasoning levels (none→xhigh)
- [x] Reads `~/.loop/auth.json`
- [x] Model and provider choices persist across restarts
- [x] xAI, OpenRouter, Vercel gateway, Groq, Cerebras, Mistral, GLM, z.ai, ZenMux, Ollama — 16 providers wired
- [—] OAuth login flows — needs a local callback server and per-provider flows
- [x] Custom providers — any OpenAI-compatible endpoint via `/provider add`
- [x] models.dev catalog — 777 models embedded; curated seed keeps the defaults
- [x] Cost & token accounting per turn — live in the status line and the turn summary

## 11. Sessions & persistence

**Done**, on SQLite — loop's arrangement. This was JSONL files until
2026-08-19; see §14 for why it moved and what the move had to preserve.

- [x] Full round-trip codec — tool calls, results, and **signed reasoning**
      (`ProviderOptions`); an unrepresentable part is an error, never a silent drop
- [x] One WAL database (`~/.pi-agent/agent.db`), opened in exactly one place
- [x] Append-only writes: one turn, one transaction, no rewrite
- [x] One-time import of the file-era sessions, leaving the files in place
- [x] Resume — replays the transcript, resets the read-before-edit registry
- [x] Fork, rename, delete; titles derived from the first prompt
- [x] A torn final line in an imported file survives; an undecodable stored
      message is reported rather than resumed with a hole in it
- [x] The import skips an unreadable session instead of failing the lot
- [x] Compaction, export to markdown, token estimate
- [x] `/tree` — sessions grouped by repository
- [x] Auto-compaction at 80% of the window, driven by the REAL input-token
      count from the last turn's usage rather than the chars÷4 estimate
- [x] Usage + cost history — a per-turn cost ledger behind `/cost`, plus
      session totals for a listing that must not touch it

## 12. Larger subsystems — none started

Each is a project in its own right; listed so the map is complete.

- [x] Settings file + `/settings` UI — 6 keys, validated before writing, applied
      live where possible. loop has ~40 keys to this handful
- [x] Permissions — allow/ask/deny rules with globs, an always-on deny list,
      cwd write confinement, an approval prompt, and `/permissions` to inspect
      and extend. Order-independent: the strictest matching rule wins
- [x] MCP client — stdio + HTTP transports, handshake, paginated discovery,
      tools bridged through the SAME permissions policy, `/mcp`. No OAuth,
      no prompts/resources/sampling
- [x] Extensions — Go, not JS (see the package doc for why `plugin.Open` is
      not an option). The SEAMS all work now: an extension contributes tools,
      commands, a system-prompt fragment, status-line transforms, a status
      word, tool-call REWRITES, and appends to a tool's RESULT; it has its own
      namespaced settings and is told about a prompt before the model is.
      OPT-IN like loop's — a builtin that shipped enabled would have `caveman`
      rewriting every reply unasked. Panel is loop's: install row, `●`/`○`,
      per-extension enable/disable/info.
      **All six of loop's builtins are ported**, plus pi's own `git`:
      - `caveman`, `ponytail` — skill text embedded verbatim, mode filtering,
        the deactivation phrase
      - `rtk` — `git status` → `rtk git status`, the full exit-code protocol
        (2 = deny, 3 = ask-and-still-rewrite), a silent no-op without the
        binary. Verified against the real rtk 0.42.4
      - `statusline-themes` — 8 layouts × 12 colour themes, composing; the
        vitals sampler reads /proc on Linux and sysctl + vm_stat on macOS,
        never during a repaint
      - `lsp` — the LSP client, the 37-server table, ambient diagnostics after
        write/edit, and the 9-operation `lsp` tool. Verified end-to-end
        against real clangd
      Not ported: loop's server PROVISIONING (npm, `go install`, prebuilt
      downloads). Discovery is node_modules/.bin then PATH — loop's own first
      choice — so on a machine with the toolchain installed the behaviour is
      identical; where nothing is found the tool names the server it wanted
- [x] Skills — `SKILL.md` directories under `~/.pi-agent/skills` and
      `<repo>/.pi-agent/skills`; `/skills` to list and read
- [x] Subagents — see §9 `task`
- [x] Hooks — **Claude Code compatible** (2026-08-19): all ten of loop's
      events, per-tool matchers, JSON payload on stdin, and the exit-code
      contract, so an existing hook script ports unchanged. Hooks CAN veto:
      exit 2 refuses the action with stderr as the reason, and PreToolUse is
      routed through the approval path so it sees calls the policy allows.
      Parallel with a first-wins merge in config order; the old 4-event
      spelling still loads
- [x] Memory — `~/.pi-agent/memory/<repo-slug>/`, index-only injection, `/memory` to inspect
- [x] Gateways — Telegram via long-polling; chat-id restricted
- [x] `serve` — HTTP+SSE rather than WS (no hand-rolled RFC 6455); loopback + bearer token
- [x] Sandbox — `sandbox-exec` on macOS, denies writes outside the workspace; honest "no" elsewhere
- [—] Desktop app (Electron) — not a Go target
- [—] Web UI (`apps/web`) — separate product

---

## 13. The 2026-08-19 parity pass

Everything above was already ticked, and the app still did not behave like
loop. The reason is worth stating: a checklist tracks whether a feature
EXISTS, and every gap in this pass was a feature that existed and behaved
differently. They were found by running both binaries side by side under
`scripts/screenshot.py`, not by reading either one's source.

**The picker family was one component; loop has three.** `selectOnce` (no
filter box — a six-row menu that advertises search is offering an interaction
that cannot pay off), `searchOnce`, and `toggleOnce` (multi-select, Space
flips, a `done` row confirms). Added `Select` and `Toggle` beside `Pick`, plus
a `Prompt` — a one-line text input, which did not exist at all and without
which a whole class of flows could only take their argument on the command
line. The row layout is now loop's: a FIXED 32-column label column, so the
description column does not jump left and right as a filter narrows the list.

**Every panel closed after one action.** loop's panels loop: list → act →
back to the list, reopening on the row you last touched. `manage()` is that
loop, and a panel opened from inside a panel runs inline on the goroutine
already waiting for it (`inPanel`) — two loops both painting would each get
half the keyboard. `/settings` rows now open `/permissions`, `/bashdeny`,
`/hooks` and the scoped-model panel as sub-panels and come back.

**Settings rows showed the value and dropped the help.** Key in the label,
value in the description meant the panel never said what `askUser` was for.
Now `key: value` with the help beside it, booleans toggling in place, free
text asking through the prompt.

**Things a setting claimed to do and did not.** `workspaceContext` gated
nothing — AGENTS.md was listed at startup and never injected. `/plan <task>`
was documented and rejected. `/logout` said "opens picker" and printed usage.
`/clear` said it started a new session and only wiped the screen.

**`/scoped-models` meant the wrong thing.** loop's is the ctrl+p CYCLE list;
pi-agent's was per-job overrides. Both now exist, under the right names —
the cycle list on `/scoped-models`, the overrides as `subagentModel` and
`compactModel` settings rows.

**Sessions were written at startup.** Every launch left an empty session
behind, and the list filled with conversations nobody had. Files are now
claimed on the first message; `/new` starts a fresh one rather than resetting
the current one in place.

**Reminders were intervals; loop's are cron.** `internal/modules/core/schedule`
is a zero-dep parser — the hard part is not the fields, it is the standard's
day-of-month-OR-day-of-week rule, which is tested.

**`/context` and `/cost` were four lines each.** Both are now loop's: a 10×20
grid coloured by category with the legend pinned beside it, and the usage wall
plus session/directory/today/7d/month/lifetime with a per-provider split.

**Keys that did not exist.** ctrl+g (send "continue"), ctrl+l, ctrl+p,
shift+tab (cycle agent — the status line was already advertising it), and `#`
as a second file-completion trigger.

**Agents were subagents.** `explore` and `review` are things you DELEGATE to;
loop's session-agent list is `default`, `plan`, and the user's own files.
Separated, with `plan` arming plan mode when selected and disarming only when
the cycle is what armed it.

**Extensions existed and did nothing.** `ToolProvider` and `PromptProvider`
were declared and never consulted — an extension could only add a command.
Wired up, plus per-extension settings, a status word for the startup line, and
a turn observer. Flipped to OPT-IN: on-by-default is defensible for a `git`
helper and indefensible for `caveman`, which rewrites every reply. `caveman`
and `ponytail` ported verbatim.

**Three seams the extension API was missing**, each found by trying to port
the extension that needed it:
- tool-call input REWRITING, threaded through the vendored model layer as
  `ApprovalDecision.UpdatedInput`. The policy re-judges a rewritten call, or
  the seam would be a way around the permission rules; hooks run before
  extensions, so a rewrite can never slip past a refusal
- status-line TRANSFORMS, a chain over the rendered rows — it has to be able
  to REPLACE them, because that is what a layout preset does
- appending to a tool RESULT, done by wrapping the tool rather than by a hook
  inside the model layer: a wrapper is ordinary code and cannot be forgotten
  by a new code path

**Commands that printed a listing where loop opens a MENU.** Found by running
every no-argument command and reading the screen, not by reading the registry —
each of these had a description promising a picker and a body that printed
text. `/statusline`, `/statuscolor`, `/login`, `/mcp`, `/gateways`, `/memory`.
The extension ones needed a seam of their own: `extension.UI`, declared in the
extension package and implemented by the app, so a command can open the app's
picker without this module importing the terminal.

Two landmines behind them, both now closed:
- `manage()` returned SILENTLY when a panel had no rows, so `/background` on a
  fresh session opened nothing and said nothing. Panels always offer their
  `+ add` row, and an empty one announces itself.
- `/memory` listed the agent's stored facts while its own description promised
  loop's AGENTS.md editor. It is the editor now (`App.Suspend` hands the
  terminal to `$EDITOR` and takes it back); the facts are `/memory list`.

**The spinner was not a clock.** `Loader.Tick` incremented a counter on every
call, and the app repaints on every keystroke and every mutation — so the
loader's speed was the RENDER rate: a blur while a response streamed, a frame
per keypress while scrolling, and its intended speed only when nothing else
was happening. The frame comes from elapsed time now, and a test pins that a
hundred paints in the same instant show one frame. The rails were always
right; they read the shared animation clock.

**`pinnedInput`** — loop's setting, and it was missing. It pads a SHORT
transcript so the composer lands on the last rows instead of floating under
the masthead. Padding is all it does: no clipping, no scroll capture, no mouse
reporting — asking for the wheel is exactly what stops a terminal
drag-selecting text, so the scrollback, the wheel and the selection stay the
terminal's.

`inlineWriter.Reset()` is not a "the layout changed" call — a lesson learned
by shipping pinning with one. It forgets where the region IS, so the next
frame lands below the old rather than over it, and the masthead appeared
twice. Paint already repaints a region that grew or shrank, in place; Reset
belongs only where the region genuinely moved out from under us (Suspend,
resize, Wipe). Pinned by a test.

Still open, and each a decision rather than a gap: loop's `/update`,
`/artifacts`, `/datasource`, and LSP server provisioning (see §12).

---

## 14. The renderer pass (2026-08-19)

The screens matched and the app still did not FEEL like loop, which is the
same lesson as §13 one layer down: §1 ticked "inline region, cursor
arithmetic, retirement" and every one of those was true. What was missing was
not a feature, it was the two mechanisms that decide how much of the terminal
a frame costs. loop's TUI header says "differential rendering" in its first
line; pi-agent repainted the whole region, every frame, and painted a frame
per event.

**Painting is now capped at 60fps and the frame is a DIFF.**

- `FrameInterval` (16ms) is a ceiling, not a schedule: an idle prompt still
  paints nothing at all. What it collapses is the burst — every streaming
  delta, every keystroke of a paste, and every tool status arrives as its own
  event, and painting each one meant a model emitting 300 tokens a second
  asked the terminal for 300 frames a second. `drain()` takes everything else
  already queued into the same frame.
- `inlineWriter` keeps the frame on screen in `prev` and rewrites only the
  span from the first changed row to the last. A streaming token changes one
  row; a spinner changes one row of chrome.
- Every frame is wrapped in synchronized output (DECSET 2026), so a repaint
  is never presented half-drawn.

Measured over one turn (a 60-line answer, 100×40 pty, bytes off the pty):

| | startup | turn |
|---|---|---|
| repaint every frame, every event | 25,678 | 2,719,852 |
| diff only | 8,103 | 254,968 |
| diff + 60fps | 8,173 | **43,498** |

62× less output for an identical screen. It is invisible locally and it is the
whole experience over ssh, which is where a screenful per token turns a
stream into a slideshow.

Four things the diff had to be taught, each one a bug first:

- **An appended row is never "unchanged", even when it is blank.** Nothing has
  been printed there, so the region has to physically grow to reach it.
  Without it the row count grew and the region did not, and every later cursor
  move was off by that many rows.
- **A row can only be reached by moving down while it EXISTS.** Past the
  region's bottom, `CUD` stops at the last row of the screen and the frame
  silently lands short; a newline scrolls when it has to. That is `moveTo`.
- **Rows that changed only because the frame got SHORTER have nothing to
  write.** They are blanked, not painted — clamping the span is what stops an
  index walking off the end of the new frame.
- **A commit does not invalidate the baseline.** Committed rows are written
  over the TOP of the old region, so what survives is `prev[len(commit):]` —
  positionally, whatever was retired. A frame that hands a block to scrollback
  still only repaints what moved.

**`Invalidate` vs `Reset`.** §13 established that `Reset` means "the region
moved out from under us". The recovery path needs the other half: the region
is where we left it but what is printed in it is not trusted. `Reset` there
writes the recovery frame BELOW the damaged one.

**A render that panics no longer kills the session.** It costs one damaged
frame and an `Invalidate`; the failed frame is deliberately not re-requested,
because a render that panics on some content panics on it again and asking
again turns one bug into a loop that burns the budget forever without
drawing. `PI_RENDER_LOG` records it — the one place it must never appear is
the screen.

**A pre-existing decoder bug fell out of the test run.** `readMore` returns
`pending` + the new bytes, and the caller appended it to `pending` AGAIN, so
every escape sequence arriving in two reads decoded as `\x1b\x1b[B`: a
spurious lone Esc, then the real key. Over ssh, where a split sequence is
normal, that Esc closed whatever menu the arrow was meant to move through.
The repo's own test pinned it and was failing.

### Live mode

loop's `uiLive`, and it did not exist. pi-agent folded runs of finished tool
calls in nav mode only. Live is a STATE of noir — the canvas, palette and
frame are identical — that holds the folds while you type: "Read 3 files"
instead of three rows. Navigation folds regardless, so entering and leaving it
does not disturb the setting, which is the bug loop had to fix: leaving nav
turned live off unconditionally and the transcript appeared to un-fold itself.

### herdr

loop reports pane state to herdr (§12 listed it as not started); pi-agent
reported nothing, so a herdr sidebar could only see a process producing bytes
— which is exactly what does not distinguish "thinking" from "waiting for
you".

- `core/status` is the bus: idle / working / blocked, fed by the two
  authorities that already know — the working indicator, and the prompts where
  the AGENT stops and waits (`Ask`, `AskChoice`). A menu the user opened
  themselves is not the agent being blocked, and does not report. Transitions
  UP publish at once; DOWN settle 250ms, because the seams flicker naturally
  (an ask flow reopening a prompt per question) and every blip would otherwise
  publish "idle" and take it back.
- `core/herdr` speaks `pane.report_agent` / `report_agent_session` /
  `release_agent` as `custom:pi-agent`, hard-gated on `HERDR_ENV=1` +
  `HERDR_SOCKET_PATH` + `HERDR_PANE_ID`. One connection per request, on a
  queue, off the caller's goroutine, dropped when the queue is full: a herdr
  server that is dead or wedged must not cost the render loop a millisecond.
  Verified end-to-end against a real socket in a pty — idle at launch,
  working on the turn, the session id announced when the session claims it,
  idle at the end.

The TUI stays ignorant of all of it: `App.SetStatusHooks` is two callbacks,
and `cmd` is the only thing that knows both sides. Same rule as `agent.Consume`
in §0.

### The todo panel

It was pi-agent's own design and it read as one: ` plan · 2/5` over rows of
`○ ◐ ●`. loop's is a checklist — a ruled `─ todos (2/5) ─────` header and
`[x] [>] [ ] [-]` rows — and the brackets are the better half of the trade,
because a markdown checklist is notation everyone already reads while
geometric glyphs are a key you have to learn per app. Screens diffed against
loop running the same prompt, which is the only way any of this was ever
found.

Three things the look needed underneath it:

- **`activeForm`.** The in-progress row says what is HAPPENING — "Reading
  PARITY.md", not "Read PARITY.md". The imperative there reads as an
  instruction nobody has picked up. The tool takes it per item now.
- **`cancelled`.** Struck through and kept. Without it the model's only ways
  to drop a step are deleting it, which loses the fact that it was ever
  planned, or marking it completed, which is a lie the user cannot catch.
- **Clipping by PRIORITY, not by a window.** In-progress first, then pending,
  then the most recent finished — drawn in the plan's own order. The old
  sliding window started at the first unfinished item, so a plan finished out
  of order pushed the running step off the panel.

And the panel now RETIRES at the end of the turn that owned it, leaving one
line behind — `todos: 3 of 7 open`. loop's rule: the panel is chrome, pinned
above the composer because it belongs to the work in flight, and a plan that
outlives its turn sits there describing a job nobody is doing. The agent's own
list is not cleared, so a follow-up turn resumes the same plan.

### SQLite

The one external dependency (`mattn/go-sqlite3`), taken on purpose — see
AGENTS.md. Sessions were two files each; they are rows now, in one WAL
database that `core/db` alone opens.

What the files could not do is answer anything ACROSS sessions without
reading all of them. `/cost` is the visible case: its buckets were derived
from session totals, so a session's whole spend landed on the day it was last
touched — resume a conversation a week later and its entire history moved
into "today". A ledger row is stamped when the turn happened, so a bucket
means what it says.

What the move had to preserve, and does:

- a turn APPENDS, in one transaction — cost per turn is the size of the turn,
  never the size of the conversation
- the stored payload is the same wire JSON the codec already produced, so
  what resumes is what resumed before, signed reasoning included
- `/export foo.jsonl` still writes the old format — it goes through the codec
  now instead of copying the body file, which is where that always belonged

Traps paid for:

- **Pragmas belong in the DSN, not in `db.Exec`.** A pragma applies to the
  CONNECTION that ran it and `database/sql` hands out a pool, so
  `Exec("PRAGMA foreign_keys=ON")` leaves the next connection with foreign
  keys OFF — and SQLite then ignores `ON DELETE CASCADE` silently, orphaning
  every message of a deleted session. `MaxOpenConns(1)` on top: the writers
  and readers are goroutines of one process, so serialising them here turns
  SQLITE_BUSY into a wait on a mutex we own.
- **Order by `updated_at DESC, id DESC`.** Two sessions created in the same
  millisecond — a fork and its parent, always — tie, and "newest first" is
  the one thing a session picker has to get right.
- **Retry only on contention.** The open sequence retries because the first
  WAL switch can lose a race; retrying a schema-from-the-future error twenty
  times turns a clear failure into a slow one.
- **The import must seed the ledger.** The file era had one total per session
  and no per-turn record, so an unseeded import makes every bucket read $0
  for everything before the migration — which looks like the move lost the
  money rather than never having had the breakdown. One row per session,
  dated when it was last touched: exactly as precise as the data ever was.
- The import runs once (marked in `meta`), skips what it cannot read, and
  **leaves the files where they are** — they are the only copy until this has
  been proven on real data. Verified on 141 real sessions: 216 messages, the
  largest file's 44 lines landing as 44 rows, and a resumed conversation
  replaying tool calls and all.

---

## 15. The 2026-08-19 evening pass

Four things, all found by the user running the thing rather than by reading it.

### `cmd+→` was flinging people into nav mode

macOS terminals ship the readline line-navigation bindings: Ghostty binds
`cmd+←`/`cmd+→` to `text:\x01`/`text:\x05` — the literal control BYTES — and
legacy ctrl+e IS `\x05`. Under a byte comparison the two keys are the same
key, so jumping to the end of a line entered nav mode.

They separate once the **kitty keyboard protocol** is negotiated: a real
ctrl+e then arrives as `\x1b[101;5u` and only a terminal macro still sends the
bare byte. `internal/modules/tui/kitty.go` negotiates it at startup (with the
probes, before the decoder exists, because it reads its answer from stdin) and
`navToggle` requires the unambiguous form when it is active — falling back to
the byte when it is not, since on a terminal that cannot negotiate the byte is
the only ctrl+e there is. `\x05` still means end-of-line in the composer,
which is what the macro was asking for all along.

Only flag 1 (disambiguate) is requested. loop asks for 7, which adds key-event
types and alternate keys — that changes how EVERY key arrives, release events
included, and none of it is needed for the one ambiguity being resolved.

### Grouping was pi-agent's own, not loop's

The old rule was "three or more adjacent calls to the SAME tool", labelled
from a small verb table. loop's is a **kind** system, and every difference
mattered:

- **Kinds, not tools.** `grep` + `glob` + `find` are one kind, so a mixed run
  reads "Searched 3 patterns"; `read` + `skill` share a verb but not a noun,
  so they stay apart as "Read 2 files, Read 1 skill".
- **A run is ADJACENCY.** A stretch of different tools is ONE group whose
  label names each kind — "Read 2 files, Ran 1 command, Listed 1 dir".
- **One member is enough.** "Read 1 file" is already tighter than the row it
  replaces, and folding from the first call means the second JOINS a header
  instead of the row collapsing under you mid-turn.
- **Failures fold, and the header says `· 1 failed`.** The old rule kept them
  out of groups entirely; loop's point is that folding must never be a way to
  lose bad news.
- **`ask` and `plan` never fold** — a question folded into a count is one
  nobody answers.
- **Unknown tools say only what is known**: `Called 2 MCP tools` (namespaced
  `server__tool`) or `Called 1 extension tool`, with `RegisterToolVerbGroup`
  the one way to earn real grammar. No verb-sniffing heuristic: loop removed
  its own because it borrowed the builtin's NOUN along with the verb and
  rendered a run of Sentry lookups as "Listed 2 dirs".
- Openness is held against **every member**, not the head — expanding the
  first call drops it out of the run and promotes the second, and a lookup
  keyed on the head alone snaps the rest shut.

### The gap in live mode

Reported as blank rows mid-transcript. Chased with a frame log (`PI_FRAME_LOG`
dumps the frame the renderer BUILT) and a **screen model** — a small ANSI
terminal in `screen_test.go` that panics on any sequence it has not been
taught, so the bytes can be checked against the frame they were supposed to
draw.

The finding: **at every presented frame the screen was correct.** The frames
were right, the diff was right, and only 96 bytes in a whole session were
written outside our synchronized frames (startup sequences). What was being
seen was a frame caught mid-repaint.

There was still a real fault underneath it. Once the transcript outgrows the
screen the frame becomes a sliding WINDOW: every row takes the content of the
row below, so the diff sees all 30 rows change and repaints the whole screen —
at 60fps, for every streamed token. On a terminal that does not honour
synchronized output that repaint is watched in progress.

`slideOf` detects that the frame is the previous one scrolled up and emits the
newlines instead, writing only the newly exposed rows. It is also the only way
those rows reach the terminal's **scrollback**: a rewritten row is overwritten
and gone; a scrolled one is history.

### xAI had no SuperGrok option

Two faults. The listing offered only "paste an API key", so a SuperGrok
subscriber paid for the same tokens twice — and worse, `SaveKey` wrote the
credential as a BARE STRING while every reader on both sides of the shared
`~/.loop/auth.json` requires `{mode, provider, apiKey}`. A key entered in
pi-agent saved successfully and was then read by nobody.

`core/config/xai_oauth.go` is the authorization-code flow with PKCE: loopback
redirect on 127.0.0.1 (the code never leaves the machine), OIDC discovery with
every endpoint checked to be HTTPS and to belong to x.ai, and `state` compared
on the way back. The access token is supplied PER REQUEST through a new
`openaicompat.AuthToken` hook rather than captured at construction — an OAuth
token is refreshed while the process runs, and a captured one is a 401 an hour
into a conversation. A subscription beats an API key when both are present:
the user chose to bill the subscription.

Verified live end to end — real browser, real tokens on disk in loop's entry
shape, a real turn answered by Grok 4 Fast.

---

## Suggested order

Done: the rail, diamond rows, thinking blocks, tool rendering, nav mode, and
`/theme`. The transcript now reads as noir. What is left is breadth, not look:

Every subsystem worth having now exists. What is left is either small or
questionable:

1. **Small and still open** — `/tree`, image paste, `/login` OAuth flows,
   `/update`, configurable keybindings.
2. **Extensions** — loop's is a JS plugin system, which in a zero-dep Go
   binary means embedding an interpreter. The one item whose cost likely
   exceeds its worth; worth a decision before starting rather than after.
3. **Gateways, `serve`, datasources, artifacts** — each a product decision
   more than a gap.
4. **[—] Desktop and web UI** — not Go targets.
