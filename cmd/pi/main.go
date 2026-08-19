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

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/core/agent"
	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/db"
	"github.com/notshekhar/pi/internal/modules/core/session"
	"github.com/notshekhar/pi/internal/modules/core/tools"
	"github.com/notshekhar/pi/internal/modules/tui"
)

func main() {
	cfg := config.Config{}
	flag.StringVar(&cfg.Provider, "provider", "", "provider id (kimi, anthropic, …)")
	flag.StringVar(&cfg.ModelID, "model", "", "short id or provider/model")
	flag.StringVar(&cfg.CWD, "cwd", "", "working directory")
	flag.IntVar(&cfg.MaxSteps, "max-steps", 0, "maximum agent steps")
	reasoning := flag.String("reasoning", "", "none, low, medium, high, or xhigh")
	flag.Parse()

	cfg.Reasoning = provider.ReasoningEffort(*reasoning)
	// Stored preferences layer UNDER the flags: an explicit -model always
	// beats a remembered one, and the file only fills what was left blank.
	settings := config.LoadSettings()
	resolved, err := config.Resolve(settings.ApplyTo(cfg))
	if err != nil {
		fatal(err)
	}

	args := flag.Args()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Stamps the clean-shutdown marker, which is what lets the next launch
	// skip a whole-file integrity scan. Missing it costs a scan, not data —
	// but it is missed on every exit path that does not come through here,
	// which is why it sits at the top rather than inside one command.
	defer db.Close()

	if len(args) > 0 && args[0] == "run" {
		prompt := strings.Join(args[1:], " ")
		if prompt == "" {
			fmt.Fprintln(os.Stderr, "usage: pi-agent run <prompt>")
			os.Exit(2)
		}
		if err := oneShot(ctx, resolved, prompt); err != nil {
			fatal(err)
		}
		return
	}

	if err := replTUI(ctx, resolved); err != nil {
		fatal(err)
	}
}

func oneShot(ctx context.Context, cfg config.Config, prompt string) error {
	run, err := newRun(cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s · %s\n\n", cfg.FullID(), cfg.CWD)
	return run.Turn(ctx, prompt, tui.NewPrinter().Consume)
}

func newRun(cfg config.Config) (*agent.Run, error) {
	sess := session.New(cfg.FullID(), cfg.CWD)
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
