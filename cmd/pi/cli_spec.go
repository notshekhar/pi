package main

import (
	"slices"
	"strings"
)

// The CLI's command and flag surface, as data — loop's spec.ts.
//
// One table drives the parser's flag arity, which commands dispatch, shell
// completion and the man page, so the four cannot describe different CLIs.
// Before loop had it, an unknown command fell through to the TUI and a typo'd
// flag was silently kept — `sesions` opened a chat and `--modle` billed a
// turn on the default model.

// flagSpec is one flag. Arity is global (one name means one thing
// everywhere), which lets the parser decide whether `--model x` consumes `x`
// before it knows which command runs.
type flagSpec struct {
	name        string
	short       string
	takesValue  bool
	description string
	// choices, when set, are the only values accepted.
	choices []string
}

// commandSpec is one top-level command.
type commandSpec struct {
	name        string
	description string
	aliases     []string
	// subcommands are fixed words that may follow (completion and man only).
	subcommands []string
	// flags this command accepts beyond the globals.
	flags []string
	// passthrough commands parse their own argv (mcp, and the commands whose
	// flag sets predate this table), so the shared parser leaves it alone.
	passthrough bool
}

// outputFormats is how `pi run` reports: text for a person, json (one
// object at the end) and stream-json (an event per line) for scripts.
var outputFormats = []string{"text", "json", "stream-json"}

// flagSpecs is every flag the CLI reads, in the order the man page lists them.
var flagSpecs = []flagSpec{
	{name: "help", short: "h", description: "Show help"},
	{name: "version", short: "v", description: "Print version"},
	{name: "model", takesValue: true, description: "Override the model (provider/id)"},
	{name: "provider", takesValue: true, description: "Override the active provider"},
	{name: "cwd", takesValue: true, description: "Working directory"},
	{name: "session", takesValue: true, description: "Resume a session by id"},
	{name: "reasoning", takesValue: true, description: "Reasoning effort: none, low, medium, high, xhigh"},
	{name: "max-steps", takesValue: true, description: "Cap agent steps in run mode"},
	{name: "output-format", takesValue: true, choices: outputFormats,
		description: "How run mode reports: text, json (one object at the end), stream-json (NDJSON events)"},
	{name: "all", description: "Include archived sessions"},
	{name: "archived", description: "Only archived sessions"},
	{name: "force", description: "Reinstall even if already up to date"},
	{name: "install", description: "Write the man page to the manpath"},
	{name: "path", description: "Print the man page source instead of opening it"},
	{name: "terminal", description: "serve: offer the terminal to other devices even if /settings turned it off"},
	{name: "no-terminal", description: "serve: keep the terminal to this machine (paired devices get chat only)"},
	{name: "host", takesValue: true, description: "Interface to bind (default 0.0.0.0)"},
	{name: "port", takesValue: true, description: "Port to bind"},
	{name: "every", takesValue: true, description: "Run on an interval (30m, 2h, 1d)"},
	{name: "cron", takesValue: true, description: "Run on a cron expression"},
	{name: "at", takesValue: true, description: `Run once at a time ("18:30", "45m")`},
	{name: "agent", takesValue: true, description: "Named agent to run as"},
}

// globalFlags are accepted with or without a command.
var globalFlags = []string{"help", "version"}

// interactiveFlags are what bare `pi` (the TUI) accepts, on top of the globals.
var interactiveFlags = []string{"model", "provider", "cwd", "session", "reasoning"}

// commandSpecs is every command pi dispatches, in the order help lists them.
var commandSpecs = []commandSpec{
	{name: "run", description: "Run a single prompt and exit",
		flags: []string{"model", "cwd", "session", "reasoning", "max-steps", "output-format"}},
	{name: "login", description: "Configure provider auth"},
	{name: "logout", description: "Remove provider auth"},
	{name: "sessions", description: "List sessions in the current directory", flags: []string{"all", "archived"}, passthrough: true},
	{name: "archive", description: "Archive a session (hide it from the lists)", passthrough: true},
	{name: "unarchive", description: "Restore an archived session", passthrough: true},
	{name: "artifacts", description: "List pages the agent wrote", subcommands: []string{"export"}},
	{name: "models", description: "List available models"},
	{name: "whoami", description: "Show active provider and auth status"},
	{name: "cost", description: "Cost ledger tools", subcommands: []string{"audit"}},
	{name: "goals", description: "Manage background tasks", aliases: []string{"background"},
		subcommands: []string{"list", "add", "rm", "run", "tick"},
		flags:       []string{"every", "cron", "at", "cwd", "model", "agent"}},
	{name: "mcp", description: "Manage MCP servers",
		subcommands: []string{"add", "add-json", "list", "get", "remove", "enable", "disable", "login"}, passthrough: true},
	{name: "serve", description: "Web UI + WebSocket RPC", flags: []string{"host", "port", "terminal", "no-terminal"}, passthrough: true},
	{name: "rpc", description: "JSON-RPC server", passthrough: true},
	{name: "install", description: "Install an extension"},
	{name: "link", description: "Link a local extension"},
	{name: "remove", description: "Remove an extension", aliases: []string{"uninstall"}},
	{name: "extensions", description: "List installed extensions"},
	{name: "enable", description: "Enable an extension"},
	{name: "disable", description: "Disable an extension"},
	{name: "completion", description: "Print a shell completion script", subcommands: completionShells},
	{name: "man", description: "Open the manual", flags: []string{"install", "path"}},
	{name: "upgrade", description: "Update to the latest release", aliases: []string{"update"}, flags: []string{"force"}, passthrough: true},
	{name: "version", description: "Print version"},
	{name: "help", description: "Show help"},
}

// findCommand resolves a word to its command, following aliases.
func findCommand(word string) (commandSpec, bool) {
	for _, c := range commandSpecs {
		if c.name == word || slices.Contains(c.aliases, word) {
			return c, true
		}
	}
	return commandSpec{}, false
}

// findFlag is a flag by its long name.
func findFlag(name string) (flagSpec, bool) {
	for _, f := range flagSpecs {
		if f.name == name {
			return f, true
		}
	}
	return flagSpec{}, false
}

// flagForShort is the flag a single letter stands for.
func flagForShort(letter string) (flagSpec, bool) {
	for _, f := range flagSpecs {
		if f.short != "" && f.short == letter {
			return f, true
		}
	}
	return flagSpec{}, false
}

// allowedFlags is what one command accepts: its own flags plus the globals.
func allowedFlags(c commandSpec) []string {
	return append(append([]string{}, globalFlags...), c.flags...)
}

// commandsForFlag names the commands that accept a flag — empty for the
// globals, which every command takes. The man page says where each belongs.
func commandsForFlag(name string) []string {
	if slices.Contains(globalFlags, name) {
		return nil
	}
	var owners []string
	if slices.Contains(interactiveFlags, name) {
		owners = append(owners, "(no command)")
	}
	for _, c := range commandSpecs {
		if slices.Contains(c.flags, name) {
			owners = append(owners, c.name)
		}
	}
	return owners
}

// suggestCommand is "did you mean" for a command word, aliases included.
func suggestCommand(word string) string {
	var names []string
	for _, c := range commandSpecs {
		names = append(names, c.name)
		names = append(names, c.aliases...)
	}
	return closest(word, names)
}

// closest is the candidate within a small edit distance of word, or "".
// Two edits catches the realistic typo (sesions, modle) without offering
// `run` for a word that shares nothing with it; a prefix of three or more
// letters is named with confidence even when it is further away.
func closest(word string, candidates []string) string {
	limit := 2
	if len(word) <= 4 {
		limit = 1
	}
	lower := strings.ToLower(word)
	best, bestScore := "", limit+1
	for _, c := range candidates {
		if len(lower) >= 3 && strings.HasPrefix(c, lower) {
			return c
		}
		if d := editDistance(lower, c, limit); d < bestScore {
			best, bestScore = c, d
		}
	}
	if bestScore <= limit {
		return best
	}
	return ""
}

// editDistance is Levenshtein capped at limit: past it, the exact number is
// never needed, so a hopeless candidate bails out a row early.
func editDistance(a, b string, limit int) int {
	if abs(len(a)-len(b)) > limit {
		return limit + 1
	}
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		row := make([]int, len(b)+1)
		row[0] = i
		best := i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			row[j] = min(prev[j]+1, row[j-1]+1, prev[j-1]+cost)
			best = min(best, row[j])
		}
		if best > limit {
			return limit + 1
		}
		prev = row
	}
	return prev[len(b)]
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
