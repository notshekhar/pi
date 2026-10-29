package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/notshekhar/pi/internal/core/cmux"
	"github.com/notshekhar/pi/internal/core/shells"
	"github.com/notshekhar/pi/internal/core/sound"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/build"
	"github.com/notshekhar/pi/internal/core/catalog"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/extension"
	"github.com/notshekhar/pi/internal/core/gateway"
	"github.com/notshekhar/pi/internal/core/herdr"
	"github.com/notshekhar/pi/internal/core/hooks"
	"github.com/notshekhar/pi/internal/core/mcp"
	"github.com/notshekhar/pi/internal/core/serve"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/skills"
	"github.com/notshekhar/pi/internal/core/status"
	"github.com/notshekhar/pi/internal/core/tools"
	"github.com/notshekhar/pi/internal/tui"
)

// repl is the REPL: the app renders, the glue pumps events into turns.
// repl is the interactive program as one session sees it: the app-wide
// state (shared) and the session it acts on (slot). Most code runs on the
// foreground's repl; a turn pins its own (pinned) so it keeps writing to its
// own session after the user switches away — loop's makeStateView.
type repl struct {
	*shared
	*slot
}

// shared is the app-wide half (loop's SharedState).
type shared struct {
	app *tui.App

	mu sync.Mutex
	// cycleCustomAgent is the one non-cycle agent shift+tab also visits: a
	// custom agent or hidden built-in the user explicitly selected.
	cycleCustomAgent string
	// inPanel is set while a picker goroutine is running, so a panel opened
	// from inside one runs inline instead of racing it for the keyboard.
	inPanel atomic.Bool
	// live is the /rc server when it is on.
	live *liveServe

	mcp      *mcp.Manager
	server   *serve.Server
	telegram *gateway.Telegram
	// status is what this pane is doing, for anything watching from outside.
	status *status.Bus
	// tab is the terminal's title: the session's name and its state.
	tab tabTitle
	// timerLabel is what the user typed for the running countdown, so
	// /timer can report "45m left (of 1h)".
	timerLabel string
	// bg holds background tasks; daemonStop closes to stop the scheduler,
	// and its nil-ness is what "is the daemon running" means.
	bg         bgSet
	daemonStop chan struct{}
	// slots are the live sessions (slots.go).
	slots *slotSet
}

// slot is one live session (loop's SessionSlot): its run, its turn, its
// queue, its transcript.
type slot struct {
	// idleCompacting is an idle compaction (agent/idlecompact.go) running
	// after this slot's turn ended; it owns its own closing message.
	idleCompacting bool
	cfg            config.Config
	run            *agent.Run
	cancel         context.CancelFunc
	// queue is input typed while a turn runs, drained FIFO (queue.go).
	queue   []string
	turning bool
	// restorePersona holds the session's own agent prompt while a one-shot
	// `/<agent>` command borrows the slot for a single turn. nil when nothing
	// is borrowed.
	restorePersona *borrowedAgent
	// planViaAgent records that plan mode was armed by SELECTING the plan
	// agent rather than by /plan. Only then does leaving the agent turn it
	// back off — a gate set deliberately must survive browsing the list.
	planViaAgent bool
	// lastContextTokens is the input-token count the provider reported for
	// the last turn: what the window ACTUALLY held, as against the chars/4
	// estimate everything else here uses.
	lastContextTokens int
	// costs is the session's spend tracker (see tracker()).
	costs    *agent.CostTracker
	costsFor *session.Session

	// agent is the active persona's name, empty for the default one.
	agent string
	// conv is this session's transcript and status, on screen or not.
	conv *tui.Conversation
	// key is process-local and stable for the slot's life.
	key int
	// liveStatus / activity / lastActivity are the roster's view of it:
	// idle | working | needs-input | done | failed.
	liveStatus   string
	activity     string
	lastActivity time.Time
	// firstPrompt titles it in the roster until it earns a name.
	firstPrompt string
	// draft is the composer text while it is out of sight.
	draft string
	// waiters are agent-driven prompts parked until it is on screen.
	waiters []chan bool
}

// toolContext builds the tool set from the stored feature switches, so a
// toggle in /settings actually removes the tool rather than only recording a
// preference.
func toolContext(cfg config.Config) *tools.Context {
	stored := config.LoadSettings()
	return &tools.Context{
		CWD:       cfg.CWD,
		Registry:  tools.NewRegistry(),
		Todos:     &tools.TodoList{},
		WebSearch: stored.WebSearchOn(),
		Sandbox:   sandboxMode(stored.Sandbox),
		NoAsk:     !stored.AskUserOn(),
		NoMemory:  !stored.MemoryOn(),
		NoTodos:   !stored.TodosOn(),
	}
}

// replTUI runs the interactive app, on resumed when given, and returns the id
// of the session it ended on (for the resume hint).
func replTUI(parent context.Context, cfg config.Config, resumed *session.Session) (string, error) {
	app, err := tui.NewApp(cfg.FullID(), cfg.CWD)
	if err != nil {
		return "", err
	}
	app.SetClipboard(clipboardWrite)
	app.SetURLOpener(func(u string) { openBrowser(u) })
	defer app.Close()
	// A stored theme wins; with none, ask the terminal what it is sitting on
	// rather than assuming dark and painting over a light background.
	//
	// Always applied, even when it resolves to the default: NewApp leaves the
	// canvas alone precisely so this is the single place that washes it, which
	// is what keeps `system` from flashing night's grey on the way past.
	app.SetTheme(tui.InitTheme(configuredThemeName()))

	sess := resumed
	if sess == nil {
		sess = session.New(cfg.FullID(), cfg.CWD)
	}
	first := &slot{cfg: cfg, run: &agent.Run{Config: cfg, Session: sess, Tools: toolContext(cfg)},
		conv: app.Foreground(), liveStatus: "idle", lastActivity: time.Now()}
	t := &repl{shared: &shared{app: app, status: status.New(status.DefaultSettle)}, slot: first}
	t.slots = newSlotSet(first)
	// What the pane is doing, published from the two authorities that already
	// know: the working indicator, and the prompts where the AGENT stops and
	// waits for an answer. Menus the user opened themselves are not the agent
	// being blocked, so they are deliberately not reported.
	app.SetStatusHooks(tui.StatusHooks{
		Working: func(on bool) {
			if on {
				t.status.SetWorking()
				return
			}
			t.status.SetIdle()
		},
		Blocked: t.status.ModalOpened,
	})
	// Mirrored to herdr when this is a herdr pane, and inert when it is not.
	// The session is read at send time, never captured: /new, /resume and
	// fork all move it.
	reporter := herdr.Attach(t.status, herdr.Options{
		Disabled: !config.LoadSettings().HerdrOn(),
		Session: func() herdr.Session {
			id := t.run.Session.ID
			if id == "" {
				return herdr.Session{}
			}
			return herdr.Session{ID: id, Path: t.run.Session.Path}
		},
	})
	defer reporter.Release()
	// cmux (inert outside a cmux pane): every lifecycle event pi emits as a
	// hook is mirrored into its Feed, the status chip follows the bus, and
	// the prompts that stop the agent become actionable cards.
	cmuxReporter := cmux.Attach(t.status, cmux.Options{
		Disabled: !config.LoadSettings().CmuxOn(),
		Session: func() (cmux.Session, bool) {
			// An unsaved session is not one yet (loop's state.session is
			// null until the first turn creates it): events wait for it.
			s := t.run.Session
			if s == nil || !s.Saved() {
				return cmux.Session{}, false
			}
			return cmux.Session{ID: s.ID, Path: s.Path}, true
		},
		CWD: func() string { return t.cfg.CWD },
	})
	cmux.Set(cmuxReporter)
	defer cmuxReporter.Release()
	// Notification hook on agent-driven waits (Claude Code parity): when a
	// prompt opens mid-turn, external watchers get the "needs attention"
	// signal. Gated on a turn running, so user-opened menus don't ping.
	t.status.On(func(e status.Event) {
		if e.State != status.Blocked || !t.anyTurning() {
			return
		}
		label := e.Label
		if label == "" {
			label = "prompt"
		}
		go t.fireHook(context.Background(), hooks.Context{
			Event: hooks.Notification, Message: "Waiting for input: " + label, Title: "pi",
		})
	})
	// Name the tab immediately. A resumed session carries its own title; a
	// fresh one says which agent this pane is until its first turn earns
	// one — either beats the stale shell command a terminal shows otherwise.
	stopTab := t.tab.attach(t.status, app.SetTitle, tabNameFor(first))
	defer stopTab()
	t.mcp = mcp.NewManager()
	defer t.mcp.Close()
	// Language servers and anything else an extension spawned. Deferred here
	// so it runs on every exit path, including a panic.
	defer extension.Shutdown()
	// Background shells are process-lifetime: nothing adopts them later.
	defer shells.KillAll()
	defer func() {
		if t.live != nil {
			t.live.h.Stop()
		}
	}()
	defer func() {
		if t.server != nil {
			t.server.Stop()
		}
	}()
	// Each extension gets its own namespaced settings store before anything
	// asks it for a prompt or a status.
	extension.Bind(func(name string) extension.Store { return config.OpenExtensionStore(name) })
	// A vitals layout samples the OS on a timer, and its numbers change with
	// no user action — without a repaint hook the clock and the CPU figure
	// would sit frozen until the next keystroke.
	extension.SetRepaint(func() { app.Do(app.Redraw) })
	// A command with no argument opens a picker, the same as every builtin.
	extension.SetUI(extensionUI{t: t})
	// The same function /reload calls, so boot and reload cannot drift —
	// which is exactly how the permission rules and the extension tools ended
	// up applying on reload but not at startup.
	t.applySettings()
	// Say it, rather than quietly running on a different provider than the
	// one configured. Resolve falls back so the app still starts; staying
	// silent about it would leave the user reading a model name they never
	// chose and wondering what happened to theirs.
	if cfg.UnknownProvider != "" {
		t.dim("provider %q is no longer configured — using %s", cfg.UnknownProvider, cfg.Provider)
	}
	// The ask/plan tools and approvals go through the picker — gated on the
	// session being on screen (wireRun), as loop's roster gates them.
	t.wireRun(first)
	// PreToolUse can refuse a call outright. Installed only when something is
	// actually bound to the event, because installing it forces every tool
	// call through the approval path — a cost worth paying for a hook that
	// exists and not for one that does not.
	if cfg, err := config.LoadSettings().HookConfig(); err == nil && len(cfg[hooks.PreToolUse]) > 0 {
		t.run.PreTool = func(ctx context.Context, tool string, args map[string]any) (map[string]any, string) {
			outcome := t.fireHook(ctx, hooks.Context{
				Event: hooks.PreToolUse, ToolName: tool, ToolInput: args,
			})
			if outcome.Block {
				return nil, outcome.Reason
			}
			// A hook that returned `updatedInput` rewrites the call. Only an
			// object is accepted: a hook that answered with a string or an
			// array has not described a tool's arguments, and running the
			// original is the honest reading of that.
			if updated, ok := outcome.UpdatedInput.(map[string]any); ok && updated != nil {
				return updated, ""
			}
			return nil, ""
		}
	}
	// Extensions get the same seam, after hooks. rtk is the one that uses it:
	// `git status` becomes `rtk git status` before it runs.
	t.run.RewriteCall = func(tool string, args map[string]any) map[string]any {
		return extension.RewriteFrom(activeExtensions(), tool, args)
	}
	t.run.OnPermissionRequest = func(ctx context.Context, tool string, args map[string]any, reason string) {
		go t.fireHook(ctx, hooks.Context{
			Event: hooks.PermissionRequest, ToolName: tool, ToolInput: args,
		})
	}

	app.SetSources(t.commandItems(), fileItems(cfg.CWD))

	stopResize := watchResize(func() { app.Do(app.Resize) })
	defer stopResize()
	// Restore the saved persona — but never `plan`. Plan mode is a mode you
	// are in, not a preference you hold, and a fresh session starting
	// read-only because the last one ended mid-plan is a surprise nobody can
	// explain.
	t.restoreAgent()
	app.SetThinking(thinkingName(cfg.Reasoning))
	// "unsaved" until the first message claims the session (loop's rule).
	if sess.Saved() {
		app.SetSession(sess.ID)
	}
	if m, ok := config.ModelInfo(cfg.Provider, cfg.ModelID); ok {
		app.SetUsage(0, 0, m.Context, 0)
		app.SetModelReasoning(m.Reasoning)
	}
	// A resumed session's context is its last request's, as loop's
	// seedFromSession reports it.
	if sess.Saved() {
		app.SetContextTokens(int(agent.NewCostTracker().SeedFromSession(sess)))
	}
	app.ShowWelcome(t.welcomeInfo())
	t.startupNotices()
	// loop is up and the first frame is about to go out.
	go sound.Cue(sound.Bloom)
	// A resumed session: the conversation replays under the banner, and its
	// usage is restored instead of reading zero until the next message.
	if resumed != nil {
		app.Do(func() {
			t.replayTranscript(resumed)
			app.SeedUsage(resumed.Meta.InputTokens, resumed.Meta.OutputTokens, resumed.Meta.CostUSD)
			app.SetCostText(t.tracker().Format())
		})
	}

	t.startMCP(parent)
	go t.fireHook(parent, hooks.Context{Event: hooks.SessionStart})
	go t.pump(parent)
	err = app.Run()
	return t.run.Session.ID, err
}

// pump is the only goroutine that talks to the model. App mutations go
// through app.Do so the render loop owns the screen.
func (t *repl) pump(parent context.Context) {
	for ev := range t.app.Events {
		switch ev.Kind {
		case tui.EvQuit:
			t.mu.Lock()
			if t.cancel != nil {
				t.cancel()
			}
			t.mu.Unlock()
			// Synchronous, and the last thing that happens: a SessionEnd hook
			// that fired in a goroutine would routinely be killed by the exit
			// before it did anything.
			t.fireHook(context.Background(), hooks.Context{Event: hooks.SessionEnd, Reason: "exit"})
			t.app.Stop()
			return
		case tui.EvInterrupt:
			// Esc stops the turn but keeps the queue: what was queued runs
			// next, as soon as the interrupted turn has wound down.
			t.mu.Lock()
			if t.cancel != nil {
				sound.Cue(sound.Press)
				t.cancel()
			}
			t.mu.Unlock()
		case tui.EvAbort:
			// ctrl+c mid-turn: stop it AND drop everything queued behind it.
			t.clearQueue()
			t.mu.Lock()
			if t.cancel != nil {
				t.cancel()
			}
			t.mu.Unlock()
		case tui.EvSubmit:
			if t.enqueue(ev.Text) {
				continue
			}
			t.do(func() { t.app.UserEcho(ev.Text) })
			// Synchronous, unlike the others: this hook can REFUSE the prompt,
			// and starting the turn while it decides would make the refusal
			// arrive after the model had already been called.
			prompt := ev.Text
			// Extensions watching for a phrase ("stop caveman") see the
			// prompt before the model does.
			extension.NotifyTurn(activeExtensions(), prompt)
			go func() {
				outcome := t.fireHook(parent, hooks.Context{
					Event: hooks.UserPromptSubmit, Prompt: prompt,
				})
				if outcome.Block {
					t.drainNext()
					return
				}
				// A hook may add context for the model — a failing build, the
				// current ticket — which rides in front of the prompt.
				if outcome.AdditionalContext != "" {
					prompt = outcome.AdditionalContext + "\n\n" + prompt
				}
				t.startTurn(parent, prompt)
			}()
		case tui.EvSlash:
			if t.enqueue(ev.Text) {
				continue
			}
			t.slash(parent, ev.Text)
			// A command that started a turn drains nothing now; the turn
			// drains the queue when it ends.
			t.drainNext()
		case tui.EvBash:
			if t.enqueue("!" + ev.Text) {
				continue
			}
			t.bang(ev.Text)
		case tui.EvCycleAgent:
			t.cycleAgent()
		case tui.EvContinue:
			// "continue" typed for you. Resuming interrupted work is the most
			// repeated prompt there is, and it is always the same word.
			t.submit(parent, "continue")
		case tui.EvClearScreen:
			t.clearScreen()
		case tui.EvClipboardPaste:
			go t.clipboardPaste(ev.Text == "image")
		case tui.EvPickImage:
			go func() {
				if path := pickImageFile(); path != "" {
					t.attach(path)
				}
			}()
		case tui.EvCycleModel:
			t.cycleModel()
		case tui.EvSwitcher:
			go t.showSwitcher()
		}
	}
}

func (t *repl) startTurn(parent context.Context, prompt string) {
	// Pinned: the turn writes to ITS session even after a switch.
	t = t.pinned()
	t.mu.Lock()
	if t.turning {
		t.mu.Unlock()
		t.dim("(turn in progress — Esc to interrupt)")
		return
	}
	t.turning = true
	ctx, cancel := context.WithCancel(parent)
	t.cancel = cancel
	t.mu.Unlock()

	started := time.Now()
	if t.firstPrompt == "" && !strings.HasPrefix(prompt, "/") {
		t.firstPrompt = strings.SplitN(strings.TrimSpace(prompt), "\n", 2)[0]
	}
	t.setLiveStatus(t.slot, "working", "Generating")
	// Everything the agent said this turn, across steps: the Stop payload
	// and the session title both read it.
	var said strings.Builder
	go func() {
		// loop's sound reporter: one cue per finished turn, error if anything
		// went wrong; an interrupted turn stays silent (Esc played its own).
		turnFailed := false
		defer func() { t.settle(t.slot, turnFailed && ctx.Err() == nil) }()
		defer func() {
			if ctx.Err() == nil {
				if turnFailed {
					sound.Cue(sound.Error)
				} else {
					sound.Cue(sound.Success)
				}
			}
		}()
		defer func() {
			t.mu.Lock()
			t.turning = false
			t.cancel = nil
			// A one-shot agent command borrowed the persona for this turn
			// only; give it back now that the turn is over.
			if t.restorePersona != nil {
				t.run.Persona, t.run.AllowedTools = t.restorePersona.persona, t.restorePersona.tools
				t.restorePersona = nil
			}
			t.mu.Unlock()
			elapsed := time.Since(started)
			// The session claims its id on its first message, so the status
			// line only learns it here — before this it correctly says
			// "unsaved".
			id := t.run.Session.ID
			cost := t.tracker().Format()
			t.do(func() {
				t.app.SetSession(id)
				t.app.SetCostText(cost)
				t.app.LoaderStop()
				// Anything still open was cut short by an interrupt or a
				// stream that ended mid-block; settle it so nothing animates
				// forever waiting for a part that will never arrive.
				t.app.InterruptOpen()
				t.retireTodos()
				// Closes every turn, including an interrupted or failed one:
				// how long it ran is exactly what you want to know when it
				// did not finish.
				t.app.TurnSummary(elapsed)
			})
			if t.live != nil {
				t.live.turnEnded()
			}
			go t.fireHook(context.Background(), hooks.Context{
				Event: hooks.Stop, LastAssistantMessage: clipRunes(strings.TrimSpace(said.String()), 4_000),
			})
			// Name the session from its opening exchange, once. Detached:
			// the prompt is already free and a title is never worth making
			// anyone wait for.
			go t.maybeTitleSession(prompt, said.String(), ctx.Err() != nil)
			t.drainNext()
		}()

		t.do(func() { t.app.LoaderStart() })

		// loop's runTurn: the prompt, auto-compaction BEFORE the request,
		// each step persisted as it finishes, an interrupted turn kept. The
		// renderer is fed the raw parts it always drew; the events go to
		// anything following this session (/rc).
		parts := make(chan ai.StreamPart, 64)
		rendered := make(chan error, 1)
		go func() { rendered <- t.consume(parts) }()
		final, err := t.run.RunTurn(ctx, agent.TurnOptions{
			Input:   prompt,
			Tracker: t.tracker(),
			Tap: func(p ai.StreamPart) {
				if d, ok := p.(provider.TextDelta); ok {
					said.WriteString(d.Delta)
				}
				parts <- p
			},
			Emit: t.turnEvent,
		})
		close(parts)
		if rerr := <-rendered; rerr != nil && err == nil {
			err = rerr
		}
		if err != nil {
			if ctx.Err() != nil {
				t.dim("(interrupted)")
			} else {
				turnFailed = true
				t.fail("error: %s", err)
			}
			return
		}
		t.finishUsage(final)
	}()
}

// consume renders a stream part-by-part via app.Do.
func (t *repl) consume(stream <-chan ai.StreamPart) error {
	var first error
	for part := range stream {
		part := part
		err := func() error {
			done := make(chan error, 1)
			t.do(func() { done <- t.part(part) })
			return <-done
		}()
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (t *repl) part(part ai.StreamPart) error {
	switch v := part.(type) {
	case provider.StreamStart:
		theme := t.app.Theme()
		for _, w := range v.Warnings {
			t.app.Print(theme.Fg(tui.SlotWarning, "warning: "+w.Feature+" "+w.Details))
		}

	case provider.ReasoningDelta:
		t.app.ThinkingDelta(v.Delta)
	case provider.ReasoningEnd:
		t.app.ThinkingEnd()

	case provider.TextDelta:
		// Text after reasoning closes the thinking block, which folds it to
		// its header — the turn has moved on.
		t.app.ThinkingEnd()
		t.app.AssistantDelta(v.Delta)

	case provider.ToolInputStart:
		t.app.ThinkingEnd()
		t.app.AssistantEnd()
		t.app.ToolStart(v.ID, v.ToolName, nil)
	case provider.ToolInputDelta:
		t.app.ToolInputDelta(v.ID, v.Delta)
	case provider.ToolCall:
		// The complete arguments have landed, so the row can show a real
		// summary instead of whatever the partial JSON allowed.
		t.app.ToolArgs(v.ToolCallID, decodeArgs(v.Input))
		// With a PreToolUse hook bound, PreTool fires the event itself;
		// without one the watchers still hear of the call.
		if t.run.PreTool == nil {
			t.watchHook(hooks.Context{Event: hooks.PreToolUse, ToolName: v.ToolName, ToolInput: decodeArgs(v.Input)})
		}

	case ai.ToolExecuted:
		exec := v.Execution
		if exec.Err != nil {
			t.app.ToolEnd(exec.ToolCallID, exec.Err.Error(), true)
		} else {
			text, isErr := resultText(exec.Result)
			t.app.ToolEnd(exec.ToolCallID, text, isErr)
		}
		if exec.ToolName == "todo" {
			t.app.SetTodos(todoItems(t.run.Tools.Todos.Items()))
		}
		// Off the render loop: a hook is a subprocess, and blocking the
		// renderer on one would freeze the screen while it runs.
		var out any
		if exec.Err != nil {
			out = exec.Err.Error()
		} else {
			out, _ = resultText(exec.Result)
		}
		go t.fireHook(context.Background(), hooks.Context{
			Event: hooks.PostToolUse, ToolName: exec.ToolName, Success: exec.Err == nil,
			ToolInput: decodeArgs(string(exec.Input)), ToolOut: out,
		})

	case provider.ErrorPart:
		return v.Err
	}
	return nil
}

// decodeArgs parses a tool call's JSON arguments. A call whose arguments do
// not parse still renders — with no summary rather than no row.
func decodeArgs(input string) map[string]any {
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil
	}
	return args
}

// finishUsage prices the turn off the catalog and updates the footer.
func (t *repl) finishUsage(final *ai.Result) {
	if final == nil {
		return
	}
	u := measureUsage(t.cfg, final)
	in, out, cost, ctxWindow := u.in, u.out, u.cost, u.ctxWindow
	// The provider's own count of what the last request carried — the only
	// measured context figure there is, and what /context quotes.
	t.lastContextTokens = int(in)
	cached := u.cached
	t.do(func() {
		t.app.SetUsage(in, out, ctxWindow, cost)
		t.app.SetCachedTokens(cached)
	})
	// The ledger already has these rows: each step billed itself as it was
	// saved. Compaction runs at the start of the next turn (RunTurn), as
	// loop's does.
}

// tracker is the session's spend, seeded from its history on first use and
// reset when the session changes.
func (t *repl) tracker() *agent.CostTracker {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.costs == nil || t.costsFor != t.run.Session {
		t.costs = agent.NewCostTracker()
		t.costs.SeedFromSession(t.run.Session)
		t.costsFor = t.run.Session
	}
	return t.costs
}

// turnEvent is a loop turn event from RunTurn: what the renderer does not get
// from the stream parts (compaction), and the feed for /rc.
func (t *repl) turnEvent(name string, data any) {
	switch name {
	case "stream-retry":
		if e, ok := data.(agent.StreamRetryEvent); ok {
			t.dim("stream failed (%s) — retrying %d/%d", e.Reason, e.Attempt, e.Max)
		}
	case "compact-start":
		t.fireHook(context.Background(), hooks.Context{Event: hooks.PreCompact})
		start, _ := data.(agent.CompactStartEvent)
		t.mu.Lock()
		t.idleCompacting = start.Reason == "idle"
		idle := t.idleCompacting
		t.mu.Unlock()
		if !idle {
			t.dim("context full — compacting…")
		}
	case "compact-end":
		t.mu.Lock()
		idle := t.idleCompacting
		t.idleCompacting = false
		t.mu.Unlock()
		if end, ok := data.(agent.CompactEndEvent); ok && idle {
			// Cancelled because the user came back: nothing to say.
			switch {
			case end.Error != "":
				t.fail("idle compaction failed: %s", end.Error)
			case !end.Aborted && end.Summary != "":
				t.do(func() {
					t.app.Print(t.app.Theme().Fg(tui.SlotSuccess, fmt.Sprintf(
						"⟲ Compacted while idle, before the prompt cache expired · %s → %s tokens",
						formatTokens(end.TokensBefore), formatTokens(end.TokensAfter))))
				})
			}
		} else if ok {
			switch {
			case end.Aborted:
				t.dim("compaction interrupted")
			case end.Error != "":
				// Not fatal: the session is untouched and this turn may still fit.
				t.fail("auto-compact failed: %s — /compact to retry", end.Error)
			case end.TokensBefore > 0:
				t.do(func() {
					t.app.Print(t.app.Theme().Fg(tui.SlotSuccess, fmt.Sprintf(
						"auto-compacted ~%s → ~%s tokens", commas(end.TokensBefore), commas(end.TokensAfter))))
				})
			}
		}
	}
	if t.live != nil {
		t.live.publish(t.run.Session.ID, name, data)
	}
}

func (t *repl) slash(parent context.Context, line string) {
	line = expandAlias(line)
	name, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)
	if cmd := lookupCommand(name); cmd != nil {
		cmd.run(t, parent, rest)
		return
	}
	// Skills, agents, and prompt files. After the builtins so none of them
	// can shadow a core command, and resolved now rather than at boot so a
	// skill written during the session is callable without a restart.
	if cmd := t.lookupDynamic(name); cmd != nil {
		cmd.run(t, parent, rest)
		return
	}
	// Extensions are consulted AFTER the builtins, so an extension cannot
	// shadow a core command — and at dispatch time, so switching one off
	// takes its commands with it.
	if cmd, ok := extensionCommand(strings.TrimPrefix(name, "/")); ok {
		t.runExtensionCommand(parent, cmd, rest)
		return
	}
	t.dim("unknown command %s — /help", name)
}

// help lists every registered command, so it can never fall behind the
// registry the way the hand-written version did.
//
// `/name — description`, not a padded two-column table. The table looked
// tidier and read worse: with sixty-odd commands the name column is as wide
// as the longest name, so most rows carry a river of whitespace, and the
// dash keeps each row one readable phrase.
func (t *repl) help() {
	table := commands()
	// Skills, agents, and prompt files are commands too — leaving them out of
	// /help is how a user ends up not knowing their own skills are callable.
	table = append(table, t.dynamicCommands()...)
	for _, c := range extension.CommandsFrom(activeExtensions()) {
		table = append(table, command{name: c.Name, description: c.About})
	}

	t.do(func() {
		th := t.app.Theme()
		lines := make([]string, 0, len(table)+5)
		for _, c := range table {
			lines = append(lines, th.Fg(tui.SlotAccent, th.Bold("/"+c.name))+
				th.Fg(tui.SlotMuted, " — "+c.description))
		}
		lines = append(lines, "",
			th.Fg(tui.SlotAccent, th.Bold("!cmd"))+th.Fg(tui.SlotMuted, " — Run a shell command"),
			th.Fg(tui.SlotAccent, th.Bold("@file"))+th.Fg(tui.SlotMuted, " — Complete a path"),
			th.Fg(tui.SlotAccent, th.Bold("ctrl+e"))+th.Fg(tui.SlotMuted, " — Browse the transcript — ↑↓ select, → expand, e all"),
			th.Fg(tui.SlotAccent, th.Bold("esc"))+th.Fg(tui.SlotMuted, " — Interrupt a running turn"))
		t.app.Print(lines...)
	})
}

// quit asks the render loop to shut down.
func (t *repl) quit() { t.app.Events <- tui.Event{Kind: tui.EvQuit} }

// newSession starts over in a FRESH session, leaving the old one on disk.
//
// It used to reset the current session in place, which quietly destroyed the
// conversation you had just finished: `/new` is how you move on from a task,
// not how you delete the record of it. The new session is unsaved until its
// first message, so `/new` twice in a row costs nothing.
//
// The TRANSCRIPT is reset too, and the header redrawn — `/new` should leave
// the screen a new session starts with. Leaving the old conversation on
// screen was the confusing half: nothing about the display said the agent had
// stopped remembering any of it.
func (t *repl) newSession() {
	// A turn in flight is not thrown away: it carries on in the background
	// (Ctrl+S to go back) and the new session opens beside it. Only an idle
	// session is replaced in place.
	if t.isTurning() {
		t.openNew()
		t.dim("previous session keeps running in the background · ctrl+s to switch")
		return
	}
	t.firstPrompt = ""
	shells.KillSession(t.run.Session.ID)
	t.run.Session = session.New(t.cfg.FullID(), t.cfg.CWD)
	t.run.Tools.Registry = tools.NewRegistry()
	t.run.Tools.Todos.Clear()
	t.lastContextTokens = 0
	// Plan mode is a stance on a conversation, and this is a different one.
	t.run.Planning, t.planViaAgent = false, false
	t.do(func() {
		t.app.SetSession("")
		t.app.SetCostText("")
		t.app.SetPlanning(false)
		t.app.SetTodos(nil)
		t.app.ResetUsage(t.contextWindow())
		t.redrawHeader()
	})
	// The tab named the session that just went away; a fresh one sits under
	// the standing name until it earns its own.
	t.setTabName(defaultTabName())
	t.dim("new session")
}

// redrawHeader empties the transcript and paints the masthead and status
// block, exactly as a fresh session opens. Must run on the render loop.
func (t *repl) redrawHeader() {
	t.app.Clear()
	t.app.ShowWelcome(t.welcomeInfo())
	t.printStartupNotices()
}

// welcomeInfo is what the masthead reports, built fresh each time it is shown
// — /new and /clear redraw it, and by then the model or the agent may have
// changed.
func (t *repl) welcomeInfo() tui.WelcomeInfo {
	session := "unsaved"
	if t.run.Session.Saved() {
		session = t.run.Session.ID
	}
	return tui.WelcomeInfo{
		Name:    tui.UserName(),
		Version: version,
		Model:   t.cfg.FullID(),
		Branch:  tui.GitBranch(t.cfg.CWD),
		CWD:     tui.ShortenPath(t.cfg.CWD),
		Session: session,
		Agent:   t.agent,
	}
}

// contextWindow is the active model's window, 0 when the catalog does not
// know the model.
func (t *repl) contextWindow() int {
	if m, ok := config.ModelInfo(t.cfg.Provider, t.cfg.ModelID); ok {
		return m.Context
	}
	return 0
}

// providerCmd routes `/provider add|remove|custom` to the endpoint editor and
// anything else to the picker.
func (t *repl) providerCmd(rest string) {
	if verb, _, _ := strings.Cut(rest, " "); verb == "add" || verb == "remove" || verb == "custom" {
		t.customProviders(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(rest, "custom"), verb)))
		return
	}
	t.pickProvider(rest)
}

// costCmd shows this session's spend, or every session's with `all`.
func (t *repl) costCmd(rest string) {
	if rest == "all" || rest == "--all" {
		t.showCostAll()
		return
	}
	t.showCost()
}

// padRight pads a key column so the descriptions line up.
// padRight pads to n CELLS. Byte length is wrong the moment a label holds an
// arrow or any other multi-byte glyph, which every table here does.
func padRight(s string, n int) string { return tui.PadRight(s, n) }

func (t *repl) pickModel(rest string) {
	if rest != "" {
		t.cfg.ModelID = rest
		t.apply()
		sound.Cue(sound.Click)
		return
	}
	// A manager loop, not a one-shot picker: registering a model id is done
	// from inside the list, and after adding or removing one the answer to
	// "what is there now" is the list itself.
	const addModel = "\x00add"
	t.manage(func() (string, []tui.Item) {
		custom := customModels(t.cfg.Provider)
		items := []tui.Item{{
			Value:       addModel,
			Label:       "+ add model…",
			Description: "register a model id under " + t.cfg.Provider,
		}}
		for _, m := range t.providerModels() {
			label := m.ShortID
			if custom[m.ShortID] {
				label += "  (custom)"
			}
			items = append(items, tui.Item{
				Value: m.ShortID,
				Label: label,
				// Two spaces around each separator, so the three facts read as
				// three columns rather than one run-on sentence.
				Description: fmt.Sprintf("%s  ·  ctx %s  ·  $%g/$%g",
					m.Name, commaInt(m.Context), m.Cost.Input, m.Cost.Output),
			})
		}
		return "Model · " + t.cfg.Provider + " (type to filter)", items
	}, func(choice tui.Item) bool {
		// Registering a model id is an EDIT: the answer to "what is there
		// now" is the list, so it reopens with the new entry in it.
		if choice.Value == addModel {
			t.addCustomModel()
			return keepPanel
		}
		// A custom model offers removal; a catalog one is just selected.
		if customModels(t.cfg.Provider)[choice.Value] {
			action := t.choose(choice.Value,
				tui.Item{Value: "use", Label: "use", Description: choice.Value},
				tui.Item{Value: "remove", Label: "remove custom model",
					Description: "forget this model id"},
			)
			// Backing out of the sub-menu returns to the list it was opened
			// from, not out of the picker entirely.
			if action == nil {
				return keepPanel
			}
			if action.Value == "remove" {
				t.removeCustomModel(choice.Value)
				return keepPanel
			}
		}
		// Choosing a model is the whole point of the picker, so it closes.
		t.cfg.ModelID = choice.Value
		t.apply()
		sound.Cue(sound.Click)
		return closePanel
	})
}

// providerModels is the catalog for the active provider plus any model ids
// the user has registered under it, sorted the way loop sorts them.
func (t *repl) providerModels() []catalog.Model {
	ms := catalog.Models(t.cfg.Provider, config.APIKey(t.cfg.Provider))
	known := make(map[string]bool, len(ms))
	for _, m := range ms {
		known[m.ShortID] = true
	}
	for id := range customModels(t.cfg.Provider) {
		if known[id] {
			continue
		}
		// Nothing is known about a hand-registered model beyond its id — no
		// context window, no price. Reporting zeroes is honest; guessing a
		// window would produce a compaction threshold based on fiction.
		ms = append(ms, catalog.Model{
			ID: t.cfg.Provider + "/" + id, Provider: t.cfg.Provider,
			ShortID: id, Name: "custom",
		})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].ShortID < ms[j].ShortID })
	return ms
}

// sandboxMode maps loop's sandbox setting onto pi's sandbox.
func sandboxMode(s *config.SandboxSetting) tools.SandboxMode {
	if s.On() {
		return tools.SandboxWorkspace
	}
	return tools.SandboxOff
}

// customModels is the set of model ids registered under a provider in
// ~/.loop/models.json.
func customModels(provider string) map[string]bool {
	out := map[string]bool{}
	for _, id := range catalog.ListCustomModelIDs() {
		if prov, short, ok := strings.Cut(id, "/"); ok && prov == provider {
			out[short] = true
		}
	}
	return out
}

// addCustomModel registers a model id the catalog does not carry (loop's
// /model add → addCustomModel into models.json).
func (t *repl) addCustomModel() {
	provider := t.cfg.Provider
	id := strings.TrimSpace(t.ask("new model id under "+provider+"/ (e.g. some-model-v2)", ""))
	if id == "" {
		return
	}
	if _, err := catalog.AddCustomModel(catalog.CustomModelInput{Provider: provider, ModelID: id}); err != nil {
		t.fail("model: %s", err)
		return
	}
	t.dim("added %s/%s (custom). It'll error at chat time if %s doesn't serve it.", provider, id, provider)
	t.cfg.ModelID = id
	t.apply()
}

// removeCustomModel forgets a registered id.
func (t *repl) removeCustomModel(id string) {
	if _, err := catalog.RemoveCustomModel(t.cfg.Provider + "/" + id); err != nil {
		t.fail("model: %s", err)
		return
	}
	t.dim("removed custom model %s", id)
}

func (t *repl) pickProvider(rest string) {
	if rest != "" {
		if !catalog.IsProvider(rest) {
			t.dim("unknown provider %s", rest)
			return
		}
		t.cfg.Provider, t.cfg.ModelID = rest, ""
		t.apply()
		return
	}
	var items []tui.Item
	for _, p := range catalog.Providers {
		if !config.Authorized(p.ID) {
			continue
		}
		desc := p.Name
		if p.ID == t.cfg.Provider {
			desc = "(active)"
		}
		items = append(items, tui.Item{Value: p.ID, Label: p.ID, Description: desc})
	}
	pick := t.app.Pick("Provider (type to filter)", items, 0, "")
	if pick == nil {
		return
	}
	t.cfg.Provider, t.cfg.ModelID = pick.Value, ""
	t.apply()
}

func (t *repl) apply() {
	resolved, err := config.Resolve(t.cfg)
	t.do(func() {
		if err != nil {
			t.app.Print(t.app.Theme().Fg(tui.SlotError, err.Error()))
			return
		}
		t.cfg = resolved
		t.run.Config = resolved
		t.app.SetModel(resolved.FullID())
		// Whether the status line shows a thinking level at all. A model
		// missing from the catalog (a custom endpoint) keeps the previous
		// answer rather than being declared non-reasoning.
		if info, ok := config.ModelInfo(resolved.Provider, resolved.ModelID); ok {
			t.app.SetModelReasoning(info.Reasoning)
		}
		_ = config.Update(func(s *config.Settings) {
			s.DefaultModel = resolved.FullID()
		})
		t.app.Print(t.app.Theme().Fg(tui.SlotDim, "model "+resolved.FullID()))
	})
}

// bang runs a shell command inline and prints the output to the transcript.
func (t *repl) bang(cmdline string) {
	t.do(func() {
		th := t.app.Theme()
		t.app.Print(th.Fg(tui.SlotAccent, "❯ ") + th.Fg(tui.SlotMuted, cmdline))
	})
	go func() {
		cmd := exec.Command("sh", "-c", cmdline)
		cmd.Dir = t.cfg.CWD
		out, err := cmd.CombinedOutput()
		text := strings.TrimRight(string(out), "\n")
		t.do(func() {
			th := t.app.Theme()
			if text != "" {
				lines := strings.Split(text, "\n")
				styled := make([]string, len(lines))
				for i, l := range lines {
					styled[i] = th.Fg(tui.SlotToolOutput, l)
				}
				t.app.Print(styled...)
			}
			if err != nil {
				t.app.Print(th.Fg(tui.SlotError, "exit: "+err.Error()))
			}
		})
		t.drainNext()
	}()
}

// fileItems completes paths under root for the `@` trigger.
func fileItems(root string) func(string) []tui.Item {
	return func(word string) []tui.Item {
		pat := filepath.Join(root, word+"*")
		matches, _ := filepath.Glob(pat)
		items := make([]tui.Item, 0, min(len(matches), 12))
		for _, m := range matches {
			rel, err := filepath.Rel(root, m)
			if err != nil {
				continue
			}
			label := rel
			if st, err := os.Stat(m); err == nil && st.IsDir() {
				label += "/"
			}
			items = append(items, tui.Item{Value: "@" + rel, Label: label})
			if len(items) == 12 {
				break
			}
		}
		return items
	}
}

// resultText is a tool result's full output and whether it failed. The row
// keeps the whole thing: it is hidden while folded, and truncating here would
// mean expanding a row only to find the output already cut.
func resultText(result ai.ToolResult) (string, bool) {
	switch out := result.Output().(type) {
	case provider.ToolOutputText:
		return out.Value, false
	case provider.ToolOutputErrorText:
		return out.Value, true
	default:
		return fmt.Sprintf("%T", out), false
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if utf8.RuneCountInString(s) > 80 {
		return string([]rune(s)[:80]) + "…"
	}
	return s
}

func commaInt(n int) string { return comma(int64(n)) }

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

// Styled output helpers. Every line the REPL prints goes through the theme,
// so a palette swap moves the whole screen and nothing is hardcoded to a
// colour the active theme does not use.

func (t *repl) dim(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	t.do(func() { t.app.Print(t.app.Theme().Fg(tui.SlotDim, text)) })
}

func (t *repl) fail(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	t.do(func() { t.app.Print(t.app.Theme().Fg(tui.SlotError, text)) })
}

// configuredThemeName is loop's: `theme`, else the old per-mode keys.
func configuredThemeName() string {
	var theme string
	var perMode map[string]string
	config.SettingsStore().Get("theme", &theme)
	config.SettingsStore().Get("uiThemes", &perMode)
	return tui.ConfiguredThemeName(theme, perMode)
}

// setTheme is loop's switchTheme: the picker bare, else that theme.
func (t *repl) setTheme(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		go t.pickTheme()
		return
	}
	if !t.applyTheme(name) {
		t.fail("unknown theme: %s (have: %s)", name, strings.Join(tui.ThemeNames(), ", "))
	}
}

// pickTheme is the picker both /theme and the /settings row open.
func (t *repl) pickTheme() {
	cur := t.app.Theme().Name
	var items []tui.Item
	for _, n := range tui.ThemeNames() {
		desc := ""
		if n == cur {
			desc = "(current)"
		}
		items = append(items, tui.Item{Value: n, Label: n, Description: desc})
	}
	if pick := t.app.Pick("Theme (type to filter)", items, 0, ""); pick != nil {
		t.applyTheme(pick.Value)
	}
}

// applyTheme applies and persists a theme; unknown names are refused rather
// than resolving to the fallback, which would look like a typo working.
func (t *repl) applyTheme(name string) bool {
	known := false
	for _, n := range tui.ThemeNames() {
		if n == name {
			known = true
		}
	}
	th, ok := tui.LoadTheme(name)
	if !known || !ok {
		return false
	}
	_ = config.SettingsStore().Set("theme", name)
	t.do(func() { t.app.SetTheme(th) })
	t.dim("theme → %s", name)
	return true
}

// version is what the masthead reports; see build.Version for why it lives
// in a package rather than here.
const version = build.Version

// startupNotices prints the status block under the masthead: the things that
// silently change how a session behaves, stated once where they are read.
func (t *repl) startupNotices() {
	t.do(t.printStartupNotices)
}

// printStartupNotices is the body, for callers already on the render loop.
func (t *repl) printStartupNotices() {
	th := t.app.Theme()
	var lines []string

	// A count and then the files themselves, one per line.
	//
	// Not a comma-joined list: these are paths, and the question this block
	// answers is "which file is the agent reading", which a bare "AGENTS.md"
	// cannot answer when the one being read is two directories up. Named
	// either way — "none" alone leaves the reader wondering what was looked
	// for.
	if found := contextFiles(t.cfg.CWD); len(found) > 0 {
		lines = append(lines, th.Fg(tui.SlotDim, fmt.Sprintf(" workspace context (%d):", len(found))))
		for _, f := range found {
			lines = append(lines, th.Fg(tui.SlotDim, "   • "+tui.ShortenPath(f)))
		}
	} else {
		lines = append(lines, th.Fg(tui.SlotDim, " workspace context: none (AGENTS.md, CLAUDE.md not found)"))
	}

	// Extensions, with each one's mode where it has one. An extension that is
	// on and pointed the wrong way is invisible otherwise — you can see that
	// caveman is enabled and not that it is set to `ultra`.
	if active := activeExtensions(); len(active) > 0 {
		names := make([]string, 0, len(active))
		for _, e := range active {
			name := e.Name()
			if status := extension.StatusOf(e); status != "" {
				name += " (" + status + ")"
			}
			names = append(names, name)
		}
		lines = append(lines, th.Fg(tui.SlotDim, " extensions: "+strings.Join(names, " · ")))
	}

	if found := skills.Load(t.cfg.CWD); len(found) > 0 {
		lines = append(lines, th.Fg(tui.SlotDim, fmt.Sprintf(" skills (%d):", len(found))))
		for _, s := range found {
			lines = append(lines, th.Fg(tui.SlotDim, "   • "+s.Name+" — "+elide(s.Description, 80)))
		}
	}
	if t.run.Tools.WebSearch {
		lines = append(lines, th.Fg(tui.SlotDim, " websearch: on"))
	}
	if t.run.Planning {
		lines = append(lines, th.Fg(tui.SlotWarning, " plan mode: read-only"))
	}
	// No trailing blank: the two-line gap above the composer already
	// separates this from the input, and adding one here made three.
	t.app.PrintRaw(lines...)
}

// contextFiles are the agent-instruction files present in the working
// directory.
// Returns full paths, not bare names: the startup block prints them so the
// user can see WHICH AGENTS.md is in play, and a bare name cannot tell a
// repo-root file from one in a parent directory.
func contextFiles(cwd string) []string {
	var found []string
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		path := filepath.Join(cwd, name)
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		}
	}
	return found
}
