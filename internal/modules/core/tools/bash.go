package tools

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/modules/ai"
)

const (
	defaultBashTimeout = 120 * time.Second
	maxBashTimeout     = 10 * time.Minute
	maxBashOutput      = 64 << 10
)

type bashArgs struct {
	Command string `json:"command" jsonschema:"description=Shell command to run"`
	Timeout *int   `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds (default 120, max 600)"`
}

// Bash returns the bash tool.
func Bash(t *Context) ai.Tool {
	return ai.NewTool("bash",
		"Run a shell command in the working directory. Use absolute paths or explicit cd; the shell does not keep state between calls. Output is truncated at 64KB. Default timeout 120s.",
		func(ctx context.Context, a bashArgs) (ai.ToolResult, error) {
			if aborted(ctx) {
				return ai.ToolError("aborted"), nil
			}
			if msg := denyCommand(a.Command); msg != "" {
				return ai.ToolError(msg), nil
			}
			timeout := defaultBashTimeout
			if a.Timeout != nil && *a.Timeout > 0 {
				timeout = time.Duration(*a.Timeout) * time.Second
				if timeout > maxBashTimeout {
					timeout = maxBashTimeout
				}
			}
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			argv, _ := WrapSandbox(t.Sandbox, t.CWD, a.Command)
			// The user's own shell, unless the sandbox supplied its own argv:
			// a login shell's rc file is often where the toolchain lives.
			if argv[0] == "sh" {
				if shell := os.Getenv("SHELL"); shell != "" {
					argv[0] = shell
				} else {
					argv[0] = "/bin/sh"
				}
			}
			cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
			cmd.Dir = t.CWD
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			err := cmd.Run()

			text := out.String()
			if len(text) > maxBashOutput {
				text = text[:maxBashOutput] + "\n… [output truncated]"
			}
			if err != nil {
				if runCtx.Err() == context.DeadlineExceeded {
					return ai.ToolErrorf("command timed out after %s\n%s", timeout, text), nil
				}
				if text == "" {
					return ai.ToolErrorf("%v", err), nil
				}
				return ai.ToolErrorf("%v\n%s", err, text), nil
			}
			if text == "" {
				text = "(no output)"
			}
			return ai.ToolText(text), nil
		})
}

func denyCommand(command string) string {
	c := strings.TrimSpace(command)
	denied := []string{
		"rm -rf /", "rm -rf / ", "rm -rf /*",
		"rm -fr /", "rm -fr /*",
		"mkfs.", "dd if=", ":(){", "shutdown", "reboot",
	}
	lower := strings.ToLower(c)
	for _, d := range denied {
		if strings.Contains(lower, d) {
			return "command refused: matches a dangerous pattern (" + d + ")"
		}
	}
	return ""
}
