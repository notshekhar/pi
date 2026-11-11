package main

import (
	"context"
	"fmt"
	"os"

	"github.com/notshekhar/pi/internal/core/config"
)

// Top-level subcommand dispatch — loop's cli.ts switch.
//
// A word that is not in commandSpecs is refused with a suggestion instead of
// opening the TUI, and each command accepts only its own flags.

// cliCall is everything one subcommand gets.
type cliCall struct {
	ctx context.Context
	// globals are the flags given before the command word (pi --model x run …).
	globals config.Config
	// session is --session/--resume given before the command word.
	session string
	args    cliArgs
	// raw is the argv after the command word, for commands with their own parser.
	raw []string
}

// cwd is --cwd, or the process's own directory.
func (c *cliCall) cwd() string {
	if v, ok := c.args.flag("cwd"); ok {
		return v
	}
	return workingDir(c.globals.CWD)
}

// cliHandler runs one command and returns the process exit code.
type cliHandler func(*cliCall) int

// cliHandlers maps each command name in commandSpecs to its implementation.
func cliHandlers() map[string]cliHandler {
	return map[string]cliHandler{
		"run":        runCommand,
		"login":      loginCommand,
		"logout":     logoutCommand,
		"whoami":     whoamiCommand,
		"models":     modelsCommand,
		"cost":       costCommand,
		"artifacts":  artifactsCommand,
		"goals":      goalsCommand,
		"mcp":        func(c *cliCall) int { return mcpCommand(c.cwd(), c.raw) },
		"install":    installExtensionCommand,
		"link":       linkExtensionCommand,
		"remove":     removeExtensionCommand,
		"extensions": listExtensionsCommand,
		"enable":     func(c *cliCall) int { return setExtensionCommand(c, true) },
		"disable":    func(c *cliCall) int { return setExtensionCommand(c, false) },
		"completion": completionCommand,
		"man":        manCommand,
		"sessions":   exitZero(func(c *cliCall) { sessionsCommand(c.cwd(), c.raw) }),
		"archive":    exitZero(func(c *cliCall) { archiveCommand(c.raw, true) }),
		"unarchive":  exitZero(func(c *cliCall) { archiveCommand(c.raw, false) }),
		"rpc":        exitZero(func(c *cliCall) { rpcCommand(c.cwd()) }),
		"serve":      exitZero(func(c *cliCall) { serveCommand(c.cwd(), c.raw) }),
		"upgrade":    exitZero(func(c *cliCall) { upgradeCommand(c.raw) }),
		"version":    exitZero(func(*cliCall) { fmt.Println(version) }),
		"help":       exitZero(func(*cliCall) { printHelp() }),
	}
}

// exitZero adapts a command that exits on its own when it fails.
func exitZero(f func(*cliCall)) cliHandler {
	return func(c *cliCall) int { f(c); return 0 }
}

// runCLICommand dispatches argv[0] and returns the exit code.
func runCLICommand(ctx context.Context, globals config.Config, session string, argv []string) int {
	spec, ok := findCommand(argv[0])
	if !ok {
		return printUsageErrors(os.Stderr, []string{unknownCommandError(argv[0])})
	}
	handler, ok := cliHandlers()[spec.name]
	if !ok {
		// The table lists a command nothing dispatches: our bug, not the user's.
		fmt.Fprintf(os.Stderr, "internal: no handler for command %q (cmd/pi/cli_spec.go lists it)\n", spec.name)
		return 70
	}
	call := &cliCall{ctx: ctx, globals: globals, session: session, raw: argv[1:]}
	if !spec.passthrough {
		call.args = parseCommandArgs(spec, argv[1:])
		if len(call.args.errors) > 0 {
			return printUsageErrors(os.Stderr, call.args.errors)
		}
		if call.args.has("help") {
			printHelp()
			return 0
		}
		if call.args.has("version") {
			fmt.Println(version)
			return 0
		}
	}
	return handler(call)
}
