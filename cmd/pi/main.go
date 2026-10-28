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
	"strings"
	"syscall"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/catalog"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/db"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/tools"
	"github.com/notshekhar/pi/internal/tui"
)

func main() {
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

	// Session housekeeping needs no model.
	if len(args) > 0 {
		switch args[0] {
		case "sessions":
			sessionsCommand(workingDir(cfg.CWD), args[1:])
			return
		case "archive", "unarchive":
			archiveCommand(args[1:], args[0] == "archive")
			return
		case "rpc":
			rpcCommand(workingDir(cfg.CWD))
			return
		case "serve":
			serveCommand(workingDir(cfg.CWD), args[1:])
			return
		case "upgrade":
			upgradeCommand(args[1:])
			return
		}
	}

	// `run` takes --session after the command too, the way loop's does.
	var prompt string
	oneShotMode := len(args) > 0 && args[0] == "run"
	if oneShotMode {
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		fs.StringVar(sessionID, "session", *sessionID, "resume a session by id")
		fs.StringVar(sessionID, "resume", *sessionID, "alias for -session")
		_ = fs.Parse(args[1:])
		prompt = strings.Join(fs.Args(), " ")
		if prompt == "" {
			fmt.Fprintf(os.Stderr, "usage: %s run [--session <id>] <prompt>\n", productName)
			os.Exit(2)
		}
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

	if oneShotMode {
		if err := oneShot(ctx, resolved, prompt, resumed); err != nil {
			fatal(err)
		}
		return
	}

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

func oneShot(ctx context.Context, cfg config.Config, prompt string, resumed *session.Session) error {
	run, err := newRun(cfg, resumed)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s · %s\n\n", cfg.FullID(), cfg.CWD)
	// Each step bills itself as it is saved, so a session started with `run`
	// carries its spend into a later resume.
	if _, err := run.TurnStream(ctx, prompt, tui.NewPrinter().Consume); err != nil {
		return err
	}
	// Reported so a follow-up can pass --session.
	if run.Session.Saved() {
		fmt.Fprintf(os.Stderr, "\nsession %s\n", run.Session.ID)
	}
	return nil
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
