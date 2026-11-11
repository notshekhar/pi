package main

import (
	"context"
	"time"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/agents"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/hooks"
	"github.com/notshekhar/pi/internal/core/projects"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/shells"
)

// One turn with no terminal — what `pi run` and a goal run share (loop's
// runPrint and runGoal recipe): the run built as the RPC server builds it,
// SessionStart hooks before, SessionEnd hooks after, and nothing left
// running when it returns.

// sessionEndGrace bounds the SessionEnd hooks: give them a moment, then
// finish regardless, as loop does.
const sessionEndGrace = 3 * time.Second

// headlessTurn is one unattended turn's inputs.
type headlessTurn struct {
	cfg     config.Config // resolved
	session *session.Session
	prompt  string
	// maxSteps and reasoning, when set, beat the stored settings.
	maxSteps  int
	reasoning provider.ReasoningEffort
	// agent, when set, is the agent to run as instead of the saved one.
	agent string
	emit  agent.Emitter
}

// headlessModel is the model a headless run uses: the explicit one, then the
// resumed session's, then this folder's last pick, then the default. No
// silent provider fallback — "" means none was chosen.
func headlessModel(explicit string, resumed *session.Session, cwd string) string {
	if explicit != "" {
		return explicit
	}
	if resumed != nil && resumed.Meta.Model != "" {
		return resumed.Meta.Model
	}
	if m := projects.Model(cwd); m != "" {
		return m
	}
	return config.LoadSettings().DefaultModel
}

// runHeadless drives the turn and returns what it cost.
func runHeadless(ctx context.Context, h headlessTurn) (agent.SessionCost, error) {
	host := &rpcHost{}
	defer host.close()
	run, err := host.newRun(h.session, h.cfg.FullID(), nil)
	if err != nil {
		h.emit("error", agent.PlainError(err))
		return agent.SessionCost{}, err
	}
	if h.maxSteps > 0 {
		run.Config.MaxSteps = h.maxSteps
	}
	if h.reasoning != "" {
		run.Config.Reasoning = h.reasoning
	}
	if h.agent != "" && agents.Exists(h.agent) {
		run.UseAgent(h.agent)
	}

	input := h.prompt
	if extra := sessionStartHooks(ctx, run, h.emit); extra != "" {
		input = extra + "\n\n" + input
	}
	tracker := agent.NewCostTracker()
	_, err = run.RunTurn(ctx, agent.TurnOptions{Input: input, Tracker: tracker, Emit: h.emit})
	// A one-shot run has no later turn in which to notice a shell it left.
	shells.KillAllBashChildren()
	sessionEndHooks(run)
	return tracker.SessionBreakdown(), err
}

// sessionStartHooks runs the SessionStart hooks and returns their
// additional context. Their messages go through the emitter, so the
// reporter decides where they land — raw on stdout, they would corrupt a
// JSON run's output.
func sessionStartHooks(ctx context.Context, run *agent.Run, emit agent.Emitter) string {
	cfg, err := config.LoadSettings().HookConfig()
	if err != nil || len(cfg[hooks.SessionStart]) == 0 {
		return ""
	}
	out := hooks.Run(ctx, cfg, hooks.Context{Event: hooks.SessionStart, CWD: run.Config.CWD, SessionID: run.Session.ID})
	for _, m := range out.Messages {
		emit("hook-message", m)
	}
	return out.AdditionalContext
}

func sessionEndHooks(run *agent.Run) {
	cfg, err := config.LoadSettings().HookConfig()
	if err != nil || len(cfg[hooks.SessionEnd]) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionEndGrace)
	defer cancel()
	hooks.Run(ctx, cfg, hooks.Context{Event: hooks.SessionEnd, CWD: run.Config.CWD, SessionID: run.Session.ID, Reason: "exit"})
}
