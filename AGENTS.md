# pi

Go coding agent. One module, three internal modules under `internal/modules`,
mirroring loop's `packages/`:

```
internal/modules/ai     model layer (was the pigo repo, package `ai`)
internal/modules/tui    terminal rendering: markdown, theme, width, editor
internal/modules/core   agent, tools, session, config, catalog
cmd/pi                  the app that glues them together
```

**The dependency graph is the design.** `ai` imports nothing of ours; `tui`
and `core` each import only `ai`; nothing imports `tui` except `cmd`. Core has
no idea a terminal exists — rendering is injected as an `agent.Consume` func.
Keep it that way: a `core → tui` import is the first step back to a tangle.

`ai` was its own repo at `../pigo`; that copy still holds the git history but
is no longer what builds.

**One external dependency: `mattn/go-sqlite3`.** The rule used to be zero, and
it held for everything above — the markdown lexer, the width model, the theme
layer are all hand-rolled and should stay that way. Sessions are the
exception, taken deliberately: they live in a WAL SQLite file now (loop's
arrangement), and every driver is either cgo or a very large pure-Go
transpilation. cgo is what that costs — a C toolchain to build, a
cross-compiler to release. Everything goes through `database/sql` and
`core/db` is the ONLY place that opens a handle, so swapping to
`modernc.org/sqlite` is one import line. Do not add a second dependency
without the same kind of reason.

Tools live in `core/tools`. The edit matcher is the fixed loop version:
fuzzy match is found in normalized space, then rewritten only on the real
span. Never normalize the whole file and write it back.

Providers and models live in `core/catalog`: a curated `seed` for ordering
(`Default` takes the first entry, so "first" must be a good default) plus the
full models.dev catalog embedded as `models.json` — 777 models. Updating it is
a file copy from loop's `packages/core/src/catalog/generated/models.json`, not
a code change.
Full ids are `provider/model`. Kimi swaps to the Code-plan catalog when
the key is `sk-kimi-…`.

`/model` and `/provider` with no args open `ui.Search` — loop's
searchOnce: the query and the list are one control.

## Checking the UI

**UI fidelity cannot be verified by reading code.** Every gap found so far —
the composer's framing rules, the completion list swallowing every keystroke,
the two-row status line, a bare `/` never opening the palette — looked correct
in source and was wrong on screen.

```bash
python3 scripts/screenshot.py 'w1'                ./pi  # startup
python3 scripts/screenshot.py '/co|w2'            ./pi  # command palette
python3 scripts/screenshot.py 'hi|w1|\r|w20'      ./pi  # a real turn
ROWS=24 python3 scripts/screenshot.py --history 'hi|w1|\r|w20' ./pi
```

It runs the binary in a real pty and prints the resulting screen. The script is
a `|`-separated list of steps: `w<seconds>` waits, anything else is typed.
`--history` also prints the terminal's scrollback, which is the only way to
check that finished blocks are being handed to the terminal rather than
overwritten in place. Point it at loop the same way
(`bun packages/cli/src/cli.ts`) and diff the two.

Two traps in the harness itself, both of which produce convincing evidence of
bugs that do not exist:

- **Enter is `\r`, not `\n`.** Terminals send carriage return. Sending `\n`
  submits nothing and the next keystrokes append to the draft, which reads as
  a command that silently did the wrong thing.
- **An escape sequence must be ONE write.** Split across writes with a delay,
  the decoder times out the ESC and reports a bare Esc — indistinguishable
  from an arrow key that was never wired up.

The rules the render loop depends on:

- **Input is never dropped.** The key channel's send BLOCKS. It used to fall
  through a `default:` case, which silently discarded typing once the buffer
  filled — and since every key costs a repaint, a fast typist outran it and
  the tail of a sentence just vanished. Backpressure on the pty is correct;
  losing input never is.

- **Repaint on CHANGE, never on schedule.** A tick only earns a frame when
  something is animating; an idle prompt must write zero bytes. Rendering
  every tick repainted the whole grid 20×/second forever.
- **A block that has just STOPPED animating must be drawn once more.** The
  finish flash is the case that bites: a tool row cached during its 400ms
  flash keeps the heavy rail, and once the flash expires the block is no
  longer "live", so the stale frame is served forever and the row never
  settles.
- **The cursor escape is the last thing written, and its column is `curCol+1`
  and nothing else.** `View` already includes the composer's padding; any
  extra offset floats the cursor away from the text.
- **Draw INLINE, never on the alternate screen.** loop does, and the anchor
  is the whole difference in how a session reads. On the alt screen the
  frame is always exactly a screen tall, so a short transcript has to be
  padded somewhere — pi-agent padded above, which parked the masthead at the
  bottom of an empty screen. Inline it grows downward from where the shell
  left the cursor, so the masthead sits at the top with the composer under
  it, and the conversation survives in scrollback instead of vanishing when
  the alt screen is restored on exit.
- **Every move inside the live region is RELATIVE to the cursor.** The region
  has no fixed screen address: writing its last line on the bottom row
  scrolls the screen and moves it. Relative moves travel with the scroll; an
  absolute anchor drifts one row per scroll and smears the frame down the
  page. Columns are the exception — the region never scrolls sideways — so
  `\x1b[<n>G` is fine.
- **Scrollback is earned, not implied.** Painting inline does NOT by itself
  give the terminal any history: the region is capped at the screen height
  and repainted in place, so the head is overwritten row by row and scrolling
  back showed nothing — the same loss the alt screen caused, arrived at
  differently. `retire.go` is what makes the claim true: a finished block
  above the fold is PRINTED above the region and then DELETED from
  `a.entries`. Deleting is the safety property — a retired block cannot
  render, be selected, or be folded again, so it can never appear twice.
  Retirement is suspended while nav or scroll is active, since those are the
  user reading back through the very blocks it would delete.
- **The region is hard-capped at the terminal height.** `avail` trims the
  transcript, but the chrome alone can exceed a short screen (a tall
  completion menu), and an over-tall region does not merely clip: the
  repaint walks the cursor further up than there is screen, the terminal
  clamps at row 0, and every frame lands a row lower than the last, printing
  copies of itself down the page.

## Commands

`cmd/pi/registry.go` is the ONE table. Dispatch, `/help`, and the `/`
palette are all generated from it — adding a command anywhere else means it
works but nobody discovers it, which is exactly how the three hand-kept lists
drifted apart before. Aliases get their own entry (`/effort` says "alias for
/thinking") rather than hiding behind their target, because a user who types
the name loop taught them should not be told it does not exist.

Two input rules that came out of comparing against loop:

- **Enter on a slash completion accepts AND runs it.** Tab accepts without
  running, so a command can be completed and then given arguments. An
  `@file` completion is the opposite — it is part of a sentence still being
  written, so Enter accepts it and stays.
- **An exact name sorts first.** `/session` sorts after `/sessions` in the
  table, so typing the full name of one command and pressing Enter ran a
  different one.

## The renderer

`internal/modules/tui` is a port of loop's TUI layer, and the rules that
survived being learned the hard way there are load-bearing here too:

- **Never count runes for width.** `visibleWidth` is the only measure; it
  treats ANSI as free, CJK/emoji as two cells, and combining marks as none.
  A Devanagari cluster is several runes and two cells.
- **Markdown is lexed, not substituted.** `markdown_block.go` +
  `markdown_inline.go` produce marked-shaped tokens; emphasis goes through
  CommonMark's delimiter-stack algorithm because whether `*` opens or closes
  depends on both sides of the run.
- **Every token's `Raw` must reconstruct the source exactly.** The streaming
  freeze (`freezePoint` / `advanceStable`) depends on it, and verifies it at
  runtime before trusting a frozen head.
- **A blank line does not end a list**, it makes it loose — so the freeze
  never cuts across one, or an ordered list renumbers from 1 mid-stream.
- **A one-line row must never exceed the width** (`fitRow`): an overflowing
  row wraps and pushes the grid down, desynchronising every later cursor move.

Tool calls are gated by `core/permissions`. Three rules hold it together:

- **Everything the model can call goes through the policy** — built-in tools,
  the subagent tool, and tools discovered from MCP servers alike. A transport
  that let a server's tools skip the policy would make the policy decorative,
  since anyone who can edit a settings file could then run anything.

- **The strictest matching rule wins, regardless of order.** Rules arrive from
  the defaults, from settings, and from `/permissions`; first-match-wins would
  be subtly order-dependent across three sources. Deny beats ask beats allow.
- **A nil `Run.Ask` makes `ask` behave as `deny`.** The one-shot `run` path has
  nobody to prompt, and running a call because no one was listening is the
  exact failure the package exists to prevent.

The always-on deny list is short on purpose: it covers damage that is
immediate, irreversible and outside the repo, where a prompt is not good
enough because people approve by reflex. Everything else is the user's call.

Sessions persist as JSONL under `~/.pi-agent/sessions`: `<id>.jsonl` is an
APPEND-ONLY body, `<id>.meta.json` the header. Not SQLite — a driver would be
cgo or a large pure-Go dep, and there are none here. Two rules the store
depends on:

- **The body is append-only.** Metadata lives in its own file precisely so a
  turn never rewrites the conversation; only `/compact` and `/new` call
  `rewrite`. Putting the header back inside the JSONL makes every turn O(n).
- **The codec must never silently drop a part.** A conversation missing half
  of a tool-call/result pair is rejected by the provider, and
  `ProviderOptions` carries Anthropic's SIGNED reasoning — lose it and a
  resumed session 400s. Unsupported parts return an error.

Preferences live in `~/.pi-agent/settings.json` (provider, model, theme,
reasoning, maxSteps). They layer UNDER the flags — an explicit `-model` always
beats a stored one — and a missing or corrupt file is never fatal, it just
costs you the preference.

```bash
go test ./...
go run ./cmd/mddemo some.md      # eyeball the markdown renderer
go run ./cmd/mddemo transcript   # eyeball the noir row grammar
go run ./cmd/pi -model kimi/k3 run "what is in ./internal/modules/core/tools?"
```
