package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/core/agent"
	"github.com/notshekhar/pi/internal/modules/core/catalog"
	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/extension"
	"github.com/notshekhar/pi/internal/modules/core/gateway"
	"github.com/notshekhar/pi/internal/modules/core/herdr"
	"github.com/notshekhar/pi/internal/modules/core/hooks"
	"github.com/notshekhar/pi/internal/modules/core/mcp"
	"github.com/notshekhar/pi/internal/modules/core/permissions"
	"github.com/notshekhar/pi/internal/modules/core/serve"
	"github.com/notshekhar/pi/internal/modules/core/session"
	"github.com/notshekhar/pi/internal/modules/core/skills"
	"github.com/notshekhar/pi/internal/modules/core/status"
	"github.com/notshekhar/pi/internal/modules/core/tools"
	"github.com/notshekhar/pi/internal/modules/tui"
)

// repl is the REPL: the app renders, the glue pumps events into turns.
type repl struct {
	app *tui.App
	cfg config.Config
	run *agent.Run

	mu      sync.Mutex
	cancel  context.CancelFunc
	turning bool
	// restorePersona holds the session's own agent prompt while a one-shot
	// `/<agent>` command borrows the slot for a single turn. nil when nothing
	// is borrowed.
	restorePersona *string
	// planViaAgent records that plan mode was armed by SELECTING the plan
	// agent rather than by /plan. Only then does leaving the agent turn it
	// back off — a gate set deliberately must survive browsing the list.
	planViaAgent bool
	// inPanel is set while a picker goroutine is running, so a panel opened
	// from inside one runs inline instead of racing it for the keyboard.
	inPanel atomic.Bool
	// lastContextTokens is the input-token count the provider reported for
	// the last turn: what the window ACTUALLY held, as against the chars/4
	// estimate everything else here uses.
	lastContextTokens int

	mcp      *mcp.Manager
	server   *serve.Server
	telegram *gateway.Telegram
	// status is what this pane is doing, for anything watching from outside.
	status *status.Bus

	// agent is the active persona's name, empty for the default one.
	agent string
	// timerLabel is what the user typed for the running countdown, so
	// /timer can report "45m left (of 1h)".
	timerLabel string
	// bg holds background tasks; daemonStop closes to stop the scheduler,
	// and its nil-ness is what "is the daemon running" means.
	bg         bgSet
	daemonStop chan struct{}
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
		WebSearch: stored.WebSearchEnabled,
		Sandbox:   tools.SandboxFromSettings(stored.Sandbox),
		NoAsk:     !stored.AskUserOn(),
		NoMemory:  !stored.MemoryOn(),
		NoTodos:   !stored.TodosOn(),
	}
}

func replTUI(parent context.Context, cfg config.Config) error {
	app, err := tui.NewApp(cfg.FullID(), cfg.CWD)
	if err != nil {
		return err
	}
	defer app.Close()
	// A stored theme wins; with none, ask the terminal what it is sitting on
	// rather than assuming dark and painting over a light background.
	if stored := config.LoadSettings().Theme; stored != "" {
		if palette, ok := tui.FindPalette(stored); ok {
			app.SetTheme(palette)
		}
	} else if app.Background() == tui.BackgroundLight {
		app.SetTheme(tui.DayPalette)
	}

	sess := session.New(cfg.FullID(), cfg.CWD)
	t := &repl{app: app, cfg: cfg, status: status.New(status.DefaultSettle), run: &agent.Run{
		Config:  cfg,
		Session: sess,
		Tools:   toolContext(cfg),
	}}
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
	t.mcp = mcp.NewManager()
	defer t.mcp.Close()
	// Language servers and anything else an extension spawned. Deferred here
	// so it runs on every exit path, including a panic.
	defer extension.Shutdown()
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
	// The `ask` and `plan` tools put their question to the user through the
	// same picker every other choice uses.
	t.run.Tools.Ask = func(ctx context.Context, question string, options []string) string {
		return app.AskChoice(question, options)
	}
	// A subagent's own transcript stays hidden; this is the only thing about
	// it the user sees while it runs.
	t.run.Progress = func(id, status string) {
		t.app.Do(func() { t.app.ToolStatus(id, status) })
		// An empty status is how the task tool says it has finished — see
		// TaskTool's deferred progress call. It is the only signal a subagent
		// has stopped, so it is where SubagentStop belongs.
		if status == "" {
			go t.fireHook(context.Background(), hooks.Context{
				Event: hooks.SubagentStop, ToolName: "task",
			})
		}
	}
	// The prompt runs on the turn goroutine and blocks there; the render loop
	// answers it.
	t.run.Ask = func(ctx context.Context, tool string, args map[string]any, reason string) bool {
		return app.Ask(tool, permissions.Subject(tool, args), reason)
	}
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
	if saved := config.LoadSettings().Agent; saved != "" && saved != planAgent {
		if a, ok := findSessionAgent(saved); ok && a.Name != defaultAgent {
			t.agent, t.run.Persona = a.Name, a.Prompt
			app.SetAgent(a.Name)
		}
	}
	app.SetThinking(thinkingName(cfg.Reasoning))
	app.SetSession(sess.ID)
	if m, ok := config.ModelInfo(cfg.Provider, cfg.ModelID); ok {
		app.SetUsage(0, 0, m.Context, 0)
	}
	app.ShowWelcome(t.welcomeInfo())
	t.startupNotices()

	t.startMCP(parent)
	go t.fireHook(parent, hooks.Context{Event: hooks.SessionStart})
	go t.pump(parent)
	return app.Run()
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
			t.fireHook(context.Background(), hooks.Context{Event: hooks.SessionEnd})
			t.app.Stop()
			return
		case tui.EvInterrupt:
			t.mu.Lock()
			if t.cancel != nil {
				t.cancel()
			}
			t.mu.Unlock()
		case tui.EvSubmit:
			t.app.Do(func() { t.app.UserEcho(ev.Text) })
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
			t.slash(parent, ev.Text)
		case tui.EvBash:
			t.bang(ev.Text)
		case tui.EvCycleAgent:
			t.cycleAgent()
		case tui.EvContinue:
			// "continue" typed for you. Resuming interrupted work is the most
			// repeated prompt there is, and it is always the same word.
			t.submit(parent, "continue")
		case tui.EvClearScreen:
			t.clearScreen()
		case tui.EvCycleModel:
			t.cycleModel()
		}
	}
}

func (t *repl) startTurn(parent context.Context, prompt string) {
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
	go func() {
		defer func() {
			t.mu.Lock()
			t.turning = false
			t.cancel = nil
			// A one-shot agent command borrowed the persona for this turn
			// only; give it back now that the turn is over.
			if t.restorePersona != nil {
				t.run.Persona = *t.restorePersona
				t.restorePersona = nil
			}
			t.mu.Unlock()
			elapsed := time.Since(started)
			// The session claims its id on its first message, so the status
			// line only learns it here — before this it correctly says
			// "unsaved".
			id := t.run.Session.ID
			t.app.Do(func() {
				t.app.SetSession(id)
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
			go t.fireHook(context.Background(), hooks.Context{Event: hooks.Stop})
		}()

		t.app.Do(func() { t.app.LoaderStart() })

		consume := func(stream <-chan ai.StreamPart) error {
			return t.consume(stream)
		}
		final, err := t.run.TurnStream(ctx, prompt, consume)
		if err != nil {
			if ctx.Err() != nil {
				t.dim("(interrupted)")
			} else {
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
			t.app.Do(func() { done <- t.part(part) })
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
		go t.fireHook(context.Background(), hooks.Context{
			Event: hooks.PostToolUse, ToolName: exec.ToolName, Success: exec.Err == nil,
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
	var in, out int64
	if v := final.Usage.InputTokens.Total; v != nil {
		in = *v
	}
	if v := final.Usage.OutputTokens.Total; v != nil {
		out = *v
	}
	var cost float64
	ctxWindow := 0
	if m, ok := config.ModelInfo(t.cfg.Provider, t.cfg.ModelID); ok {
		ctxWindow = m.Context
		cost = float64(in)*m.Cost.Input/1e6 + float64(out)*m.Cost.Output/1e6
		if v := final.Usage.InputTokens.CacheRead; v != nil && m.Cost.CacheRead > 0 {
			cost -= float64(*v) * (m.Cost.Input - m.Cost.CacheRead) / 1e6
		}
	}
	// The provider's own count of what the last request carried — the only
	// measured context figure there is, and what /context quotes.
	t.lastContextTokens = int(in)
	var cached int64
	if v := final.Usage.InputTokens.CacheRead; v != nil {
		cached = *v
	}
	t.app.Do(func() {
		t.app.SetUsage(in, out, ctxWindow, cost)
		t.app.SetCachedTokens(cached)
	})
	// Recorded on the session too, so spend survives the process and can be
	// totalled across sessions.
	if err := t.run.Session.AddUsage(in, out, cost); err != nil {
		t.dim("usage not saved: %s", err)
	}
	t.autoCompact(int(in), ctxWindow)
}

// autoCompact summarises the session once it fills too much of the window.
//
// Driven by the REAL input-token count the provider just reported, not the
// chars÷4 estimate: after a request we know exactly how big the context was.
//
// Runs on the turn goroutine while it STILL HOLDS the turn lock — the
// deferred release has not fired yet — which is what makes rewriting the
// session here safe: no prompt can start alongside it.
func (t *repl) autoCompact(used, window int) {
	threshold := config.LoadSettings().AutoCompact()
	if !agent.ShouldCompact(used, window, threshold) {
		return
	}
	t.dim("context %d%% full — compacting…", 100*used/max(window, 1))

	t.fireHook(context.Background(), hooks.Context{Event: hooks.PreCompact})
	before, after, err := agent.Compact(context.Background(), t.run)
	if err != nil {
		// Not fatal: the session is untouched and the next turn may still
		// fit. Saying so beats a silent no-op the user cannot explain.
		t.fail("auto-compact failed: %s — /compact to retry", err)
		return
	}
	t.app.Do(func() {
		th := t.app.Theme()
		t.app.Print(th.Fg(tui.SlotSuccess, fmt.Sprintf(
			"auto-compacted ~%s → ~%s tokens",
			commas(agent.EstimateTokens(before)), commas(agent.EstimateTokens(after)))))
	})
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

	t.app.Do(func() {
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
	if t.busy() {
		return
	}
	t.run.Session = session.New(t.cfg.FullID(), t.cfg.CWD)
	t.run.Tools.Registry = tools.NewRegistry()
	t.run.Tools.Todos.Clear()
	t.lastContextTokens = 0
	// Plan mode is a stance on a conversation, and this is a different one.
	t.run.Planning, t.planViaAgent = false, false
	t.app.Do(func() {
		t.app.SetSession("")
		t.app.SetPlanning(false)
		t.app.SetTodos(nil)
		t.app.ResetUsage(t.contextWindow())
		t.redrawHeader()
	})
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

// customModels is the set of model ids registered under a provider.
func customModels(provider string) map[string]bool {
	out := map[string]bool{}
	for _, id := range config.LoadSettings().CustomModels[provider] {
		out[id] = true
	}
	return out
}

// addCustomModel registers a model id the catalog does not carry.
func (t *repl) addCustomModel() {
	provider := t.cfg.Provider
	id := strings.TrimSpace(t.ask("new model id under "+provider+"/ (e.g. some-model-v2)", ""))
	if id == "" {
		return
	}
	if err := config.Update(func(s *config.Settings) {
		if s.CustomModels == nil {
			s.CustomModels = map[string][]string{}
		}
		for _, existing := range s.CustomModels[provider] {
			if existing == id {
				return
			}
		}
		s.CustomModels[provider] = append(s.CustomModels[provider], id)
	}); err != nil {
		t.fail("model: %s", err)
		return
	}
	// Said plainly rather than validated: there is no way to ask a provider
	// whether it serves a model without trying to use it, and a check that
	// cannot be made should not be implied.
	t.dim("added %s/%s (custom). It'll error at chat time if %s doesn't serve it.", provider, id, provider)
	t.cfg.ModelID = id
	t.apply()
}

// removeCustomModel forgets a registered id.
func (t *repl) removeCustomModel(id string) {
	provider := t.cfg.Provider
	if err := config.Update(func(s *config.Settings) {
		kept := make([]string, 0, len(s.CustomModels[provider]))
		for _, existing := range s.CustomModels[provider] {
			if existing != id {
				kept = append(kept, existing)
			}
		}
		if len(kept) == 0 {
			delete(s.CustomModels, provider)
			return
		}
		s.CustomModels[provider] = kept
	}); err != nil {
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
	t.app.Do(func() {
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
			s.Provider, s.Model = resolved.Provider, resolved.ModelID
		})
		t.app.Print(t.app.Theme().Fg(tui.SlotDim, "model "+resolved.FullID()))
	})
}

// bang runs a shell command inline and prints the output to the transcript.
func (t *repl) bang(cmdline string) {
	t.app.Do(func() {
		th := t.app.Theme()
		t.app.Print(th.Fg(tui.SlotAccent, "❯ ") + th.Fg(tui.SlotMuted, cmdline))
	})
	go func() {
		cmd := exec.Command("sh", "-c", cmdline)
		cmd.Dir = t.cfg.CWD
		out, err := cmd.CombinedOutput()
		text := strings.TrimRight(string(out), "\n")
		t.app.Do(func() {
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
	t.app.Do(func() { t.app.Print(t.app.Theme().Fg(tui.SlotDim, text)) })
}

func (t *repl) fail(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	t.app.Do(func() { t.app.Print(t.app.Theme().Fg(tui.SlotError, text)) })
}

// setTheme swaps the palette. Noir is the only look; night and day are its
// two themes.
func (t *repl) setTheme(name string) {
	name = strings.ToLower(strings.TrimSpace(name))
	// Aliases, because "dark" and "light" are what people type.
	switch name {
	case "dark":
		name = "night"
	case "light":
		name = "day"
	}
	if name == "" {
		t.pickTheme()
		return
	}
	palette, ok := tui.FindPalette(name)
	if !ok {
		t.dim("unknown theme %q — /theme with no argument lists them", name)
		return
	}
	t.applyTheme(palette)
}

// pickTheme offers every palette, built-in and user-defined.
func (t *repl) pickTheme() {
	palettes := tui.AllPalettes()
	items := make([]tui.Item, 0, len(palettes))
	current := config.LoadSettings().Theme
	initial := 0
	for i, p := range palettes {
		kind := "dark"
		if p.Light {
			kind = "light"
		}
		if p.Name == current {
			initial = i
		}
		items = append(items, tui.Item{Value: p.Name, Label: p.Name, Description: kind})
	}
	go func() {
		if choice := t.app.Pick("Theme", items, initial, current); choice != nil {
			if palette, ok := tui.FindPalette(choice.Value); ok {
				t.applyTheme(palette)
			}
		}
	}()
}

func (t *repl) applyTheme(palette tui.Palette) {
	t.app.Do(func() { t.app.SetTheme(palette) })
	// Remembered, so the next session opens on the same canvas. A failed
	// write is worth saying out loud but not worth refusing the change over.
	if err := config.Update(func(s *config.Settings) { s.Theme = palette.Name }); err != nil {
		t.dim("theme %s (not saved: %s)", palette.Name, err)
		return
	}
	t.dim("theme %s", palette.Name)
}

// version is what the masthead reports.
const version = "0.1.3"

// startupNotices prints the status block under the masthead: the things that
// silently change how a session behaves, stated once where they are read.
func (t *repl) startupNotices() {
	t.app.Do(t.printStartupNotices)
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
