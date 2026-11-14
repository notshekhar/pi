package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/core/shells"
)

// Default and ceiling for the bash timeout. A command with no timeout is
// capped at the default so a hung process (a server left in the foreground,
// a command waiting on stdin) cannot run unbounded. The model may ask for
// longer for real long work, but never above the maximum.
const (
	defaultBashTimeoutSec = 120
	maxBashTimeoutSec     = 600
)

// exitStdioGrace is how long a finished command's output is waited for. A
// process it left behind can hold the pipe open for ever; the command is done
// when its shell is, plus this long for the last bytes to arrive.
const exitStdioGrace = 100 * time.Millisecond

type bashArgs struct {
	Command         string  `json:"command"`
	Timeout         float64 `json:"timeout,omitzero"`
	RunInBackground bool    `json:"run_in_background,omitzero"`
}

func resolveBashTimeout(seconds float64) time.Duration {
	if seconds <= 0 {
		seconds = defaultBashTimeoutSec
	}
	return time.Duration(min(seconds, maxBashTimeoutSec) * float64(time.Second))
}

// Bash returns the bash tool.
func Bash(t *Context) ai.Tool {
	return LoopTool("bash", func(ctx context.Context, a bashArgs) (ai.ToolResult, error) {
		run := bashRun{tools: t, command: a.Command, timeout: resolveBashTimeout(a.Timeout)}
		if a.RunInBackground {
			if err := run.backgroundRefusal(); err != nil {
				return fail(err)
			}
		}
		confinement, err := resolveSandbox(a.Command, t)
		if err != nil {
			return fail(err)
		}
		run.argv = confinement.argv
		var text string
		if a.RunInBackground {
			text, err = run.background()
		} else {
			text, err = run.foreground(ctx)
		}
		if err != nil {
			return fail(errors.New(withSandboxWarning(confinement.warning, err.Error())))
		}
		return ai.ToolText(withSandboxWarning(confinement.warning, text)), nil
	})
}

// bashRun is one bash call.
type bashRun struct {
	tools   *Context
	command string
	timeout time.Duration
	// argv is the sandbox wrapper; nil runs the plain shell.
	argv []string
}

func (b bashRun) spawn(sink io.Writer) (*shells.Child, error) {
	return shells.Spawn(shells.SpawnOptions{Command: b.command, CWD: b.tools.CWD, Argv: b.argv, Sink: sink})
}

func (b bashRun) register(child *shells.Child, seed string, promoted bool) shells.Info {
	return shells.Register(shells.RegisterOptions{
		SessionID: b.tools.SessionID, Command: b.command, CWD: b.tools.CWD, Child: child, Seed: seed, Promoted: promoted,
	})
}

func (b bashRun) shellsAtCap() bool {
	return shells.RunningCount(b.tools.SessionID) >= shells.MaxBackground
}

// backgroundRefusal is why this call may not run in the background, or nil.
func (b bashRun) backgroundRefusal() error {
	switch {
	case !b.tools.BackgroundShells:
		return errors.New("Background shells are off (the backgroundShells setting is opt-in). Run the command in the foreground, with a longer timeout if it needs one.")
	case b.tools.ReadOnlyBash || b.tools.planMode():
		// A read-only planning agent must not leave daemons behind: plan
		// mode ends, the process does not.
		return errors.New("Background shells are not available here — this agent runs bash read-only and must not leave processes running. Run the command in the foreground, or leave it for after plan mode.")
	case b.shellsAtCap():
		return fmt.Errorf("Too many background shells (%d already running). Kill one with the shells tool before starting another.", shells.MaxBackground)
	}
	return nil
}

// background spawns the command, hands it to the registry and returns its
// id. The turn's cancellation is deliberately not wired: esc ends the turn,
// and must not take the user's dev server with it.
func (b bashRun) background() (string, error) {
	child, err := b.spawn(nil)
	if err != nil {
		return "", err
	}
	info := b.register(child, "", false)
	return fmt.Sprintf("Started %s in the background (pid %d).\nRead its output with shells({ action: \"output\", id: \"%s\" }). You will be told when it exits.", info.ID, info.PID, info.ID), nil
}

func (b bashRun) foreground(ctx context.Context) (string, error) {
	output := newOutputAccumulator()
	defer output.close()
	child, err := b.spawn(output)
	if err != nil {
		return "", err
	}
	deadline := time.NewTimer(b.timeout)
	defer deadline.Stop()
	select {
	case <-child.Exited():
		return b.finished(child, output)
	case <-ctx.Done():
		b.stop(child)
		return "", errors.New(appendStatus(renderOutput(output, ""), "Command aborted"))
	case <-deadline.C:
		if info, ok := b.promote(child, output); ok {
			return fmt.Sprintf("Still running after %ss, so it was moved to the background as %s (pid %d) rather than killed.\nRead it with shells({ action: \"output\", id: \"%s\" }), or kill it with shells({ action: \"kill\", id: \"%s\" }). You will be told when it exits.",
				seconds(b.timeout), info.ID, info.PID, info.ID, info.ID), nil
		}
		b.stop(child)
		return "", errors.New(appendStatus(renderOutput(output, ""), fmt.Sprintf("Command timed out after %s seconds", seconds(b.timeout))))
	}
}

// stop kills the command's whole tree and lets go of it.
func (b bashRun) stop(child *shells.Child) {
	child.KillTree()
	<-child.Exited()
	b.drain(child)
}

// drain waits briefly for the last output, then stops reading.
func (b bashRun) drain(child *shells.Child) {
	select {
	case <-child.Closed():
	case <-time.After(exitStdioGrace):
	}
	child.Detach()
}

func (b bashRun) finished(child *shells.Child, output *outputAccumulator) (string, error) {
	b.drain(child)
	text := renderOutput(output, "(no output)")
	// A command ended by a signal has no exit code to report.
	if code := child.ExitCode(); code > 0 {
		return "", errors.New(appendStatus(text, fmt.Sprintf("Command exited with code %d", code)))
	}
	return text, nil
}

// promote hands a command that outlived its timeout to the registry instead
// of killing it — but only where a shells panel will show it. The timeout
// exists to stop an invisible long run, and a promoted shell nobody lists is
// that invisible run again.
func (b bashRun) promote(child *shells.Child, output *outputAccumulator) (shells.Info, bool) {
	if !b.tools.BackgroundShells || !shells.PanelPresent() || b.shellsAtCap() {
		return shells.Info{}, false
	}
	// The registry replays what the foreground collected, then takes the
	// stream over: two writers on one child would duplicate every chunk.
	return b.register(child, output.snapshot().Content, true), true
}

// withSandboxWarning puts a sandbox that could not hold in front of the
// command's output, so neither the model nor the user mistakes it for a
// confined run.
func withSandboxWarning(warning, text string) string {
	if warning == "" {
		return text
	}
	return appendStatus("[pi sandbox] "+warning, text)
}

func appendStatus(text, status string) string {
	if text == "" {
		return status
	}
	return text + "\n\n" + status
}

// renderOutput is the command's output with a note on what was cut.
func renderOutput(output *outputAccumulator, emptyText string) string {
	snap := output.snapshot()
	text := snap.Content
	if text == "" {
		text = emptyText
	}
	if !snap.Truncated {
		return text
	}
	fullOutput := ""
	if snap.fullOutputPath != "" {
		fullOutput = " Full output: " + snap.fullOutputPath
	}
	first, last := snap.TotalLines-snap.OutputLines+1, snap.TotalLines
	switch {
	case snap.LastLinePartial:
		return fmt.Sprintf("%s\n\n[Showing last %s of line %d (line is %s).%s]", text, FormatSize(int64(snap.OutputBytes)), last, FormatSize(int64(snap.lastLineBytes)), fullOutput)
	case snap.TruncatedBy == "lines":
		return fmt.Sprintf("%s\n\n[Showing lines %d-%d of %d.%s]", text, first, last, snap.TotalLines, fullOutput)
	}
	return fmt.Sprintf("%s\n\n[Showing lines %d-%d of %d (%s limit).%s]", text, first, last, snap.TotalLines, FormatSize(DefaultMaxBytes), fullOutput)
}

// seconds prints a duration as a bare number of seconds: 120, 1.5.
func seconds(d time.Duration) string {
	return fmt.Sprintf("%g", d.Seconds())
}
