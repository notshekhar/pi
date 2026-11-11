package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/home"
	"github.com/notshekhar/pi/internal/core/session"
)

// Headless goal runs and the scheduler heartbeat — loop's goals/run.ts and
// goals/tick.ts. Each run is a normal saved session named
// "background: <text>", so either program can list and resume it.

// goalMaxSteps bounds unattended spend: fewer steps than an interactive turn.
const goalMaxSteps = 50

// goalSummaryMax is how much of the closing reply the goal row keeps.
const goalSummaryMax = 300

const goalPreamble = "You are running unattended on a schedule. Work toward the goal below, then end with a short summary of what you did and whether anything needs the user's attention."

type goalRunResult struct {
	status, sessionID, summary string
}

// goalTranscript keeps what a goal's summary is made of: the reply after the
// last tool call (the model's closing summary, as a subagent reports), and
// any errors, so a failed run says what broke rather than half a reply.
type goalTranscript struct {
	mu        sync.Mutex
	finalText strings.Builder
	errors    []string
}

func (t *goalTranscript) emit(event string, data any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch event {
	case "text-delta":
		if s, ok := data.(string); ok {
			t.finalText.WriteString(s)
		}
	case "tool-call":
		t.finalText.Reset()
	case "error":
		t.errors = append(t.errors, errorText(data))
	}
}

// summary is the row's last_run_summary for a run that ended with status.
func (t *goalTranscript) summary(status string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	text := strings.TrimSpace(t.finalText.String())
	if status == "error" {
		if errs := strings.TrimSpace(strings.Join(t.errors, "\n")); errs != "" {
			text = errs
		}
	}
	if r := []rune(text); len(r) > goalSummaryMax {
		text = string(r[:goalSummaryMax])
	}
	switch {
	case text != "":
		return text
	case status == "ok":
		return "(no response)"
	}
	return "(failed with no output)"
}

// runGoal runs one goal now and records the outcome on its row. A recovered
// mid-turn error keeps status ok; only a turn that failed outright is an
// error.
func runGoal(ctx context.Context, g goal) goalRunResult {
	model := headlessModel(g.model, nil, g.cwd)
	if model == "" {
		_ = recordGoalRunEnd(g.id, "error", "no model configured for headless runs")
		return goalRunResult{status: "error", summary: "no model configured"}
	}
	cfg, err := config.Resolve(config.Config{ModelID: model, CWD: g.cwd})
	if err != nil {
		_ = recordGoalRunEnd(g.id, "error", err.Error())
		return goalRunResult{status: "error", summary: err.Error()}
	}
	sess := session.New(cfg.FullID(), cfg.CWD)
	_ = sess.SetName("background: " + g.text)
	_ = recordGoalRunStart(g.id, sess.ID)

	transcript := &goalTranscript{}
	_, err = runHeadless(ctx, headlessTurn{
		cfg: cfg, session: sess, prompt: goalPreamble + "\n\nGoal: " + g.text,
		maxSteps: goalMaxSteps, agent: g.agent, emit: transcript.emit,
	})
	status := "ok"
	if err != nil {
		status = "error"
	}
	summary := transcript.summary(status)
	_ = recordGoalRunEnd(g.id, status, summary)
	return goalRunResult{status: status, sessionID: sess.ID, summary: summary}
}

// goalsTick runs every due goal once, then exits — what a scheduler calls
// each minute. The pid lock is loop's own file, so a pi tick and a loop
// tick never run the same goals at once. No backfill of missed windows, and
// a fired one-shot stays as a disabled row so its last run stays visible.
func goalsTick(c *cliCall) int {
	release, ok := acquireTickLock()
	if !ok {
		return 0 // the previous tick is still running
	}
	defer release()
	goals, err := listGoals()
	if err != nil {
		return goalError(err)
	}
	ran := 0
	for _, g := range dueGoals(goals, time.Now()) {
		fmt.Fprintf(os.Stderr, "running goal: %s\n", g.text)
		result := runGoal(c.ctx, g)
		fmt.Fprintf(os.Stderr, "goal %s: %s\n", result.status, g.text)
		if g.kind == "once" {
			_ = disableGoal(g.id)
		}
		ran++
	}
	if ran > 0 {
		fmt.Printf("ran %d goal%s\n", ran, plural(ran))
	}
	return 0
}

func tickPidPath() string { return home.AgentDir("goals-tick.pid") }

// liveTickPid is the pid in the lock file when that process still runs.
func liveTickPid(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, pidAlive(pid)
}

// acquireTickLock takes the tick lock, or reports a live holder. A stale
// lock (its process gone) is simply taken over.
func acquireTickLock() (release func(), ok bool) {
	path := tickPidPath()
	if _, live := liveTickPid(path); live {
		return nil, false
	}
	if _, err := home.Ensure("agent"); err != nil {
		return func() {}, true
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
	return func() {
		if pid, _ := liveTickPid(path); pid == os.Getpid() {
			_ = os.Remove(path)
		}
	}, true
}
