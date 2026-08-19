package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/core/agent"
	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/extension"
	"github.com/notshekhar/pi/internal/modules/core/extension/statusline"
	"github.com/notshekhar/pi/internal/modules/core/memory"
	"github.com/notshekhar/pi/internal/modules/tui"
)

// `/settings` — read and edit the stored preferences without leaving the app.
//
// A flat `key value` surface rather than a form. There are six keys; a form
// would be more machinery than the thing it configures, and `key value` is
// the shape people already expect from a shell.

// settingKey describes one editable preference.
// boolKey builds a toggle row: a picker of on/off, rendered as its current
// state.
func boolKey(name, help string, get func(config.Settings) bool, set func(*config.Settings, bool)) settingKey {
	return settingKey{
		name: name, help: help, boolean: true, choices: []string{"on", "off"},
		get: func(s config.Settings) string {
			if get(s) {
				return "on"
			}
			return "off"
		},
		set: func(s *config.Settings, v string) error {
			switch v {
			case "on", "true", "yes":
				set(s, true)
			case "off", "false", "no":
				set(s, false)
			default:
				return fmt.Errorf("%s must be on or off", name)
			}
			return nil
		},
	}
}

type settingKey struct {
	name string
	help string
	// boolean marks a switch. A switch is TOGGLED in place by selecting its
	// row — putting an on/off menu in front of a two-state value asks the
	// user to answer a question they already answered by choosing the row.
	boolean bool
	// manager marks a row that opens a sub-panel instead of setting a value,
	// and holds the panel to open.
	manager func(*repl)
	// status renders the row's value for a manager row, which has no stored
	// setting of its own to read.
	status func(config.Settings) string
	// choices are the values a picker offers. Empty means free text.
	choices []string
	// get renders the current value.
	get func(config.Settings) string
	// set applies a new one, reporting what is wrong with it.
	set func(*config.Settings, string) error
}

var settingKeys = []settingKey{
	{
		name: "theme", choices: paletteNames(), help: "colour palette",
		get: func(s config.Settings) string { return orDefault(s.Theme, "night") },
		set: func(s *config.Settings, v string) error {
			switch v {
			case "night", "day":
				s.Theme = v
				return nil
			}
			return fmt.Errorf("theme must be night or day")
		},
	},
	{
		name: "provider", help: "default provider id",
		get: func(s config.Settings) string { return orDefault(s.Provider, "(unset)") },
		set: func(s *config.Settings, v string) error { s.Provider = v; return nil },
	},
	{
		name: "model", help: "default model id",
		get: func(s config.Settings) string { return orDefault(s.Model, "(unset)") },
		set: func(s *config.Settings, v string) error { s.Model = v; return nil },
	},
	{
		name: "thinking", choices: thinkingLevelNames, help: "how much the model reasons before answering",
		get: func(s config.Settings) string {
			if s.Reasoning == "" {
				return "(provider default)"
			}
			return thinkingName(provider.ReasoningEffort(s.Reasoning))
		},
		// Named "off" here and "none" on the wire, the same split /thinking
		// makes: the user turns thinking off, the provider is told the effort
		// is none.
		set: func(s *config.Settings, v string) error {
			if v == "" {
				s.Reasoning = ""
				return nil
			}
			if v == "none" {
				v = "off"
			}
			if !contains(thinkingLevelNames, v) {
				return fmt.Errorf("thinking must be one of: %s", strings.Join(thinkingLevelNames, ", "))
			}
			s.Reasoning = string(thinkingWire(v))
			return nil
		},
	},
	{
		name: "maxSteps", help: "agent step cap per turn",
		get: func(s config.Settings) string {
			if s.MaxSteps <= 0 {
				return fmt.Sprint(config.DefaultMaxSteps)
			}
			return fmt.Sprint(s.MaxSteps)
		},
		set: func(s *config.Settings, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return fmt.Errorf("maxSteps must be a positive number")
			}
			s.MaxSteps = n
			return nil
		},
	},
	{
		name: "autoCompact", choices: []string{"off", "50%", "70%", "80%", "90%"}, help: "window fraction to compact at; 0 disables",
		get: func(s config.Settings) string {
			if s.AutoCompact() == 0 {
				return "off"
			}
			return fmt.Sprintf("%.0f%%", s.AutoCompact()*100)
		},
		set: func(s *config.Settings, v string) error {
			if v == "off" || v == "0" {
				// Stored as negative, because zero means "unset, use the
				// default" — an explicit off has to be distinguishable.
				s.AutoCompactThreshold = -1
				return nil
			}
			f, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64)
			if err != nil {
				return fmt.Errorf("autoCompact must be a fraction, a percentage, or off")
			}
			if f > 1 {
				f /= 100
			}
			if f <= 0 || f > 1 {
				return fmt.Errorf("autoCompact must be between 0 and 100%%")
			}
			s.AutoCompactThreshold = f
			return nil
		},
	},
	// Feature switches. Each is a toggle whose picker offers on/off, and
	// each one actually GATES the feature — a setting that only records a
	// preference is worse than no setting, because it reads as though it did
	// something.
	boolKey("subagents", "let the agent delegate work via the task tool",
		func(s config.Settings) bool { return s.SubagentsOn() },
		func(s *config.Settings, v bool) { s.Subagents = &v }),
	boolKey("memory", "agent saves per-repo facts across sessions",
		func(s config.Settings) bool { return s.MemoryOn() },
		func(s *config.Settings, v bool) { s.MemoryEnabled = &v }),
	boolKey("recap", "short recap under responses that changed files",
		func(s config.Settings) bool { return s.RecapOn() },
		func(s *config.Settings, v bool) { s.RecapEnabled = &v }),
	boolKey("askUser", "let the agent pause mid-turn to ask you questions",
		func(s config.Settings) bool { return s.AskUserOn() },
		func(s *config.Settings, v bool) { s.AskUser = &v }),
	boolKey("todos", "pinned checklist the agent maintains during long tasks",
		func(s config.Settings) bool { return s.TodosOn() },
		func(s *config.Settings, v bool) { s.TodosEnabled = &v }),
	boolKey("webSearch", "give the agent a websearch tool (scrapes DuckDuckGo)",
		func(s config.Settings) bool { return s.WebSearchEnabled },
		func(s *config.Settings, v bool) { s.WebSearchEnabled = v }),
	boolKey("mcp", "connect configured MCP servers and expose their tools",
		func(s config.Settings) bool { return s.MCPOn() },
		func(s *config.Settings, v bool) { s.MCPEnabled = &v }),
	boolKey("workspaceContext", "inject AGENTS.md / CLAUDE.md into the system prompt",
		func(s config.Settings) bool { return s.WorkspaceContextOn() },
		func(s *config.Settings, v bool) { s.WorkspaceContext = &v }),
	boolKey("pinnedInput", "hold the prompt on the last rows; scrollback, wheel and selection stay the terminal's",
		func(s config.Settings) bool { return s.PinnedInputOn() },
		func(s *config.Settings, v bool) { s.PinnedInput = &v }),
	boolKey("uiLive", "keep runs of finished tool calls folded while you type (ctrl+e does it anyway)",
		func(s config.Settings) bool { return s.UILiveOn() },
		func(s *config.Settings, v bool) { s.UILive = &v }),
	boolKey("herdr", "report working/blocked/idle to a herdr pane; inert outside one",
		func(s config.Settings) bool { return s.HerdrOn() },
		func(s *config.Settings, v bool) { s.HerdrEnabled = &v }),
	boolKey("clock", "live clock in the status line",
		func(s config.Settings) bool { return s.ClockOn() },
		func(s *config.Settings, v bool) { s.ClockEnabled = &v }),
	boolKey("reminders", "fire /reminder alerts; off mutes without deleting",
		func(s config.Settings) bool { return s.RemindersOn() },
		func(s *config.Settings, v bool) { s.RemindersEnabled = &v }),
	boolKey("serve", "allow /serve — anyone with the url and token drives this machine",
		func(s config.Settings) bool { return s.ServeOn() },
		func(s *config.Settings, v bool) { s.ServeEnabled = &v }),
	boolKey("bashApprove", "ask before every bash command",
		func(s config.Settings) bool { return s.BashApproveOn() },
		func(s *config.Settings, v bool) { s.BashApprove = &v }),
	{
		name: "subagentMaxParallel", help: "concurrent subagent streams",
		get: func(s config.Settings) string { return fmt.Sprint(s.SubagentParallel()) },
		set: func(s *config.Settings, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return fmt.Errorf("subagentMaxParallel must be a whole number")
			}
			s.SubagentMaxParallel = n
			return nil
		},
	},
	{
		name: "sandbox", choices: []string{"off", "workspace"}, help: "bound what shell commands may write",
		get: func(s config.Settings) string { return orDefault(s.Sandbox, "off") },
		set: func(s *config.Settings, v string) error {
			switch v {
			case "off", "workspace", "":
				s.Sandbox = v
				return nil
			}
			return fmt.Errorf("sandbox must be off or workspace")
		},
	},
	{
		name: "subagentModel", help: "model for work delegated with the task tool — blank inherits the session's",
		get: func(s config.Settings) string { return orDefault(s.SubagentModel, "inherit") },
		set: func(s *config.Settings, v string) error {
			if v == "inherit" {
				v = ""
			}
			s.SubagentModel = v
			return nil
		},
	},
	{
		name: "compactModel", help: "model that summarises a session when it compacts — blank inherits",
		get: func(s config.Settings) string { return orDefault(s.CompactModel, "inherit") },
		set: func(s *config.Settings, v string) error {
			if v == "inherit" {
				v = ""
			}
			s.CompactModel = v
			return nil
		},
	},
	// Rows that open another panel rather than setting a value.
	//
	// They live here, at the bottom of the same list, because a user looking
	// for "what can I configure" does not know in advance which answers are
	// one value and which are a list of them — and a guardrail nobody can
	// find is a guardrail nobody uses.
	{
		name: "bash denylist", help: "add/remove bash commands the agent is refused (guardrail)",
		manager: (*repl).pickBashDeny,
		status: func(config.Settings) string {
			return fmt.Sprintf("%d blocked", len(bashDenyPatterns()))
		},
	},
	{
		name: "permission rules", help: "allow/ask/deny rules over the tools (bash(git *), read(secrets/**), …)",
		manager: (*repl).pickPermission,
		status:  func(s config.Settings) string { return fmt.Sprint(len(s.Permissions)) },
	},
	{
		name: "hooks", help: "shell commands run at session, prompt, tool, and turn boundaries",
		manager: (*repl).pickHook,
		status: func(s config.Settings) string {
			n := 0
			for _, cmds := range s.Hooks {
				n += len(cmds)
			}
			return fmt.Sprintf("%d configured", n)
		},
	},
	{
		name: "scoped models", help: "the models ctrl+p cycles through",
		manager: (*repl).pickScopedModels,
		status: func(s config.Settings) string {
			if len(s.ScopedModels) == 0 {
				return "none — ctrl+p disabled"
			}
			return fmt.Sprintf("%d", len(s.ScopedModels))
		},
	},
}

// paletteNames is every theme the picker may offer, user themes included.
func paletteNames() []string {
	palettes := tui.AllPalettes()
	names := make([]string, 0, len(palettes))
	for _, p := range palettes {
		names = append(names, p.Name)
	}
	return names
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// settings shows the stored preferences, or sets one.
func (t *repl) settings(rest string) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		t.settingsMenu()
		return
	}
	if rest == "list" {
		t.showSettings()
		return
	}

	name, value, hasValue := strings.Cut(rest, " ")
	value = strings.TrimSpace(value)

	var key *settingKey
	for i := range settingKeys {
		if strings.EqualFold(settingKeys[i].name, name) {
			key = &settingKeys[i]
			break
		}
	}
	if key == nil {
		t.dim("unknown setting %q — /settings to list them", name)
		return
	}
	if key.set == nil {
		// A panel row has no value to set from the command line.
		t.dim("%s — open it with /settings and select the row", key.name)
		return
	}
	if !hasValue || value == "" {
		t.dim("%s is %s — %s", key.name, key.value(config.LoadSettings()), key.help)
		return
	}

	// Validate against a copy FIRST. Update writes unconditionally, so a
	// rejected value reaching it would persist a half-applied change and then
	// report a failure — leaving the file and the message disagreeing.
	probe := config.LoadSettings()
	if err := key.set(&probe, value); err != nil {
		t.fail("%s", err)
		return
	}
	if err := config.Update(func(s *config.Settings) { _ = key.set(s, value) }); err != nil {
		t.fail("settings: %s", err)
		return
	}
	t.dim("%s = %s", key.name, key.value(config.LoadSettings()))
	t.applyKey(key.name)
}

// applyKey makes one changed preference take effect now.
//
// It re-applies EVERYTHING rather than only the key that changed: the switches
// interact (the tool set depends on four of them at once), and a per-key
// switch statement is where a new setting silently gets forgotten.
func (t *repl) applyKey(name string) {
	if n := config.LoadSettings().MaxSteps; n > 0 {
		t.cfg.MaxSteps = n
		t.run.Config.MaxSteps = n
	}
	t.applySettings()
	if name == "provider" || name == "model" {
		t.dim("takes effect on /model or the next session")
	}
}

func (t *repl) showSettings() {
	stored := config.LoadSettings()
	t.app.Do(func() {
		th := t.app.Theme()
		lines := make([]string, 0, len(settingKeys)+3)
		for _, key := range settingKeys {
			lines = append(lines,
				th.Fg(tui.SlotAccent, padRight(key.name, 18))+
					th.Fg(tui.SlotText, padRight(key.value(stored), 18))+
					th.Fg(tui.SlotDim, key.help))
		}
		path, err := config.SettingsPath()
		if err == nil {
			lines = append(lines, "", th.Fg(tui.SlotDim, path))
		}
		lines = append(lines, th.Fg(tui.SlotDim, "/settings <key> <value> to change one"))
		t.app.Print(lines...)
	})
}

// showAgents lists the subagents `task` can delegate to.
func (t *repl) showAgents() {
	t.app.Do(func() {
		th := t.app.Theme()
		lines := make([]string, 0, len(agent.TaskAgents)+2)
		for _, a := range agent.TaskAgents {
			access := "read-only"
			if !a.ReadOnly {
				access = "can edit"
			}
			lines = append(lines,
				th.Fg(tui.SlotAccent, padRight(a.Name, 12))+
					th.Fg(tui.SlotText, padRight(a.About, 44))+
					th.Fg(tui.SlotDim, access))
		}
		lines = append(lines, "",
			th.Fg(tui.SlotDim, "the model delegates with the task tool; a subagent sees none of this conversation"))
		t.app.Print(lines...)
	})
}

// memoryCmd inspects what the agent has remembered about this repository.
// pickMemoryFile is `/memory`: choose an instruction file and edit it.
func (t *repl) pickMemoryFile() {
	root := agent.RepoRoot(t.cfg.CWD)
	type spot struct{ label, dir string }
	spots := []spot{
		{"Project memory", root},
		{"Project memory (local)", filepath.Join(root, config.DirName)},
	}
	if home, err := config.Dir(); err == nil {
		spots = append([]spot{{"User memory (global)", home}}, spots...)
	}
	if t.cfg.CWD != root {
		spots = append(spots, spot{"Directory memory", t.cfg.CWD})
	}

	var items []tui.Item
	seen := map[string]bool{}
	for _, sp := range spots {
		// AGENTS.md unless only a legacy CLAUDE.md is there — the loader
		// honours both, so the picker must offer whichever actually exists
		// rather than always proposing to create a second file.
		path := filepath.Join(sp.dir, "AGENTS.md")
		if !exists(path) && exists(filepath.Join(sp.dir, "CLAUDE.md")) {
			path = filepath.Join(sp.dir, "CLAUDE.md")
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		note := ""
		if !exists(path) {
			note = "  (create)"
		}
		items = append(items, tui.Item{
			Value:       path,
			Label:       sp.label,
			Description: tui.ShortenPath(path) + note,
		})
	}

	t.pickPlain("memory — pick a file to edit", items, 0, "", func(choice tui.Item) {
		t.editFile(choice.Value)
	})
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// editFile hands the terminal to $EDITOR and takes it back afterwards.
//
// The TUI is stopped first and restarted after. Without that the render loop
// keeps painting over the editor's screen, and the two fight for the cursor
// until one of them is killed.
func (t *repl) editFile(path string) {
	editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.fail("memory: %s", err)
		return
	}
	shell := firstNonEmpty(os.Getenv("SHELL"), "/bin/sh")
	quoted := "'" + strings.ReplaceAll(path, "'", `'''`) + "'"

	t.app.Suspend(func() {
		cmd := exec.Command(shell, "-c", editor+" "+quoted)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		_ = cmd.Run()
	})
	t.dim("edited %s", tui.ShortenPath(path))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (t *repl) memoryCmd(rest string) {
	store, err := memory.Open(t.cfg.CWD)
	if err != nil {
		t.fail("memory: %s", err)
		return
	}

	action, arg, _ := strings.Cut(strings.TrimSpace(rest), " ")
	arg = strings.TrimSpace(arg)

	switch action {
	case "":
		// loop's `/memory` picks an instruction file and opens it in the
		// editor. That is what the command's own description promises, and
		// what the stored-facts listing — reachable as `/memory list` — is
		// not: the facts are written by the agent, and the file is the one
		// thing here a person edits by hand.
		t.pickMemoryFile()
	case "list":
		t.listMemory(store)
	case "forget", "delete":
		if arg == "" {
			t.dim("usage: /memory forget <name>")
			return
		}
		if err := store.Delete(arg); err != nil {
			t.fail("%s", err)
			return
		}
		t.dim("forgot %s", arg)
	default:
		// Anything else is a name to read, so `/memory build-command` works
		// without a verb.
		fact, err := store.Get(action)
		if err != nil {
			t.fail("%s", err)
			return
		}
		t.app.Do(func() {
			th := t.app.Theme()
			t.app.Print(th.Fg(tui.SlotAccent, th.Bold(fact.Name)) + "  " +
				th.Fg(tui.SlotDim, fact.Description))
			t.app.AssistantDelta(fact.Body)
			t.app.AssistantEnd()
		})
	}
}

func (t *repl) listMemory(store *memory.Store) {
	facts, err := store.List()
	if err != nil {
		t.fail("memory: %s", err)
		return
	}
	t.app.Do(func() {
		th := t.app.Theme()
		if len(facts) == 0 {
			t.app.Print(
				th.Fg(tui.SlotDim, "nothing remembered about this repository yet"),
				th.Fg(tui.SlotDim, "the agent saves facts with the memory tool as it learns them"))
			return
		}
		lines := make([]string, 0, len(facts)+2)
		for _, f := range facts {
			lines = append(lines,
				th.Fg(tui.SlotAccent, padRight(f.Name, 24))+th.Fg(tui.SlotMuted, f.Description))
		}
		lines = append(lines, "",
			th.Fg(tui.SlotDim, store.Dir),
			th.Fg(tui.SlotDim, "/memory <name> to read one · /memory forget <name>"))
		t.app.Print(lines...)
	})
}

// settingsMenu is `/settings` with no arguments: the settings panel.
//
// A menu rather than a text dump, because the answer to "what can I set this
// to?" should be on screen rather than in the help text — and because
// choosing from a list cannot be misspelled.
//
// The row shows `key: value` and the DESCRIPTION carries the help. The other
// way round — key as the label, value as the description — costs the help
// text entirely, and the help is the half a user cannot reconstruct: a value
// of `on` next to `askUser` says nothing about what it turns on.
func (t *repl) settingsMenu() {
	t.manage(func() (string, []tui.Item) {
		stored := config.LoadSettings()
		items := make([]tui.Item, 0, len(settingKeys))
		for _, key := range settingKeys {
			items = append(items, tui.Item{
				Value:       key.name,
				Label:       key.name + ": " + key.value(stored),
				Description: key.help,
			})
		}
		return "Settings (type to filter, Esc to close)", items
	}, func(choice tui.Item) {
		t.editSetting(choice.Value)
	})
}

// value renders the row's current state, whatever kind of row it is.
func (k settingKey) value(s config.Settings) string {
	if k.status != nil {
		return k.status(s)
	}
	return k.get(s)
}

// editSetting acts on one row: open its panel, flip its switch, offer its
// choices, or ask for a new value.
func (t *repl) editSetting(name string) {
	var key *settingKey
	for i := range settingKeys {
		if settingKeys[i].name == name {
			key = &settingKeys[i]
			break
		}
	}
	if key == nil {
		return
	}

	switch {
	case key.manager != nil:
		// Runs inline — see manage: a panel opened from a panel shares the
		// goroutine that is already waiting for it, so control comes back
		// here when the sub-panel is closed and the settings list reopens.
		key.manager(t)
		return

	case key.boolean:
		// Selecting the row IS the toggle. See settingKey.boolean.
		next := "on"
		if key.get(config.LoadSettings()) == "on" {
			next = "off"
		}
		t.settings(key.name + " " + next)
		return

	case len(key.choices) > 0:
		items := make([]tui.Item, 0, len(key.choices))
		for _, v := range key.choices {
			items = append(items, tui.Item{Value: v, Label: v})
		}
		current := key.get(config.LoadSettings())
		if choice := t.app.Select(key.name, items, indexOf(items, current), current); choice != nil {
			t.settings(key.name + " " + choice.Value)
		}
		return
	}

	// Free text: ask for it, pre-filled with what is there now so an edit is
	// an edit rather than a retype.
	current := key.get(config.LoadSettings())
	if v := strings.TrimSpace(t.ask(key.name+" — "+key.help, current)); v != "" && v != current {
		t.settings(key.name + " " + v)
	}
}

// applySettings re-reads the stored settings and makes them true of the
// running session.
//
// One function, called by /reload AND after every settings edit, because a
// toggle that only takes effect on the next launch is indistinguishable from
// one that does not work. Everything here is idempotent: it is re-applying
// state, not toggling it.
func (t *repl) applySettings() {
	stored := config.LoadSettings()

	t.run.Permissions = t.loadPolicy()
	t.run.Subagents = stored.SubagentsOn()

	// The tool set is REBUILT rather than patched: the switches decide which
	// tools exist at all, and there is no way to add one back by assignment.
	// The registry is carried across so read-before-edit does not forget
	// which files this session has already seen.
	registry := t.run.Tools.Registry
	todos := t.run.Tools.Todos
	ask := t.run.Tools.Ask
	t.run.Tools = toolContext(t.cfg)
	t.run.Tools.Registry, t.run.Tools.Todos, t.run.Tools.Ask = registry, todos, ask

	// What the enabled extensions contribute. Recomputed here rather than at
	// boot so toggling one in /extensions takes effect on the next turn —
	// a switch that needs a restart reads as a switch that does not work.
	active := activeExtensions()
	t.run.ExtensionTools = extension.ToolsFrom(active, t.run.Tools)
	// Extensions may also DECORATE the built-in tools — the LSP extension
	// appends type errors to a write's result, which is what lets the agent
	// find out it broke the build without being told to check.
	t.run.WrapTools = func(set []ai.Tool) []ai.Tool {
		return extension.WrapTools(activeExtensions(), set)
	}
	t.run.ExtensionPrompt = strings.TrimSpace(extension.PromptFrom(active, t.cfg.CWD))

	// Status-line transforms, adapted across the module boundary: the
	// extension layer describes a status line without importing the terminal,
	// and this is the single place the two shapes are translated.
	transforms := extension.StatusTransformsFrom(active)
	chain := make([]tui.StatusTransform, 0, len(transforms))
	for _, transform := range transforms {
		chain = append(chain, func(lines []string, ctx tui.StatusContext) []string {
			return transform(lines, statusSnapshot(ctx))
		})
	}

	t.app.Do(func() {
		t.app.SetStatusTransform(chain...)
		if palette, ok := tui.FindPalette(orDefault(stored.Theme, "night")); ok {
			t.app.SetTheme(palette)
		}
		t.app.SetClock(stored.ClockOn())
		// Applied live: pinning moves the prompt, and a toggle that waited
		// for a relaunch would read as one that does nothing.
		t.app.SetPinnedInput(stored.PinnedInputOn())
		t.app.SetLiveVariant(stored.UILiveOn())
	})
}

// statusSnapshot converts the terminal's status context into the shape the
// extension layer speaks.
func statusSnapshot(ctx tui.StatusContext) statusline.Snapshot {
	return statusline.Snapshot{
		Agent: ctx.Agent, ModelID: ctx.ModelID, Model: ctx.Model,
		Session: ctx.Session, Thinking: ctx.Thinking, Reasoning: ctx.Reasoning,
		Cost:        ctx.Cost,
		InputTokens: ctx.InputTokens, OutputTokens: ctx.OutputTokens,
		CachedTokens: ctx.CachedTokens,
		ContextUsed:  ctx.ContextUsed, ContextMax: ctx.ContextMax,
		Width: ctx.Width,
	}
}
