package tui

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"
)

// App is the TUI: noir transcript blocks rendered above the editor and
// status bar, painted INLINE into the normal screen buffer rather than onto
// the alternate screen. The live region grows downward from where the shell
// left the cursor and is capped at the terminal height; what scrolls off the
// top is the terminal's own scrollback, so it stays selectable and outlives
// the process. See inline.go.

// entryKind is what a transcript block is.
type entryKind int

const (
	entNotice entryKind = iota // system lines: banner, command output, errors
	entUser
	entThinking
	entAssistant
	entTool
	entSummary
)

// entry is one transcript block. It owns its rendered lines and the cache
// that keeps a settled block from re-rendering on every animation frame.
type entry struct {
	kind entryKind

	lines []string // notice/summary: the content itself
	// preformatted marks lines already laid out to the full width — the
	// masthead — which must not be re-indented into the content column.
	preformatted bool
	text         string // user prompt, or assistant markdown source
	at           time.Time

	md *Markdown // assistant

	started   time.Time // thinking
	duration  time.Duration
	streaming bool

	tool ToolState

	expanded bool
	selected bool

	cache      []string
	cacheWidth int
	cacheTick  int64
	cacheNav   bool
	// cacheLive records whether the cached frame was drawn while the block
	// was still animating. See render.
	cacheLive bool
	dirty     bool
}

// animated reports whether this block's appearance depends on the frame
// clock. Only these re-render every tick; everything else is served from
// cache, which is what keeps a long transcript cheap.
func (e *entry) animated() bool {
	switch e.kind {
	case entThinking:
		return e.streaming
	case entTool:
		if e.tool.IsPartial {
			return true
		}
		// The finish flash is a brief animated beat after the call lands.
		return !e.tool.FinishedAt.IsZero() && time.Since(e.tool.FinishedAt) < FinishFlash
	}
	return false
}

// render returns the block's lines, using the cache when it is still valid.
func (e *entry) render(t *Theme, width int, tick int64, expandAll, nav bool) []string {
	live := e.animated()
	// A block that has JUST stopped animating must be drawn once more. The
	// finish flash is the case that bites: a tool row cached during its
	// 400ms flash holds the heavy rail, and once the flash expires the block
	// is no longer "live", so the stale frame would be served forever — the
	// row never settles to its light rail.
	settling := e.cacheLive && !live
	// nav is part of the cache key: the expand hint is worded differently
	// depending on who has the keyboard, so a block cached outside nav would
	// still be telling you to press ctrl+e once you are already in it.
	if !e.dirty && !settling && e.cacheNav == nav && e.cache != nil &&
		e.cacheWidth == width && (!live || e.cacheTick == tick) {
		return e.cache
	}

	expanded := e.expanded || expandAll
	var out []string
	switch e.kind {
	case entNotice:
		if e.preformatted {
			out = e.lines
			break
		}
		out = RenderNotice(t, e.lines, width)

	case entUser:
		out = RenderUser(t, e.text, e.at, width)

	case entThinking:
		out = RenderThinking(t, ThinkingState{
			Text:      e.text,
			Streaming: e.streaming,
			Expanded:  expanded,
			Selected:  e.selected,
			Nav:       nav,
			Duration:  e.duration,
		}, width, tick)

	case entAssistant:
		e.md.SetText(e.text)
		// The assistant's own words are the one block with no rail: it is
		// prose, not an event, and a rail would make it read as one.
		out = assistantBlock(t, e.md.Render(max(1, width-ContentIndent)), e.at, width)

	case entTool:
		st := e.tool
		st.Expanded = expanded
		st.Selected = e.selected
		st.Nav = nav
		out = RenderTool(t, st, width, tick)

	case entSummary:
		out = e.lines
	}

	// The spine is applied LAST, over whatever the block rendered, so every
	// block type gets it without each having to know about selection.
	if e.selected {
		out = markSelected(t, out)
	}
	e.cache, e.cacheWidth, e.cacheTick, e.cacheLive, e.cacheNav, e.dirty = out, width, tick, live, nav, false
	return out
}

// indentLines shifts pre-rendered lines into the content column.
func indentLines(lines []string, n int) []string {
	pad := strings.Repeat(" ", n)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = pad + l
	}
	return out
}

// assistantBlock puts prose in the content column with the same single
// leading gap every other block owns.
//
// Markdown emits its own trailing blank after a block-level element, so the
// trailing ones are trimmed: without that, prose followed by a tool row shows
// two blank lines where every other pair shows one.
func assistantBlock(t *Theme, lines []string, at time.Time, width int) []string {
	for len(lines) > 0 && strings.TrimSpace(stripANSI(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}
	out := indentLines(lines, ContentIndent)
	// The timestamp rides the first line, matching the user prompt it answers.
	if !at.IsZero() {
		out[0] = withStamp(t, strings.TrimRight(out[0], " "), at.Format("3:04 PM"), width)
	}
	return append([]string{""}, out...)
}

// App is the interactive terminal application.
type App struct {
	size    Size
	entries []*entry
	loader  *Loader

	// Open blocks, held so deltas land in the right place.
	stream   *entry
	thinking *entry
	tools    map[string]*entry

	editor   *Editor
	commands []Item
	files    func(string) []Item

	theme   *Theme
	mdTheme MarkdownTheme
	cwd     string

	model   string
	statIn  int64
	statOut int64
	// statCached is cache-read input tokens. Tracked separately from statIn
	// because the interesting number is the RATIO — a status-line layout
	// showing "hit 87%" is showing how much of the input was free.
	statCached int64
	statCtx    int
	statMax    int
	cost       float64

	// Nav mode: the transcript takes the keyboard, so blocks can be selected
	// and opened. Without it, folding hides content with no way back.
	nav       bool
	sel       int
	expandAll bool
	// groupsOpen tracks which folded runs the user has opened, keyed by the
	// index of the run's first member.
	groupsOpen map[int]bool

	Events  chan Event
	keys    *KeyDecoder
	done    bool
	closeFn func()
	// suspendFn hands the terminal to a child program and takes it back.
	suspendFn func(func())
	scroll    int
	// timerEndsAt is the countdown's deadline, zero when none is running.
	timerEndsAt time.Time
	// clock puts a ticking clock in the status line. Off by default: a clock
	// forces a repaint every second forever, which is the one thing the
	// render loop is built to avoid.
	clock bool
	out   inlineWriter
	// chromeRows is the height of the composer/status block, so exit can
	// erase it and leave only the conversation behind.
	chromeRows  int
	mut         chan func()
	modal       *Modal
	prompt      *Prompt
	confirm     *Confirm
	todos       []TodoItem
	planning    bool
	background  Background
	attachments []Attachment
	// Status-line identity. `thinkingLevel` rather than `thinking`, which is
	// already the open reasoning block.
	agent         string
	thinkingLevel string
	// modelReasoning is whether the CURRENT model reasons at all. Defaults to
	// true: a custom-provider model is not resolvable in the catalog until the
	// async warm-up lands, and starting at false hides the thinking level on
	// every boot until the user re-picks the model.
	modelReasoning bool
	// statusTransforms rewrite the rendered status rows — see SetStatusTransform.
	statusTransforms []StatusTransform
	// pinnedInput holds the composer on the last rows of the screen. Off by
	// default: a short conversation reads better growing downward from where
	// it started than pushed to the bottom behind a wall of blanks.
	pinnedInput bool
	session     string
	// liveVariant is loop's live look applied outside navigation: runs of
	// finished tool calls stay folded into one row while you are typing.
	liveVariant bool
	// hooks report what the app is doing to whoever is watching the pane.
	hooks StatusHooks
	// needsPaint is the frame the loop owes: set by anything that changes
	// what the screen should say, cleared by the paint that says it.
	needsPaint bool
	// paintedAt is when the last frame went out, which is what the frame
	// budget is measured from.
	paintedAt time.Time
}

// Event is something the app needs the caller to do.
type Event struct {
	Kind EventKind
	Text string
}

type EventKind int

const (
	EvSubmit EventKind = iota
	EvSlash
	EvBash
	EvQuit
	EvInterrupt
	// EvCycleAgent is shift+tab: step to the next agent. The app does not
	// own the agent list, so it reports the keypress and lets the repl decide
	// what "next" means.
	EvCycleAgent
	// EvContinue is ctrl+g: resume interrupted work without retyping the
	// word "continue".
	EvContinue
	// EvClearScreen is ctrl+l.
	EvClearScreen
	// EvCycleModel is ctrl+p: step through the scoped-model list.
	EvCycleModel
)

// NewApp boots the terminal: alt screen, raw mode, bracketed paste, canvas.
func NewApp(model, cwd string) (*App, error) {
	if !isTerminal(os.Stdin) {
		return nil, fmt.Errorf("tui: not a tty — use `run` for non-interactive")
	}
	restore, err := makeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return nil, err
	}
	// Both probes read their answer from stdin, so they run here: raw mode is
	// on and the key decoder does not exist yet, so no keystroke of the
	// user's can be mistaken for a reply.
	ProbeWidths()
	background := DetectBackground()
	// Negotiated here for the same reason as the probes above, and before the
	// decoder exists: it reads its answer from stdin.
	leaveKitty := EnableKittyKeyboard()

	leaveInline := InlineScreen()
	leavePaste := EnableBracketedPaste()

	theme := NewTheme(NightPalette)
	theme.TrueColor = TerminalCaps().TrueColor
	unwash := SetCanvas(theme.Palette)

	a := &App{
		size:   size(os.Stdout),
		editor: NewEditor(),
		model:  model,
		// See the field: unknown until the catalog is warm, and true is the
		// answer that does not hide a level the user did set.
		modelReasoning: true,
		cwd:            cwd,
		theme:          theme,
		mdTheme:        MarkdownThemeFor(theme),
		tools:          map[string]*entry{},
		groupsOpen:     map[int]bool{},
		Events:         make(chan Event, 8),
		mut:            make(chan func(), 64),
	}
	a.background = background
	a.keys = DecodeKeys()
	a.editor.Theme = theme
	a.editor.SetCompleter(a.complete)
	a.closeFn = func() {
		a.keys.Close()
		leavePaste()
		leaveKitty()
		a.out.Finish(a.chromeRows)
		leaveInline()
		unwash()
		restore()
	}
	// Suspending gives the terminal BACK to the shell for the duration of a
	// child program, then takes it again. It is the same teardown and setup
	// as Close and NewApp, minus the probes — which read from stdin and would
	// swallow the first keystroke after an editor exits.
	a.suspendFn = func(f func()) {
		a.keys.Close()
		leavePaste()
		leaveKitty()
		a.out.Finish(a.chromeRows)
		leaveInline()
		unwash()
		restore()

		f()

		if again, err := makeRaw(int(os.Stdin.Fd())); err == nil {
			restore = again
		}
		// Renegotiated rather than assumed: the child may have pushed its own
		// keyboard flags and popped ours, and a stale "active" would make
		// ctrl+e stop working for the rest of the session.
		leaveKitty = EnableKittyKeyboard()
		leaveInline = InlineScreen()
		leavePaste = EnableBracketedPaste()
		unwash = SetCanvas(a.theme.Palette)
		a.keys = DecodeKeys()
		// The child scribbled over the screen, so the writer's idea of where
		// its region sits is a lie — and acting on a stale row count walks
		// the cursor up into rows it does not own.
		a.out.Reset()
		a.size = size(os.Stdout)
		a.dirtyAll()
	}
	return a, nil
}

// Suspend hands the terminal to f — an editor, a pager — and takes it back.
//
// Synchronous, and it must be called from OFF the render loop: it stops the
// input decoder and restores cooked mode, and a render that landed in the
// middle would paint over whatever the child is drawing.
func (a *App) Suspend(f func()) {
	done := make(chan struct{})
	a.Do(func() {
		defer close(done)
		a.suspendFn(f)
	})
	<-done
}

// Theme is the active theme, for callers that render their own content.
func (a *App) Theme() *Theme { return a.theme }

// SetTheme swaps the palette and invalidates every cached block.
func (a *App) SetTheme(p Palette) {
	a.theme = NewTheme(p)
	a.theme.TrueColor = TerminalCaps().TrueColor
	a.mdTheme = MarkdownThemeFor(a.theme)
	a.editor.Theme = a.theme
	SetCanvas(p)
	for _, e := range a.entries {
		if e.kind == entAssistant {
			e.md.Theme = a.mdTheme
			e.md.Invalidate()
		}
		e.dirty = true
	}
}

// SetSources wires the completion catalogs.
func (a *App) SetSources(commands []Item, files func(string) []Item) {
	a.commands = commands
	a.files = files
}

// complete supplies candidates for the word under the cursor.
//
// The word carries its own trigger, so this switches on the word's first
// character rather than on what precedes it.
func (a *App) complete(word string) []Item {
	switch {
	case strings.HasPrefix(word, "/"):
		// A slash command is only a command at the very start of the draft;
		// anywhere else a slash is just a path separator.
		if a.editor.row != 0 || a.editor.col != len([]rune(word)) {
			return nil
		}
		return matchCommands(a.commands, strings.TrimPrefix(word, "/"))

	// Both triggers, because both are habits people arrive with: `@` from
	// chat clients, `#` from issue trackers and from Claude Code.
	case strings.HasPrefix(word, "@"), strings.HasPrefix(word, "#"):
		if a.files != nil {
			return a.files(word[1:])
		}
	}
	return nil
}

// Close restores the terminal.
func (a *App) Close() { a.closeFn() }

// SetModel updates the status bar identity.
func (a *App) SetModel(id string) { a.model = id }

// SetModelReasoning records whether the current model reasons, which is what
// decides if the thinking level belongs in the status line.
func (a *App) SetModelReasoning(on bool) { a.modelReasoning = on }

// SetUsage updates footer stats after a turn.
func (a *App) SetUsage(in, out int64, ctxWindow int, costUSD float64) {
	a.statIn += in
	a.statOut += out
	a.statCtx = int(in)
	a.statMax = ctxWindow
	a.cost += costUSD
}

// SetCachedTokens adds to the cache-read total.
func (a *App) SetCachedTokens(n int64) { a.statCached += n }

// StatusContext is everything a status-line layout can draw from.
//
// A flat snapshot, passed by value: a layout runs inside the render loop and
// must not be able to reach back into the App and mutate anything, and a
// struct it cannot hold a pointer into is the cheapest way to guarantee that.
type StatusContext struct {
	Agent    string
	ModelID  string
	Provider string
	Model    string
	Session  string
	CWD      string
	Thinking string
	// Reasoning is whether the model reasons at all — a layout must not show
	// a stale level for a model that has none.
	Reasoning bool
	Cost      float64
	InputTokens,
	OutputTokens,
	CachedTokens int64
	ContextUsed, ContextMax int
	Width                   int
	Planning                bool
}

// StatusTransform rewrites the rendered status rows.
//
// Returning nil leaves them alone, which is what a transform does for the
// state it has no opinion about — and what a BROKEN one is made to do, since
// a status line that blanks itself is worse than one that ignores a theme.
type StatusTransform func(lines []string, ctx StatusContext) []string

// SetStatusTransform installs the transform chain, replacing any previous one.
//
// A chain rather than one function because the two axes compose: a layout
// decides the structure and a colour theme repaints whatever it produced, and
// neither has to know about the other.
func (a *App) SetStatusTransform(chain ...StatusTransform) {
	a.statusTransforms = chain
}

// statusContext is the snapshot handed to a transform.
func (a *App) statusContext(width int) StatusContext {
	provider, model, _ := strings.Cut(a.model, "/")
	if model == "" {
		provider, model = "", a.model
	}
	agent := a.agent
	if agent == "" {
		agent = "default"
	}
	return StatusContext{
		Agent: agent, ModelID: a.model, Provider: provider, Model: model,
		Session: a.session, CWD: a.cwd,
		Thinking: a.thinkingLevel, Reasoning: a.modelReasoning,
		Cost:        a.cost,
		InputTokens: a.statIn, OutputTokens: a.statOut, CachedTokens: a.statCached,
		ContextUsed: a.statCtx, ContextMax: a.statMax,
		Width: width, Planning: a.planning,
	}
}

// ResetUsage zeroes the running totals for a new session.
//
// Its own method rather than SetUsage(0, …): SetUsage ACCUMULATES, so passing
// zeroes to it changes nothing and leaves the last session's spend on the
// status line of a conversation that has not cost anything yet.
func (a *App) ResetUsage(ctxWindow int) {
	a.statIn, a.statOut, a.statCached, a.statCtx, a.cost = 0, 0, 0, 0, 0
	a.statMax = ctxWindow
}

// Wipe erases the screen AND the terminal's scrollback.
//
// Clear only empties this app's model of the transcript; blocks that were
// retired have already been handed to the terminal and live in its
// scrollback, where nothing but this sequence can reach them. It is the whole
// difference between /new (start again, keep the record) and /clear (start
// again, and take the record with it).
func (a *App) Wipe() {
	a.out.write("\x1b[3J\x1b[2J\x1b[H")
	// The region no longer exists on screen, so the writer's row count is a
	// lie — acting on it would walk the cursor up into rows it does not own.
	a.out.Reset()
}

func (a *App) push(e *entry) *entry {
	e.dirty = true
	a.entries = append(a.entries, e)
	return e
}

// Print adds finished lines to the transcript.
func (a *App) Print(lines ...string) {
	a.push(&entry{kind: entNotice, lines: lines})
}

// PrintRaw adds lines already laid out to the full width, bypassing the
// content-column indent. For the masthead and the startup block, which are
// chrome rather than conversation.
func (a *App) PrintRaw(lines ...string) {
	a.push(&entry{kind: entNotice, lines: lines, preformatted: true})
}

// Printf is Print with formatting.
func (a *App) Printf(format string, args ...any) {
	a.Print(strings.Split(fmt.Sprintf(format, args...), "\n")...)
}

// UserEcho prints the submitted prompt into the transcript.
func (a *App) UserEcho(text string) {
	a.push(&entry{kind: entUser, text: text, at: time.Now()})
}

// ThinkingDelta appends reasoning, opening the block on first delta.
func (a *App) ThinkingDelta(delta string) {
	if a.thinking == nil {
		a.thinking = a.push(&entry{kind: entThinking, streaming: true, started: time.Now()})
	}
	a.thinking.text += delta
	a.thinking.dirty = true
}

// ThinkingEnd seals the reasoning block, which folds to its header.
func (a *App) ThinkingEnd() {
	if a.thinking == nil {
		return
	}
	a.thinking.streaming = false
	a.thinking.duration = time.Since(a.thinking.started)
	a.thinking.dirty = true
	a.thinking = nil
}

// AssistantDelta appends model text, opening a markdown block on first delta.
func (a *App) AssistantDelta(delta string) {
	if a.stream == nil {
		md := NewMarkdown(a.mdTheme, nil)
		md.SetStreaming(true)
		a.stream = a.push(&entry{kind: entAssistant, md: md, at: time.Now()})
	}
	a.stream.text += delta
	a.stream.dirty = true
}

// AssistantEnd seals the markdown block.
//
// The finishing pass renders the whole document at once, which is what
// resolves a link definition arriving after the link that used it — the one
// thing incremental lexing cannot see.
func (a *App) AssistantEnd() {
	if a.stream == nil {
		return
	}
	a.stream.md.SetStreaming(false)
	a.stream.dirty = true
	a.stream = nil
}

// ToolStart opens a tool row.
func (a *App) ToolStart(id, name string, args map[string]any) {
	// A run of consecutive calls stays tight; only the first takes a gap.
	groupLead := len(a.entries) == 0 || a.entries[len(a.entries)-1].kind != entTool
	e := a.push(&entry{kind: entTool, tool: ToolState{
		Name:      name,
		Args:      args,
		IsPartial: true,
		GroupLead: groupLead,
		CWD:       a.cwd,
	}})
	a.tools[id] = e
}

// ToolArgs updates a call's arguments once they have finished streaming.
func (a *App) ToolArgs(id string, args map[string]any) {
	if e := a.tools[id]; e != nil {
		e.tool.Args = args
		e.dirty = true
	}
}

// ToolInputDelta appends a fragment of the call's streaming JSON arguments.
// The interesting field is pulled back out for the live tail — a `write`
// shows its body landing rather than a frozen row.
func (a *App) ToolInputDelta(id, delta string) {
	e := a.tools[id]
	if e == nil {
		return
	}
	e.tool.rawInput += delta
	e.tool.StreamingContent = StreamingPreview(e.tool.Name, e.tool.rawInput)
	e.dirty = true
}

// ToolStatus updates a running call's status text — a subagent reporting
// what it is doing, so a long delegation is not a frozen row.
func (a *App) ToolStatus(id, status string) {
	if e := a.tools[id]; e != nil {
		e.tool.StatusText = status
		e.dirty = true
	}
}

// ToolEnd resolves a call with its output.
func (a *App) ToolEnd(id, output string, isError bool) {
	e := a.tools[id]
	if e == nil {
		return
	}
	e.tool.IsPartial = false
	e.tool.Output = output
	e.tool.IsError = isError
	e.tool.FinishedAt = time.Now()
	e.dirty = true
	delete(a.tools, id)
}

// InterruptOpen marks every still-running block as interrupted. A cancelled
// turn leaves calls that will never resolve; without this they animate
// forever.
func (a *App) InterruptOpen() {
	for id, e := range a.tools {
		e.tool.IsPartial = false
		e.tool.Interrupted = true
		e.dirty = true
		delete(a.tools, id)
	}
	a.ThinkingEnd()
	a.AssistantEnd()
}

// TurnSummary closes a turn with its stats line.
func (a *App) TurnSummary(d time.Duration) {
	a.push(&entry{kind: entSummary, duration: d, lines: RenderTurnSummary(a.theme, d, a.size.Cols)})
}

// LoaderStart shows the spinner line.
func (a *App) LoaderStart() {
	a.loader = NewLoader()
	if a.hooks.Working != nil {
		a.hooks.Working(true)
	}
}

// LoaderStop hides it.
func (a *App) LoaderStop() {
	a.loader = nil
	if a.hooks.Working != nil {
		a.hooks.Working(false)
	}
}

// Resize refreshes dimensions and drops every width-keyed cache.
func (a *App) Resize() {
	a.size = size(os.Stdout)
	// The terminal reflows on resize, so the writer's idea of where the
	// region sits is no longer true — and acting on a stale row count is
	// what walks the cursor up into rows it does not own. Forget the region
	// and redraw from wherever the cursor now is.
	a.out.Reset()
	for _, e := range a.entries {
		if e.kind == entAssistant {
			// The frozen streaming head was wrapped to a width that no
			// longer holds.
			e.md.Invalidate()
		}
		if e.kind == entSummary {
			e.lines = RenderTurnSummary(a.theme, e.duration, a.size.Cols)
		}
		e.dirty = true
	}
}

// FrameInterval is the floor between two repaints — 60fps, loop's budget.
//
// It is a CEILING on painting, not a schedule to paint on: an idle prompt
// still paints nothing at all. What it buys is the opposite case. Every
// streaming delta, every keystroke of a paste, every tool status arrives as
// its own event, and painting each one meant a model emitting 300 tokens a
// second asked the terminal for 300 frames a second. Nothing above 60 is
// visible, so the ones above it are bytes down the wire and time not spent
// reading stdin — which is what makes a fast stream feel LESS responsive
// than a slow one.
const FrameInterval = time.Second / 60

// drainLimit bounds one coalescing pass. A producer that can fill the
// mutation channel faster than the loop empties it would otherwise hold the
// pass open indefinitely and starve the frame it is collecting work for.
const drainLimit = 4096

// Run is the event loop. It collects everything that has arrived, then paints
// at most once per frame interval.
func (a *App) Run() error {
	tick := time.NewTicker(AnimInterval)
	defer tick.Stop()
	// The pacer. Armed only when a frame is owed but the budget has not
	// elapsed, so a quiet session parks in the select with no timer running.
	frame := time.NewTimer(time.Hour)
	frame.Stop()
	defer frame.Stop()
	armed := false

	a.paint()
	for !a.done {
		// Repaint on CHANGE, not on schedule. A tick only earns a frame when
		// something is actually animating; an idle prompt paints nothing at
		// all. Rendering unconditionally here repainted the whole grid 20
		// times a second forever — invisible on a fast terminal, and pure
		// waste over ssh or in a scrollback-heavy session.
		select {
		case k, ok := <-a.keys.Ch:
			if !ok {
				return nil
			}
			a.onKey(k)
			a.needsPaint = true
		case f := <-a.mut:
			f()
			a.needsPaint = true
		case <-tick.C:
			if !a.live() {
				continue
			}
			AdvanceAnim()
			a.needsPaint = true
		case <-frame.C:
			armed = false
		}
		// Everything else already queued belongs in the same frame. This is
		// where a burst collapses: a 500-character paste is one repaint, and
		// a turn's worth of deltas that landed inside one interval is one
		// repaint of their combined result rather than one each.
		a.drain()

		if !a.needsPaint {
			continue
		}
		if wait := a.frameWait(time.Now()); wait > 0 {
			if !armed {
				frame.Reset(wait)
				armed = true
			}
			continue
		}
		a.paint()
	}
	return nil
}

// frameWait is how long the owed frame must be held back to stay inside the
// budget, or zero to paint now.
//
// The last frame is owed unconditionally. The loop is about to exit and Close
// will erase the chrome over whatever is on screen, so a frame withheld for
// the budget would simply never be drawn.
func (a *App) frameWait(now time.Time) time.Duration {
	if a.done {
		return 0
	}
	return FrameInterval - now.Sub(a.paintedAt)
}

// drain consumes whatever is already waiting, without blocking.
func (a *App) drain() {
	for i := 0; i < drainLimit; i++ {
		select {
		case k, ok := <-a.keys.Ch:
			if !ok {
				a.done = true
				return
			}
			a.onKey(k)
			a.needsPaint = true
		case f := <-a.mut:
			f()
			a.needsPaint = true
		default:
			return
		}
	}
}

// paint draws one frame, and survives a render that does not.
//
// A panic in render is a bug in a block — a malformed table, a width model
// disagreeing with itself on some rune a tool returned — and killing the
// process for it loses the session AND leaves the terminal in raw mode with
// the cursor hidden. Recovering costs a damaged frame; the recovery is to
// distrust what is on screen, so the next frame rewrites the region whole.
//
// The failed frame is NOT re-requested. A render that panics on some content
// panics on it again, and asking for another frame immediately turns one bug
// into a loop that burns the frame budget forever without ever drawing.
func (a *App) paint() {
	a.needsPaint = false
	a.paintedAt = time.Now()
	defer func() {
		if r := recover(); r != nil {
			a.out.Invalidate()
			logRenderPanic(r)
		}
	}()
	a.render()
}

// logRenderPanic records a recovered render panic, since the one place it
// must never appear is the screen.
func logRenderPanic(r any) {
	path := os.Getenv("PI_RENDER_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] render panic: %v\n%s\n", time.Now().Format(time.RFC3339), r, debug.Stack())
}

// live reports whether anything on screen is animating.
func (a *App) live() bool {
	if a.loader != nil {
		return true
	}
	// A running countdown — or a clock — has to keep the frame clock going,
	// or the status line freezes at whatever second it was last repainted
	// for some other reason and appears stopped.
	if !a.timerEndsAt.IsZero() || a.clock {
		return true
	}
	for _, e := range a.entries {
		if e.animated() {
			return true
		}
	}
	return false
}

// Do runs f on the render goroutine. Every mutation from outside the event
// loop must go through it.
func (a *App) Do(f func()) { a.mut <- f }

// Pick opens a filterable picker, blocking the caller until the user selects
// (nil = cancel). `current` marks the entry already in force.
//
// Must not be called from the render loop: it waits on a channel the render
// loop feeds.
func (a *App) Pick(title string, items []Item, initial int, current string) *Item {
	return a.openModal(ModalSearch, title, items, initial, current)
}

// Select is the same picker WITHOUT a filter box, for a list short enough
// that every option is already on screen. See ModalSelect.
func (a *App) Select(title string, items []Item, initial int, current string) *Item {
	return a.openModal(ModalSelect, title, items, initial, current)
}

func (a *App) openModal(mode ModalMode, title string, items []Item, initial int, current string) *Item {
	if len(items) == 0 {
		return nil
	}
	done := make(chan *Item, 1)
	a.Do(func() {
		a.modal = &Modal{
			Mode: mode, Title: title, All: items, Cursor: initial,
			Current: current, Result: done, Theme: a.theme,
		}
		a.modal.Refilter()
	})
	return <-done
}

// Prompt puts a one-line question to the user and blocks until they answer.
// An empty string is both a blank answer and a cancel — every caller treats
// the two the same, and loop does not distinguish them either.
//
// Must not be called from the render loop.
func (a *App) Prompt(label, initial string) string {
	var p *Prompt
	ready := make(chan struct{})
	a.Do(func() {
		p = NewPrompt(a.theme, label, initial)
		a.prompt = p
		close(ready)
	})
	<-ready
	return <-p.Result
}

// Toggle is the multi-select: check any number of values, `done` confirms.
// nil means cancelled, which is not the same as an empty set — the modal
// refuses to confirm one.
//
// Must not be called from the render loop.
func (a *App) Toggle(title string, values []string, initial map[string]bool) []string {
	if len(values) == 0 {
		return nil
	}
	var m *Modal
	ready := make(chan struct{})
	a.Do(func() {
		m = NewToggleModal(a.theme, title, values, initial)
		a.modal = m
		close(ready)
	})
	<-ready
	return <-m.ToggleResult
}

// Ask puts a tool call to the user and blocks until they answer.
//
// Three goroutines have to stay unblocked for this not to deadlock, and they
// do: the model layer calls this from the goroutine PRODUCING the stream, the
// turn goroutine goes on draining that stream, and the render loop — which
// owns the screen and the keyboard — is free to paint the prompt and collect
// the answer. This must never be called from the render loop itself, which
// would leave nobody to answer it.
func (a *App) Ask(tool, subject, reason string) bool {
	defer a.blocked(tool + " approval")()
	done := make(chan bool, 1)
	a.Do(func() {
		a.confirm = &Confirm{
			Theme: a.theme, Tool: tool, Subject: subject,
			Reason: reason, Result: done,
		}
	})
	return <-done
}

// Stop ends the event loop.
func (a *App) Stop() { a.done = true }

// Redraw asks for a frame: something outside the transcript changed and the
// screen no longer says the truth.
//
// It marks rather than paints, so a caller on a timer — a status-line layout
// sampling the OS every second — cannot paint more often than the frame
// budget allows. Only safe from the render goroutine: inside a Do, or from a
// key handler.
func (a *App) Redraw() { a.needsPaint = true }

// selectable reports whether a block can be opened — the only ones worth
// stopping on in nav mode.
func (e *entry) selectable() bool {
	switch e.kind {
	case entThinking:
		return e.text != ""
	case entTool:
		return e.tool.Output != "" || e.tool.StreamingContent != ""
	case entUser, entAssistant:
		// Prose is navigable even though it does not fold: nav is how you
		// walk back through a turn, and a transcript that skipped what was
		// said would only stop on the machinery around it.
		return e.text != ""
	}
	return false
}

// moveSelection walks to the next navigable block in a direction.
func (a *App) moveSelection(delta int) {
	units := a.navigable()
	if len(units) == 0 {
		return
	}
	at := -1
	for i, idx := range units {
		if idx == a.sel {
			at = i
			break
		}
	}
	next := at + delta
	if at < 0 {
		// Selection is not on a unit (a fold just swallowed it): step in from
		// the nearest end rather than jumping somewhere arbitrary.
		next = 0
		if delta < 0 {
			next = len(units) - 1
		}
	}
	if next < 0 || next >= len(units) {
		return
	}
	a.setSelection(units[next])
}

func (a *App) setSelection(i int) {
	if a.sel >= 0 && a.sel < len(a.entries) {
		a.entries[a.sel].selected = false
		a.entries[a.sel].dirty = true
	}
	a.sel = i
	if i >= 0 && i < len(a.entries) {
		a.entries[i].selected = true
		a.entries[i].dirty = true
	}
}

// enterNav puts the keyboard on the transcript, selecting the last openable
// block so the arrows have somewhere to start.
func (a *App) enterNav() {
	a.nav = true
	units := a.navigable()
	if len(units) == 0 {
		a.sel = -1
		return
	}
	a.setSelection(units[len(units)-1])
}

func (a *App) exitNav() {
	a.nav = false
	a.setSelection(-1)
}

// navKey handles a key while the transcript owns the keyboard. It returns
// false for keys nav does not claim.
func (a *App) navKey(k Key) bool {
	switch k.Kind {
	case KeyUp:
		a.moveSelection(-1)
	case KeyDown:
		a.moveSelection(1)
	case KeyRight:
		// On a folded run, the first open reveals its members rather than
		// expanding any one of them.
		if _, folded := a.groups()[a.sel]; folded {
			a.groupsOpen[a.sel] = true
			a.dirtyAll()
			return true
		}
		if a.sel >= 0 && a.sel < len(a.entries) {
			a.entries[a.sel].expanded = true
			a.entries[a.sel].dirty = true
		}
	case KeyLeft:
		if a.sel >= 0 && a.sel < len(a.entries) {
			if a.entries[a.sel].expanded {
				a.entries[a.sel].expanded = false
				a.entries[a.sel].dirty = true
				return true
			}
			// Already closed: fold the run this row belongs to back up.
			for start, g := range findGroups(a.entries, nil) {
				if a.sel >= start && a.sel < start+g.count {
					delete(a.groupsOpen, start)
					a.setSelection(start)
					a.dirtyAll()
					return true
				}
			}
		}
	case KeyEsc:
		a.exitNav()
	case KeyCtrlE:
		// Only a REAL ctrl+e. A terminal macro that sends the same byte —
		// Ghostty's cmd+→ — is a line-navigation key, and inside nav it must
		// do nothing rather than drop the user out of the transcript.
		if !navToggle(k) {
			return false
		}
		a.exitNav()
	case KeyRune:
		if k.Rune == 'e' {
			a.expandAll = !a.expandAll
			a.dirtyAll()
			return true
		}
		return false
	default:
		return false
	}
	return true
}

func (a *App) dirtyAll() {
	for _, e := range a.entries {
		e.dirty = true
	}
}

func (a *App) onKey(k Key) {
	// An approval owns the keyboard outright: nothing else may be reached
	// while a call is waiting on an answer.
	if a.confirm != nil {
		if a.confirm.Handle(k) {
			a.confirm = nil
		}
		return
	}
	if a.modal != nil {
		if a.modal.Handle(k, a.keys) {
			a.modal = nil
		}
		return
	}
	if a.prompt != nil {
		if a.prompt.Handle(k, a.keys.PasteText()) {
			a.prompt = nil
		}
		return
	}
	if a.nav {
		if a.navKey(k) {
			return
		}
		a.exitNav()
	}
	switch k.Kind {
	case KeyCtrlE:
		if navToggle(k) {
			a.enterNav()
			return
		}
		// Not the chord — it is a macro's `\x05`, which means end-of-line.
		// Fall through to the editor.
	case KeyPageUp:
		a.scroll = min(a.scroll+a.size.Rows/2, max(a.transcriptLen()-1, 0))
		return
	case KeyPageDown:
		a.scroll = max(a.scroll-a.size.Rows/2, 0)
		return
	}
	// During a turn the editor still accepts keys, but Esc interrupts
	// instead of clearing.
	if a.loader != nil && k.Kind == KeyEsc {
		a.Events <- Event{Kind: EvInterrupt}
		return
	}
	// The chords the composer does not own. Handled before the editor so a
	// binding can never be swallowed by a completion menu or a draft.
	switch k.Kind {
	case KeyBacktab:
		a.Events <- Event{Kind: EvCycleAgent}
		return
	case KeyCtrlG:
		a.Events <- Event{Kind: EvContinue}
		return
	case KeyCtrlL:
		a.Events <- Event{Kind: EvClearScreen}
		return
	case KeyCtrlP:
		a.Events <- Event{Kind: EvCycleModel}
		return
	}
	if k.Kind == KeyPaste {
		// A paste may be a file path; InsertDraft decides.
		a.InsertDraft(a.keys.PasteText())
		return
	}
	submit, quit, handled := a.editor.Handle(k, a.keys.PasteText())
	if !handled {
		return
	}
	if quit {
		a.Events <- Event{Kind: EvQuit}
		return
	}
	if submit == "" {
		return
	}
	switch {
	case strings.HasPrefix(submit, "!"):
		a.Events <- Event{Kind: EvBash, Text: submit[1:]}
	case strings.HasPrefix(submit, "/"):
		a.Events <- Event{Kind: EvSlash, Text: submit}
	default:
		a.Events <- Event{Kind: EvSubmit, Text: submit}
	}
}

// transcript renders every block for the current frame, folding runs of
// finished tool calls into a single header row.
func (a *App) transcript() []string {
	tick := AnimTick()
	groups := a.groups()
	var lines []string
	for i := 0; i < len(a.entries); i++ {
		if g, ok := groups[i]; ok {
			lines = append(lines, renderGroupHeader(a.theme, g, a.nav && a.sel == i, a.nav, a.size.Cols)...)
			i += g.count - 1
			continue
		}
		lines = append(lines, a.entries[i].render(a.theme, a.size.Cols, tick, a.expandAll, a.nav)...)
	}
	return lines
}

// groups is the set of runs currently folded. Expand-all dissolves them:
// "show me everything" must mean everything.
func (a *App) groups() map[int]group {
	// Grouping is nav's look by default, and the live variant is asking for
	// it all the time.
	//
	// Folding a run of calls into "Read 3 files" is only worth the hidden
	// detail when you can open it again, and the arrow that opens it only
	// works once the transcript has the keyboard — so out here the calls
	// stay listed unless the user has said otherwise. Saying otherwise is
	// what live is: the same fold, held while you type, for people who read
	// a turn as what it did rather than as the calls it made. It is a STATE
	// of noir and not a mode of its own, so nothing about the canvas, the
	// palette or the frame changes with it.
	if a.expandAll || (!a.nav && !a.liveVariant) {
		return nil
	}
	folded := map[int]group{}
	for start, g := range findGroups(a.entries, a.groupsOpen) {
		if !g.opened {
			folded[start] = g
		}
	}
	return folded
}

// navigable is the list of entry indices nav can stop on: every openable
// block, plus one stop per folded run standing in for its members.
func (a *App) navigable() []int {
	groups := a.groups()
	var out []int
	for i := 0; i < len(a.entries); i++ {
		if g, ok := groups[i]; ok {
			out = append(out, i)
			i += g.count - 1
			continue
		}
		if a.entries[i].selectable() {
			out = append(out, i)
		}
	}
	return out
}

func (a *App) transcriptLen() int { return len(a.transcript()) }

// render draws the live tail. Content above the viewport scrolls out into
// terminal scrollback.
func (a *App) render() {
	cols, rows := a.size.Cols, a.size.Rows

	var edLines []string
	curRow, curCol := 0, 0
	switch {
	case a.confirm != nil:
		edLines = a.confirm.View(cols)
	case a.modal != nil:
		edLines = a.modal.View(cols)
	case a.prompt != nil:
		edLines, curRow, curCol = a.prompt.View(cols)
	default:
		edLines, curRow, curCol = a.editor.View(cols)
	}
	// The plan is pinned above the editor rather than rendered into the
	// transcript, so it cannot scroll away while it is still being worked on.
	var todoLines []string
	if a.confirm == nil && a.prompt == nil && len(a.todos) > 0 {
		// No rule of its own underneath: the composer's own top border is the
		// next row, and two rules stacked read as an empty box between the
		// plan and the prompt.
		todoLines = renderTodos(a.theme, a.todos, cols)
	}

	// A fixed two-line gap between the transcript and the composer, which the
	// working indicator OCCUPIES rather than adds to — so the composer does
	// not jump down a line the moment a turn starts.
	gap := []string{"", ""}
	if a.loader != nil {
		gap[1] = pad(ContentIndent) + a.loader.Tick(a.theme)
	}

	status := a.statusLine(cols)
	// One blank below the status line, so the last row is never flush against
	// the terminal's edge.
	const trailing = 1
	bottom := len(gap) + len(todoLines) + len(edLines) + len(status) + trailing

	a.chromeRows = bottom
	avail := max(rows-bottom, 1)
	// Hand finished blocks that no longer fit to the terminal BEFORE
	// measuring, so what is measured is what remains.
	commit := a.retire(avail)
	blockLines := a.transcript()
	if a.scroll > len(blockLines)-1 {
		a.scroll = max(len(blockLines)-1, 0)
	}

	// The transcript GROWS DOWNWARD from wherever it started; it is never
	// padded to fill the screen. A short conversation therefore sits at the
	// top with the composer right beneath it, which is the shape loop has
	// and the alt-screen version could not: there, the frame was always a
	// full screen tall, so a two-line transcript had to be pushed to the
	// bottom behind a wall of blanks.
	//
	// Once it outgrows the screen the tail fills it and the head is handed
	// to the terminal, which keeps it as real scrollback.
	if len(blockLines) > avail {
		end := len(blockLines) - a.scroll
		blockLines = blockLines[max(end-avail, 0):end]
	}

	// Pinned input pads a SHORT transcript so the composer lands on the last
	// rows instead of floating under the masthead.
	//
	// Padding is all it does. It does not clip, it does not take the
	// scrolling, and it never asks for mouse reporting — asking for the wheel
	// is exactly what stops a terminal drag-selecting text, and owning the
	// scroll is what costs you the selection. The scrollback, the wheel and
	// the selection stay the terminal's.
	var padding []string
	if a.pinnedInput {
		if short := rows - (len(blockLines) + bottom); short > 0 {
			padding = make([]string, short)
		}
	}

	view := append([]string{}, padding...)
	view = append(view, blockLines...)
	view = append(view, gap...)
	view = append(view, todoLines...)
	view = append(view, edLines...)
	view = append(view, status...)
	view = append(view, "")

	editorTop := len(padding) + len(blockLines) + len(gap) + len(todoLines)
	// Hard cap at the terminal height. avail already trims the transcript,
	// but the chrome alone can exceed the screen — a tall completion menu on
	// a short terminal — and an over-tall region is not merely clipped: the
	// repaint walks the cursor up further than there is screen, the terminal
	// clamps at row 0, and every frame after that lands one row lower than
	// the last and prints a copy of itself down the page.
	if over := len(view) - rows; over > 0 {
		view = view[over:]
		editorTop -= over
	}
	// When something else owns the keyboard, parking a visible cursor in the
	// composer would lie about where typing goes.
	visible := !a.nav && a.confirm == nil && a.modal == nil
	if !visible {
		curRow, curCol = 0, 0
	}
	// Whether the region fills the screen, which decides whether a newline at
	// its last row scrolls the terminal — and so whether a sliding transcript
	// can be scrolled instead of repainted.
	a.out.full = len(view) >= rows
	a.out.Paint(commit, view, cols, editorTop+curRow, curCol, visible)
	logFrame(commit, view)
}

// logFrame dumps the frame the renderer BUILT, which is the discriminator
// when the screen is wrong: if the view already has the defect, it is a
// layout bug; if the view is right and the screen is not, it is the diff.
func logFrame(commit, view []string) {
	path := os.Getenv("PI_FRAME_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "=== frame commit=%d view=%d\n", len(commit), len(view))
	for i, l := range commit {
		fmt.Fprintf(f, "C%02d %q\n", i, stripANSI(l))
	}
	for i, l := range view {
		fmt.Fprintf(f, "V%02d %q\n", i, stripANSI(l))
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// statusLine renders loop's two-row footer.
//
// Two rows, not a filled bar: identity on top, usage below, parts joined by
// ` · `. A background-filled strip reads as chrome and gets ignored; plain
// dim text on the canvas is read, which matters because this is where the
// cost and the context pressure live.
func (a *App) statusLine(width int) []string {
	t := a.theme

	// Identity.
	agent := t.Fg(SlotDim, "agent default")
	if a.agent != "" && a.agent != "default" {
		agent = t.Fg(SlotAccent, "agent "+a.agent)
	}
	// The hint rides on the agent segment because shift+tab is the only way
	// to change it, and an agent selector nobody knows the key for is one
	// nobody uses.
	agent += t.Fg(SlotDim, " (shift+tab)")
	model := a.model
	if model == "" {
		model = "no-model"
	}
	// A model that does not reason has no level to show, and printing "off"
	// next to it reads as a setting the user turned off rather than one the
	// model never had.
	if a.modelReasoning && a.thinkingLevel != "" && a.thinkingLevel != "off" {
		model += " • " + a.thinkingLevel
	}
	identity := []string{agent, t.Fg(SlotAccent, model)}
	if a.planning {
		identity = append(identity, t.Fg(SlotWarning, "plan mode (read-only)"))
	}

	// Usage.
	session := "unsaved"
	if a.session != "" {
		session = a.session
		if len(session) > 8 {
			session = session[:8]
		}
	}
	usage := []string{
		t.Fg(SlotDim, "session "+session),
		t.Fg(SlotSuccess, fmt.Sprintf("$%.4f", a.cost)),
		a.contextPart(),
	}
	// Timer before clock: the countdown is the segment that is changing, and
	// a segment that moves is easier to find next to a fixed one than after it.
	if !a.timerEndsAt.IsZero() {
		left := time.Until(a.timerEndsAt)
		slot := SlotDim
		if left < time.Minute {
			slot = SlotWarning
		}
		usage = append(usage, t.Fg(slot, "timer "+FormatCountdown(left)))
	}
	if a.clock {
		usage = append(usage, t.Fg(SlotDim, FormatClock(time.Now())))
	}

	out := append(wrapParts(t, identity, width), wrapParts(t, usage, width)...)

	// Transforms run last, over the finished rows. A transform that throws
	// away the rows entirely is exactly what a layout preset does, so this
	// cannot be a "contribute a segment" hook — it has to be able to replace.
	if len(a.statusTransforms) > 0 {
		ctx := a.statusContext(width)
		for _, transform := range a.statusTransforms {
			if replaced := transform(out, ctx); replaced != nil {
				out = replaced
			}
		}
	}

	for i, l := range out {
		out[i] = fitRow(l, width)
	}
	return out
}

// contextPart is the window-pressure segment, coloured by how full it is —
// the one number here worth interrupting a reader for.
func (a *App) contextPart() string {
	t := a.theme
	if a.statMax <= 0 {
		return t.Fg(SlotDim, "ctx "+formatTokens(a.statCtx))
	}
	pct := float64(a.statCtx) / float64(a.statMax) * 100
	body := fmt.Sprintf("ctx %s/%s (%.1f%%)", formatTokens(a.statCtx), formatTokens(a.statMax), pct)
	switch {
	case pct > 90:
		return t.Fg(SlotError, body)
	case pct > 70:
		return t.Fg(SlotWarning, body)
	}
	return t.Fg(SlotDim, body)
}

// wrapParts joins segments with ` · `, breaking to a new row rather than
// truncating — a status line that drops its last segment hides exactly the
// thing that just changed.
func wrapParts(t *Theme, parts []string, width int) []string {
	sep := t.Fg(SlotDim, " · ")
	sepWidth := visibleWidth(sep)

	var lines []string
	cur, curWidth := "", 0
	for _, p := range parts {
		w := visibleWidth(p)
		if cur == "" {
			cur, curWidth = p, w
			continue
		}
		if curWidth+sepWidth+w <= width {
			cur += sep + p
			curWidth += sepWidth + w
			continue
		}
		lines = append(lines, cur)
		cur, curWidth = p, w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// formatTokens renders a count compactly: 1.2k, 262.1k, 1.0M.
func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return fmt.Sprint(n)
}

func cols(s Size) int { return s.Cols }

func comma(n int64) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return string(out)
}

// SetPlanning shows or hides the plan-mode badge.
func (a *App) SetPlanning(on bool) { a.planning = on }

// Clear empties the transcript. The session is untouched — this wipes what is
// on screen, not what the model remembers.
func (a *App) Clear() {
	a.entries = nil
	a.stream, a.thinking = nil, nil
	a.tools = map[string]*entry{}
	a.groupsOpen = map[int]bool{}
	a.sel, a.scroll = -1, 0
	a.nav = false
}

// Stats is the running usage total, for /context and /cost.
func (a *App) Stats() (in, out int64, ctxWindow int, cost float64) {
	return a.statIn, a.statOut, a.statMax, a.cost
}

// SetCWD updates the directory shown in the status bar and used for tool
// summaries.
func (a *App) SetCWD(dir string) {
	a.cwd = dir
	for _, e := range a.entries {
		if e.kind == entTool {
			e.tool.CWD = dir
			e.dirty = true
		}
	}
}

// SetAgent names the active agent in the status line.
func (a *App) SetAgent(name string) { a.agent = name }

// SetThinking shows the reasoning level in the status line.
func (a *App) SetThinking(level string) { a.thinkingLevel = level }

// SetSession names the session in the status line.
func (a *App) SetSession(id string) { a.session = id }

// ShowWelcome prints the startup masthead.
func (a *App) ShowWelcome(info WelcomeInfo) {
	a.push(&entry{kind: entNotice, lines: Welcome(a.theme, info, a.size.Cols), preformatted: true})
}

// AskChoice puts a question to the user and blocks until they answer,
// returning "" if they decline.
//
// Reuses the picker rather than inventing a dialog: the interaction is the
// same one — read a short list, choose an entry — and a second widget that
// looked almost like the picker would be worse than one that is it.
//
// Same goroutine rules as Ask: called from the turn, answered by the render
// loop, never invoked on the render loop itself.
func (a *App) AskChoice(question string, options []string) string {
	defer a.blocked("question")()
	items := make([]Item, 0, len(options))
	for _, o := range options {
		items = append(items, Item{Value: o, Label: o})
	}
	if choice := a.Pick(question, items, 0, ""); choice != nil {
		return choice.Value
	}
	return ""
}

// InsertDraft types text into the composer, as though it had been pasted.
//
// A pasted or dropped image path becomes an ATTACHMENT rather than literal
// text: pasting a path is how a terminal delivers a file, and leaving it as
// characters in the draft is never what was meant.
func (a *App) InsertDraft(text string) {
	if att, ok := DetectAttachment(text); ok {
		a.attachments = append(a.attachments, att)
		a.Print(a.theme.Fg(SlotDim, "attached "+att.Label()))
		return
	}
	a.editor.Insert(text)
}

// AddAttachment queues an image for the next message, as a drag-drop does.
func (a *App) AddAttachment(att Attachment) {
	a.attachments = append(a.attachments, att)
}

// TakeAttachments returns the pending attachments and clears them, so they
// ride exactly one prompt.
func (a *App) TakeAttachments() []Attachment {
	out := a.attachments
	a.attachments = nil
	return out
}

// Attachments is the pending list, for the composer hint.
func (a *App) Attachments() []Attachment { return a.attachments }

// Background is what the terminal was measured to be sitting on, for callers
// choosing a default theme.
func (a *App) Background() Background { return a.background }

// SetLiveVariant turns loop's live look on or off: runs of finished tool
// calls stay folded while you are typing, not only while navigating.
//
// Navigation folds regardless, so entering and leaving nav does not disturb
// this — which is the bug loop had to fix twice: leaving nav turned live off
// unconditionally, and a user whose transcript is always folded saw it
// un-fold itself every time they stopped navigating.
func (a *App) SetLiveVariant(on bool) {
	if a.liveVariant == on {
		return
	}
	a.liveVariant = on
	// Folding changes which blocks are drawn at all, and a folded run's
	// header is not any one block's cache.
	a.dirtyAll()
}

// LiveVariant reports whether the live look is on.
func (a *App) LiveVariant() bool { return a.liveVariant }

// StatusHooks report what the app is doing to whoever is watching this pane —
// a multiplexer's sidebar, a notifier, a terminal title.
//
// The seams are the two authorities that already know, which is why this is
// two callbacks and not a state machine: the working indicator is the only
// thing that starts and ends a working stretch, and the two blocking prompts
// below are the only places the AGENT stops and waits for the user. A menu
// the user opened themselves is not the agent being blocked, so pickers,
// /settings and the like deliberately do not report.
type StatusHooks struct {
	// Working is called when a working stretch starts (true) and ends.
	Working func(on bool)
	// Blocked is called when an agent-driven prompt opens; the function it
	// returns is called when that prompt closes. Prompts nest.
	Blocked func(label string) func()
}

// SetStatusHooks installs the pane-status seams. Zero fields are ignored, so
// a consumer can take one signal and not the other.
func (a *App) SetStatusHooks(h StatusHooks) { a.hooks = h }

// blocked reports an agent-driven wait, returning its closer. Always safe to
// call and always safe to call the result, hook or no hook.
func (a *App) blocked(label string) func() {
	if a.hooks.Blocked == nil {
		return func() {}
	}
	done := a.hooks.Blocked(label)
	if done == nil {
		return func() {}
	}
	return done
}

// SetPinnedInput holds the composer on the last rows of the screen, padding a
// short transcript above rather than letting it float under the masthead.
func (a *App) SetPinnedInput(on bool) {
	if a.pinnedInput == on {
		return
	}
	a.pinnedInput = on
	// NO out.Reset() here. The region's height changes, but it is still
	// exactly where the writer thinks it is, and Paint already handles a
	// region growing or shrinking in place. Reset forgets the region's
	// position, so the next frame is written BELOW the old one instead of
	// over it — which printed the masthead twice, since this is called once
	// at startup while the first frame is already on screen.
	//
	// Reset belongs only where the region genuinely moved out from under us:
	// after Suspend, after a resize, after Wipe.
	a.dirtyAll()
}

// SetClock shows or hides the status-line clock. It also keeps the frame
// clock running — see live.
func (a *App) SetClock(on bool) { a.clock = on }

// SetTimer shows a countdown in the status line, or clears it with the zero
// time. The countdown is also what keeps the frame clock running — see live.
func (a *App) SetTimer(endsAt time.Time) { a.timerEndsAt = endsAt }

// TimerEndsAt is the running countdown's deadline, zero when none is set.
func (a *App) TimerEndsAt() time.Time { return a.timerEndsAt }

// FormatCountdown renders a remaining duration as a clock reads it:
// "45s" / "12:34" / "1:02:03" / "2d 01:02:03".
//
// Colon-separated rather than unit-suffixed ("1h2m"): a countdown is read at
// a glance against a wall clock, and the sub-units must stay zero-padded and
// in fixed positions or the eye has to re-parse the string every second.
// Seconds round UP, so a timer never shows "0s" while it is still running.
func FormatCountdown(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int((d + time.Second - 1) / time.Second)
	days := total / 86400
	hours := (total % 86400) / 3600
	mins := (total % 3600) / 60
	secs := total % 60

	switch {
	case days > 0:
		return fmt.Sprintf("%dd %02d:%02d:%02d", days, hours, mins, secs)
	case hours > 0:
		return fmt.Sprintf("%d:%02d:%02d", hours, mins, secs)
	case mins > 0:
		return fmt.Sprintf("%d:%02d", mins, secs)
	default:
		return fmt.Sprintf("%ds", secs)
	}
}

// FormatClock is the status-line clock: "2026-06-13 21:48:32".
//
// Absolute and sortable rather than "Mon 21:48" — the clock shares a row with
// the cost and the context pressure, which are all things a user copies into
// a note, and a weekday is not enough to date one.
func FormatClock(at time.Time) string { return at.Format("2006-01-02 15:04:05") }

// FormatWhen labels a scheduled time in a list — FormatClock without seconds.
func FormatWhen(at time.Time) string { return at.Format("2006-01-02 15:04") }
