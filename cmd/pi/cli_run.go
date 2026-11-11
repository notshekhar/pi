package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/catalog"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
)

// `pi run <prompt|->` — loop's cmdRun + runPrint: one prompt, reported as
// text, json or stream-json, exiting nonzero when the turn errored so CI
// gets more than an error line buried in stderr.

func runCommand(c *cliCall) int {
	format := "text"
	if v, ok := c.args.flag("output-format"); ok {
		format = v
	}
	prompt, ok := runPrompt(c.args.positional)
	if !ok {
		fmt.Fprintf(os.Stderr, "Usage: %[1]s run \"<prompt>\"   (or: echo \"<prompt>\" | %[1]s run -)\n", productName)
		return 1
	}
	turn, msg := prepareRunTurn(c)
	if msg != "" {
		return failRunEarly(format, msg)
	}
	reporter := newRunReporter(format, os.Stdout, os.Stderr)
	turn.emit = reporter.emit
	turn.prompt = prompt
	started := time.Now()
	reporter.begin(runContext{sessionID: turn.session.ID, model: turn.cfg.FullID(), cwd: turn.cfg.CWD})
	cost, err := runHeadless(c.ctx, turn)
	if err != nil && !reporter.errored() && c.ctx.Err() == nil {
		turn.emit("error", err.Error())
	}
	reporter.end(runResult{cost: cost, duration: time.Since(started)})
	if reporter.errored() {
		return 1
	}
	return 0
}

// runPrompt is the prompt from the arguments, or from stdin for `-` and for
// no argument with a pipe attached — CI pipes long prompts rather than
// shell-quoting them.
func runPrompt(positional []string) (string, bool) {
	prompt := strings.Join(positional, " ")
	if prompt == "-" || (prompt == "" && !isTerminal(os.Stdin)) {
		prompt = strings.TrimSpace(readStdinAll())
	}
	return prompt, prompt != ""
}

// prepareRunTurn resolves the session and model, or says why it cannot.
// A resumed session supplies its own cwd and model as defaults; an unknown
// id fails rather than silently forking a fresh session.
func prepareRunTurn(c *cliCall) (headlessTurn, string) {
	var turn headlessTurn
	var resumed *session.Session
	if id := c.sessionFlag(); id != "" {
		s, err := session.Load(id)
		if err != nil {
			return turn, fmt.Sprintf("Session not found: %s (see `%s sessions`)", id, productName)
		}
		resumed = s
	}
	cwd := workingDir(c.globals.CWD)
	if v, ok := c.args.flag("cwd"); ok {
		cwd = v
	} else if resumed != nil && c.globals.CWD == "" {
		cwd = resumed.Info.CWD
	}
	explicit, _ := c.args.flag("model")
	if explicit == "" {
		explicit = c.globals.ModelID
	}
	model := headlessModel(explicit, resumed, cwd)
	if model == "" && c.globals.Provider == "" {
		return turn, fmt.Sprintf("No model selected. Pass --model <provider/model>, or run %s interactively and use /login + /provider first.", productName)
	}
	cfg, err := config.Resolve(config.Config{Provider: c.globals.Provider, ModelID: model, CWD: cwd})
	if err != nil {
		return turn, err.Error()
	}
	config.ConfigureCatalog()
	catalog.RefreshIfStale()

	turn.cfg, turn.session = cfg, resumed
	if turn.session == nil {
		turn.session = session.New(cfg.FullID(), cfg.CWD)
	}
	turn.maxSteps = c.globals.MaxSteps
	if v, ok := c.args.flag("max-steps"); ok {
		turn.maxSteps, _ = strconv.Atoi(v)
	}
	turn.reasoning = c.globals.Reasoning
	if v, ok := c.args.flag("reasoning"); ok {
		turn.reasoning = provider.ReasoningEffort(v)
	}
	return turn, ""
}

// sessionFlag is --session after the command, or before it.
func (c *cliCall) sessionFlag() string {
	if v, ok := c.args.flag("session"); ok {
		return v
	}
	return c.session
}

// failRunEarly reports a run that never started in the run's own format: a
// JSON caller gets a result object it parses like any other, instead of
// every consumer special-casing "no output at all".
func failRunEarly(format, msg string) int {
	if format == "text" {
		fmt.Fprintln(os.Stderr, msg)
	} else {
		writeJSONLine(os.Stdout, struct {
			Type    string   `json:"type"`
			IsError bool     `json:"is_error"`
			Errors  []string `json:"errors"`
		}{"result", true, []string{msg}})
	}
	return 1
}
