package main

import (
	"context"
	"strings"

	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/hooks"
	"github.com/notshekhar/pi/internal/modules/core/skills"
	"github.com/notshekhar/pi/internal/modules/tui"
)

// Hooks and skills, from the app's side.

// fireHook runs the hooks bound to an event and reports anything they said.
//
// Called from the turn goroutine, never the render loop: a hook is a
// subprocess and blocking the renderer on one would freeze the screen for as
// long as it takes.
//
// The Outcome is RETURNED, not just printed: a PreToolUse hook that exits 2
// has refused the call, and a caller that ignored that would leave the
// feature looking like it worked while enforcing nothing.
func (t *repl) fireHook(ctx context.Context, hc hooks.Context) hooks.Outcome {
	cfg, err := config.LoadSettings().HookConfig()
	if err != nil {
		t.fail("%s", err)
		return hooks.Outcome{}
	}
	if len(cfg[hc.Event]) == 0 {
		return hooks.Outcome{}
	}
	hc.CWD = t.cfg.CWD
	hc.SessionID = t.run.Session.ID

	outcome := hooks.Run(ctx, cfg, hc)
	if len(outcome.Messages) == 0 && !outcome.Block {
		return outcome // a silent success is the normal case
	}
	event, messages, blocked, reason := hc.Event, outcome.Messages, outcome.Block, outcome.Reason
	t.app.Do(func() {
		th := t.app.Theme()
		if blocked {
			t.app.Print(th.Fg(tui.SlotError, "hook "+string(event)+" blocked: "+reason))
		}
		for _, m := range messages {
			for _, line := range strings.Split(m, "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				t.app.Print(th.Fg(tui.SlotDim, "  "+line))
			}
		}
	})
	return outcome
}

// skillsCmd lists the available skills, or shows one.
func (t *repl) skillsCmd(rest string) {
	name := strings.TrimSpace(rest)
	if name != "" {
		skill, err := skills.Find(t.cfg.CWD, name)
		if err != nil {
			t.fail("%s", err)
			return
		}
		t.app.Do(func() {
			th := t.app.Theme()
			t.app.Print(th.Fg(tui.SlotAccent, th.Bold(skill.Name)) + "  " +
				th.Fg(tui.SlotDim, skill.Description))
			t.app.AssistantDelta(skill.Body)
			t.app.AssistantEnd()
		})
		return
	}

	all := skills.Load(t.cfg.CWD)
	t.app.Do(func() {
		th := t.app.Theme()
		if len(all) == 0 {
			userDir, _ := skills.UserDir()
			t.app.Print(
				th.Fg(tui.SlotDim, "no skills installed"),
				th.Fg(tui.SlotDim, "a skill is a directory with a "+skills.FileName+" in:"),
				th.Fg(tui.SlotDim, "  "+userDir),
				th.Fg(tui.SlotDim, "  "+skills.ProjectDir(t.cfg.CWD)+"  (project, wins on a name clash)"))
			return
		}
		lines := make([]string, 0, len(all)+2)
		for _, s := range all {
			origin := ""
			if s.Project {
				origin = " (project)"
			}
			lines = append(lines,
				th.Fg(tui.SlotAccent, padRight(s.Name, 20))+
					th.Fg(tui.SlotMuted, s.Description)+
					th.Fg(tui.SlotDim, origin))
		}
		lines = append(lines, "", th.Fg(tui.SlotDim, "/skills <name> to read one"))
		t.app.Print(lines...)
	})
}
