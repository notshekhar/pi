package main

import (
	"context"
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/modules/core/extension"
	"github.com/notshekhar/pi/internal/modules/tui"
)

// The slash-command registry.
//
// One table, three consumers: dispatch, `/help`, and the `/` completion
// palette. They used to be three hand-maintained lists, and they had drifted
// — the palette was missing a third of the commands the switch accepted,
// `/help` described `/clear` as keeping the session while loop's starts a
// new one, and several aliases existed in dispatch but nowhere a user could
// discover them. A command that is not in all three places is a command
// nobody finds.
//
// Shaped after loop's registry, including the decision to give every ALIAS
// its own entry rather than hiding it behind its target. `/effort` is a real
// command in the palette that says it is an alias for `/thinking`, because
// the alternative is a user typing the name loop taught them and being told
// it does not exist.

// command is one slash command.
type command struct {
	name        string
	description string
	// run does the work. rest is the argument text, already trimmed; ctx is
	// the turn's parent context, for the commands that start work.
	run func(t *repl, ctx context.Context, rest string)
}

// arg adapts a handler that only wants the argument text.
func arg(f func(*repl, string)) func(*repl, context.Context, string) {
	return func(t *repl, _ context.Context, rest string) { f(t, rest) }
}

// bare adapts a handler that takes nothing.
func bare(f func(*repl)) func(*repl, context.Context, string) {
	return func(t *repl, _ context.Context, _ string) { f(t) }
}

// commands is the whole table, in the order `/help` prints it.
func commands() []command {
	return []command{
		{"help", "Show available commands", bare((*repl).help)},
		{"login", "Configure provider authentication", arg((*repl).login)},
		{"logout", "Remove provider authentication (opens picker)", arg((*repl).logout)},
		{"model", "Select model (opens picker, or /model provider/id)", arg((*repl).pickModel)},
		{"models", "Alias for /model", arg((*repl).pickModel)},
		{"provider", "Switch active provider (opens picker, or /provider <id>)", arg((*repl).providerCmd)},
		{"providers", "Alias for /provider", arg((*repl).providerCmd)},
		{"new", "Start a new session", bare((*repl).newSession)},
		{"clear", "Start a new session (clears screen)", bare((*repl).clear)},
		{"compact", "Manually compact the session context", func(t *repl, ctx context.Context, _ string) { t.compact(ctx) }},
		{"plan", "Toggle plan mode — read-only until a plan is approved (/plan <task> to start planning)", arg((*repl).setPlanMode)},
		{"thinking", "Set reasoning/thinking level (off|minimal|low|medium|high|xhigh)", arg((*repl).setThinking)},
		{"effort", "Alias for /thinking — set reasoning effort (off|minimal|low|medium|high|xhigh)", arg((*repl).setThinking)},
		{"resume", "Resume a different session", arg((*repl).pickSession)},
		{"sessions", "Alias for /resume", arg((*repl).pickSession)},
		{"session", "Show session info and stats", bare((*repl).sessionInfo)},
		{"name", "Set session display name (no arg = rename prompt)", arg((*repl).renameSession)},
		{"rename", "Alias for /name", arg((*repl).renameSession)},
		{"timer", "Countdown timer: /timer 1h30m · /timer off · /timer shows remaining", arg((*repl).timer)},
		{"reminder", "Manage reminders (one-time or interval-scheduled)", arg((*repl).reminders)},
		{"reminders", "Alias for /reminder", arg((*repl).reminders)},
		{"background", "Background tasks: /background opens the manager · /background <text> runs one", func(t *repl, ctx context.Context, rest string) { t.background(ctx, rest) }},
		{"bg", "Alias for /background", func(t *repl, ctx context.Context, rest string) { t.background(ctx, rest) }},
		{"goal", "Goal mode: /goal <objective> works autonomously until verified done · pause|resume|status|clear", func(t *repl, ctx context.Context, rest string) { t.goal(ctx, rest) }},
		{"daemon", "Background scheduler for reminders and scheduled tasks: /daemon on|off|status", arg((*repl).daemon)},
		{"cost", "Show cost breakdown (session, directory, today, 7d, month, lifetime)", arg((*repl).costCmd)},
		{"context", "Show context window usage breakdown", bare((*repl).showContext)},
		{"steak", "Token-usage heatmap, GitHub-contributions style: /steak · /steak <year>", arg((*repl).steak)},
		{"attach", "Attach an image (paste from clipboard, or /attach <path>)", arg((*repl).attach)},
		{"paste", "Paste the clipboard — an image is attached, text goes into the draft", arg((*repl).paste)},
		{"cd", "Move this session to a new working directory: /cd <path>", arg((*repl).changeDir)},
		{"cwd", "Alias for /cd", arg((*repl).changeDir)},
		{"copy", "Copy last assistant message to clipboard", bare((*repl).copyLast)},
		{"export", "Export session (path optional, .md/.jsonl/.html)", arg((*repl).export)},
		{"import", "Import a session from a JSONL file: /import <path>", arg((*repl).importSession)},
		{"settings", "Open settings menu", arg((*repl).settings)},
		{"config", "Alias for /settings", arg((*repl).settings)},
		{"ui", "Show or switch the UI mode (experience): /ui · /ui <mode>", arg((*repl).setUIMode)},
		{"theme", "Switch the colour theme: /theme · /theme night|day", arg((*repl).setTheme)},
		{"hotkeys", "Show all keyboard shortcuts", bare((*repl).hotkeys)},
		{"reload", "Reload prompts, keybindings, settings", bare((*repl).reload)},
		{"changelog", "Show changelog entries", bare((*repl).changelog)},
		{"release-notes", "Alias for /changelog", bare((*repl).changelog)},
		{"recap", "Generate a short recap of the last turn now", bare((*repl).recap)},
		{"doctor", "Diagnose the environment and configuration", bare((*repl).doctor)},
		{"memory", "Open an AGENTS.md memory file in your editor", arg((*repl).memoryCmd)},
		{"init", "Analyze the codebase and create or improve AGENTS.md", func(t *repl, ctx context.Context, _ string) { t.initProject(ctx) }},
		{"agents", "Select the agent this session speaks as (custom system prompts)", arg((*repl).agentsCmd)},
		{"hooks", "List, add, and remove lifecycle hooks", bare((*repl).hooksMenu)},
		{"bashdeny", "Add or remove bash commands the agent is refused (denylist guardrail)", arg((*repl).bashDeny)},
		{"permissions", "Manage allow/ask/deny permission rules (Bash(git *), Read(secrets/**), …)", arg((*repl).permissions)},
		{"perms", "Alias for /permissions", arg((*repl).permissions)},
		{"mcp", "List MCP servers and their tools (/mcp reconnect [name])", arg((*repl).mcpCmd)},
		{"skills", "Packaged instruction sets the agent can load", arg((*repl).skillsMenu)},
		{"skill", "Alias for /skills", arg((*repl).skillsMenu)},
		{"fork", "Create a new fork from a previous user message", bare((*repl).forkSession)},
		{"clone", "Duplicate the current session at current position", bare((*repl).cloneSession)},
		{"tree", "Navigate the session tree (switch branches)", bare((*repl).sessionTree)},
		{"share", "Share session as a secret GitHub gist (needs gh CLI)", bare((*repl).share)},
		{"scoped-models", "Enable/disable models for ctrl+p cycling (panel, or add/rm <id>)", arg((*repl).scopedModels)},
		{"gateways", "Set up remote chat gateways — each runs as its own daemon", func(t *repl, ctx context.Context, rest string) { t.gateways(ctx, rest) }},
		{"serve", "Expose this session over loopback HTTP", func(t *repl, ctx context.Context, rest string) { t.serve(ctx, rest) }},
		{"extensions", "Manage extensions: enable, disable, info", arg((*repl).extensionsCmd)},
		{"install", "Install an extension: /install <path to a Go main package>", arg((*repl).installExtension)},
		{"alias", "Command aliases: /alias · /alias <name> </cmd args…> · /alias rm <name>", arg((*repl).alias)},
		{"quit", "Quit pi-agent", bare((*repl).quit)},
		{"exit", "Alias for /quit", bare((*repl).quit)},
	}
}

// The name→command lookup, built on first use.
//
// Built inside a function rather than as a var initializer: the dynamic
// commands ask whether a name is already taken, which makes the table
// reachable from its own initializer — and a var whose initializer can reach
// itself is an initialization cycle, whether or not it would ever recurse at
// run time.
var (
	indexOnce  sync.Once
	commandMap map[string]*command
)

// lookupCommand finds a command by name, with or without the leading slash.
func lookupCommand(name string) *command {
	indexOnce.Do(func() {
		table := commands()
		commandMap = make(map[string]*command, len(table))
		for i := range table {
			commandMap[table[i].name] = &table[i]
		}
	})
	return commandMap[strings.TrimPrefix(name, "/")]
}

// slashItems is the completion catalog for `/`, derived from the table so a
// new command is discoverable the moment it is registered.
func slashItems() []tui.Item {
	table := commands()
	items := make([]tui.Item, 0, len(table))
	for _, c := range table {
		items = append(items, tui.Item{Value: "/" + c.name, Label: c.name, Description: c.description})
	}
	// Extension commands are completable too, or an extension's command is
	// one you have to already know about to use.
	for _, c := range extension.CommandsFrom(activeExtensions()) {
		items = append(items, tui.Item{Value: "/" + c.Name, Label: c.Name, Description: c.About})
	}
	return items
}
