// Command pi-agent is a terminal coding agent built on pigo.
//
//	go run ./cmd/pi-agent run "what is in ./internal/tools?"
//	go run ./cmd/pi-agent -provider google -model gemini-3-pro
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/catalog"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/db"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/tools"
)

// printHelp is loop's printHelp, for pi's commands.
func printHelp() {
	fmt.Printf(`%[1]s — terminal coding agent, a Go build of loop (v%[2]s)

Usage:
  %[1]s                     Start interactive TUI
  %[1]s run <prompt|->      Run a single prompt and exit (- reads the prompt from stdin)
  %[1]s login [provider]    Configure provider auth
  %[1]s logout [provider]   Remove auth
  %[1]s sessions [--archived|--all] List sessions in current cwd
  %[1]s archive <id>        Archive a session (hides it from the lists)
  %[1]s unarchive <id>      Restore an archived session
  %[1]s goals <cmd>         Manage background tasks (list, add, rm, run, tick)
  %[1]s artifacts [export <id>] List pages the agent wrote, or save one out
  %[1]s models              List available models
  %[1]s whoami              Show active provider + auth status
  %[1]s cost audit          Verify the cost ledger reconciles (self-audit)
  %[1]s rpc                 Start the JSON-RPC server on stdio (loop's desktop app)
  %[1]s serve [--host|--port|--no-terminal] Web UI + WebSocket RPC (opt-in via /settings; token-locked)
  %[1]s mcp <cmd>           Manage MCP servers (add, list, remove, login…)
  %[1]s man                 Open the manual (--install writes it to the manpath)
  %[1]s completion <shell>  Print a tab-completion script (bash, zsh, fish)
  %[1]s upgrade [--force]   Install the latest release
  %[1]s version | -v        Print version
  %[1]s help | -h           Show this help

Extensions:
  %[1]s install <path>      Build and install a Go extension (a main package speaking MCP)
  %[1]s link <path>         Link a local extension for development (rebuilt on every connect)
  %[1]s remove <name>       Remove an installed extension
  %[1]s extensions          List installed extensions
  %[1]s enable <name>       Enable an extension
  %[1]s disable <name>      Disable an extension

Flags:
  --model <provider/id>    Override default model
  --provider <id>          Override active provider
  --cwd <path>             Working directory
  --session <id>           Resume session by id
  --reasoning <level>      none, low, medium, high, xhigh
  --max-steps <n>          Cap agent steps in run mode (default: maxSteps setting)
  --output-format <fmt>    run mode output: text (default), json, stream-json
  --                       End flag parsing (a prompt may then start with -)

In run mode, text puts the reply on stdout and activity on stderr; json prints
one result object when the run ends; stream-json prints one JSON event per line
as it happens. Both JSON formats keep stdout free of anything but JSON, and
report the session id so a follow-up can pass --session.

Unknown commands and flags are refused rather than guessed at; each command
accepts only its own flags (%[1]s completion <shell> teaches your shell which).
`, productName, version)
}

func main() {
	os.Args = append(os.Args[:1], withoutScriptExtensions(os.Args[1:])...)
	// loop's `version | -v | --version` and `help | -h | --help`, answered
	// before the flag parser, which would reject --version as unknown.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "-v", "--version", "-version":
			fmt.Println(version)
			return
		case "help", "-h", "--help", "-help":
			printHelp()
			return
		}
	}
	cfg := config.Config{}
	flag.StringVar(&cfg.Provider, "provider", "", "provider id (kimi, anthropic, …)")
	flag.StringVar(&cfg.ModelID, "model", "", "short id or provider/model")
	flag.StringVar(&cfg.CWD, "cwd", "", "working directory")
	flag.IntVar(&cfg.MaxSteps, "max-steps", 0, "maximum agent steps")
	reasoning := flag.String("reasoning", "", "none, low, medium, high, or xhigh")
	sessionID := flag.String("session", "", "resume a session by id")
	flag.StringVar(sessionID, "resume", "", "alias for -session")
	flag.Parse()
	cfg.Reasoning = provider.ReasoningEffort(*reasoning)

	args := flag.Args()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Stamps the clean-shutdown marker, which is what lets the next launch
	// skip a whole-file integrity scan. Missing it costs a scan, not data —
	// but it is missed on every exit path that does not come through here,
	// which is why it sits at the top rather than inside one command.
	defer db.Close()

	if len(args) > 0 {
		code := runCLICommand(ctx, cfg, *sessionID, args)
		db.Close() // os.Exit skips the deferred one
		if code != 0 {
			os.Exit(code)
		}
		return
	}

	// Stored preferences layer UNDER the flags: an explicit -model always
	// beats a remembered one, and the file only fills what was left blank.
	cfg = config.LoadSettings().ApplyTo(cfg)
	// A resumed session carries on with the model it was last using, as in
	// loop: the conversation's own history is the strongest signal of what
	// should answer it.
	var resumed *session.Session
	if *sessionID != "" {
		resumed = openSession(*sessionID)
		if resumed.Meta.Model != "" {
			cfg.Provider, cfg.ModelID = "", resumed.Meta.Model
		}
	}
	resolved, err := config.Resolve(cfg)
	if err != nil {
		fatal(err)
	}

	// The catalog learns new models at runtime (models.dev + each provider's
	// own list), cached for an hour; a stale cache revalidates in the
	// background without holding up startup.
	config.ConfigureCatalog()
	catalog.RefreshIfStale()

	id, err := replTUI(ctx, resolved, resumed)
	if err != nil {
		fatal(err)
	}
	printResumeHint(id)
}

// workingDir is -cwd, or the process's own directory.
func workingDir(cwd string) string {
	if cwd != "" {
		return cwd
	}
	wd, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	return wd
}

// newRun builds a run on a fresh session, or on resumed when one was given.
func newRun(cfg config.Config, resumed *session.Session) (*agent.Run, error) {
	sess := resumed
	if sess == nil {
		sess = session.New(cfg.FullID(), cfg.CWD)
	}
	return &agent.Run{
		Config:  cfg,
		Session: sess,
		Tools:   &tools.Context{CWD: cfg.CWD, Registry: tools.NewRegistry()},
	}, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
