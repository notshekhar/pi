package extension

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// gitExt puts the three git questions asked mid-session behind one command,
// so answering them does not cost a model turn.
//
// It shells out rather than reading .git: a repository's real state involves
// the index, submodules, and worktrees, and reimplementing that to save one
// process is how a tool starts lying about whether your work is committed.
type gitExt struct{}

func init() { Register(gitExt{}) }

func (gitExt) Name() string  { return "git" }
func (gitExt) About() string { return "/git status · diff · log, without spending a turn" }

func (g gitExt) Commands() []Command {
	return []Command{{
		Name:  "git",
		About: "Repository state — /git [status|diff|log|branch]",
		Run: func(ctx context.Context, cwd, rest string) (string, string, error) {
			verb := strings.TrimSpace(rest)
			if verb == "" {
				verb = "status"
			}
			args, ok := gitArgs[verb]
			if !ok {
				return "", "", fmt.Errorf("git: %s — try status, diff, log, or branch", verb)
			}
			cmd := exec.CommandContext(ctx, "git", args...)
			cmd.Dir = cwd
			out, err := cmd.CombinedOutput()
			text := strings.TrimRight(string(out), "\n")
			if err != nil && text == "" {
				return "", "", fmt.Errorf("git %s: %w", verb, err)
			}
			if text == "" {
				text = "(nothing)"
			}
			return text, "", nil
		},
	}}
}

// gitArgs is the allowed set. A fixed table rather than passing the user's
// words to git: this command exists to answer three questions, and forwarding
// arbitrary arguments turns it into a second, worse shell.
var gitArgs = map[string][]string{
	"status": {"status", "--short", "--branch"},
	"diff":   {"diff", "--stat"},
	"log":    {"log", "--oneline", "-15"},
	"branch": {"branch", "--show-current"},
}
