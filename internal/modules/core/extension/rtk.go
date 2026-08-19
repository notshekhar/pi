package extension

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// RTK (Rust Token Killer, github.com/rtk-ai/rtk) as an extension.
//
// RTK is an external CLI that compresses verbose command output — git, npm,
// cargo, test runners — for 60-90%% fewer tokens. The integration is command
// REWRITING: `git status` becomes `rtk git status` before it runs. Upstream
// does this with a PreToolUse hook; here the seam is CallRewriter, which is
// the same thing without a subprocess round trip per call.
//
// `rtk rewrite "<cmd>"` is the single source of truth, the same as the
// official hook — the command table is rtk's business, and duplicating it
// here would mean shipping a copy that goes stale.
//
// Exit protocol:
//
//	0 + stdout  a rewrite exists → use it (skipped if unchanged or already rtk)
//	1           no equivalent    → leave unchanged
//	2           deny rule        → leave unchanged; the permission policy handles it
//	3 + stdout  ask rule         → rewrite; the approval flow still prompts
//
// Without the binary the extension is a silent no-op, so enabling it on a
// machine that does not have rtk never breaks bash.

type rtk struct {
	store Store
	// available is looked up once. Probing PATH on every bash call would
	// spend a process to learn something that does not change mid-session.
	once      sync.Once
	installed bool
}

func init() { Register(&rtk{}) }

func (*rtk) Name() string { return "rtk" }
func (*rtk) About() string {
	return "Rewrites bash commands to compress output 60-90% (needs the rtk binary). /rtk"
}
func (r *rtk) UseStore(s Store) { r.store = s }

// rewritingOn is the extension's own switch, separate from being enabled at
// all: `/rtk-toggle` turns rewriting off without unloading the commands that
// report why.
func (r *rtk) rewritingOn() bool {
	return r.store == nil || r.store.Get("enabled", "on") != "off"
}

// available reports whether the rtk binary can be run.
func (r *rtk) available() bool {
	r.once.Do(func() {
		path, err := exec.LookPath("rtk")
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// Run, not merely found: a stale symlink or a wrapper script that
		// exits non-zero is worse than an absent binary, because every bash
		// call would then pay for a failed process.
		r.installed = exec.CommandContext(ctx, path, "--version").Run() == nil
	})
	return r.installed
}

func (r *rtk) Status() string {
	switch {
	case !r.available():
		return "no binary"
	case r.rewritingOn():
		return "on"
	}
	return "off"
}

func (r *rtk) Commands() []Command {
	return []Command{
		{
			Name:  "rtk",
			About: "RTK token optimizer status (rewrites bash commands to compress output)",
			Run: func(context.Context, string, string) (string, string, error) {
				if !r.available() {
					return "rtk binary not found on PATH. Install it: https://github.com/rtk-ai/rtk — then /reload.", "", nil
				}
				if r.rewritingOn() {
					return "rtk rewriting: on. Toggle: /rtk-toggle", "", nil
				}
				return "rtk rewriting: off. Toggle: /rtk-toggle", "", nil
			},
		},
		{
			Name:  "rtk-toggle",
			About: "Turn RTK command rewriting on/off",
			Run: func(context.Context, string, string) (string, string, error) {
				on := !r.rewritingOn()
				if r.store != nil {
					value := "off"
					if on {
						value = "on"
					}
					if err := r.store.Set("enabled", value); err != nil {
						return "", "", err
					}
				}
				if on {
					return "rtk rewriting on.", "", nil
				}
				return "rtk rewriting off.", "", nil
			},
		},
	}
}

// RewriteCall turns a bash command into its rtk equivalent.
//
// Silently — the command runs as `rtk …` with no note in the transcript. A
// note per call would be noise on something that changes nothing about what
// the command means.
func (r *rtk) RewriteCall(tool string, args map[string]any) map[string]any {
	if tool != "bash" || !r.rewritingOn() || !r.available() {
		return nil
	}
	command, _ := args["command"].(string)
	if command == "" {
		return nil
	}
	rewritten := r.rewrite(command)
	if rewritten == "" {
		return nil
	}
	// Copied, not mutated: the caller's map is the decoded original, and a
	// rewriter that edited it in place would change what a LATER rewriter —
	// or the audit of what was asked — sees.
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	out["command"] = rewritten
	return out
}

// rewrite asks rtk for an equivalent, or "" to leave the command alone.
func (r *rtk) rewrite(command string) string {
	// Heredocs confuse line-oriented rewriting; rtk skips them and so does
	// this.
	if strings.Contains(command, "<<") {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "rtk", "rewrite", command).Output()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !asExit(err, &exit) {
			return ""
		}
		code = exit.ExitCode()
	}
	// 1 = no match, 2 = deny, anything unexpected → leave it alone.
	if code != 0 && code != 3 {
		return ""
	}
	rewritten := strings.TrimSpace(string(out))
	if rewritten == "" || rewritten == command {
		return ""
	}
	return rewritten
}

func asExit(err error, out **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*out = e
	}
	return ok
}
