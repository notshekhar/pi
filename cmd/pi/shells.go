package main

import (
	"strconv"
	"strings"

	"github.com/notshekhar/pi/internal/core/shells"
	"github.com/notshekhar/pi/internal/tui"
)

// /shells — loop's shells-handlers.ts: the user's half of background shells.
// The agent reaches them through the `shells` tool; this is the same registry
// from the other side. `run` is the user's own hand on the keyboard, so it
// skips the denylist and the approval prompt.

func (t *repl) shellsCmd(args string) {
	fields := strings.Fields(args)
	sid := t.run.Session.ID
	th := t.app.Theme()
	heading := func(s string) string { return th.Fg(tui.SlotAccent, s) }
	say := func(lines ...string) {
		t.do(func() { t.app.Print(strings.Split(strings.Join(lines, "\n"), "\n")...) })
	}

	if len(fields) == 0 {
		list := shells.List(sid)
		if len(list) == 0 {
			t.dim("No background shells. /shells run <cmd> starts one, or bash run_in_background does.")
			return
		}
		lines := []string{heading("Background shells")}
		for _, s := range list {
			lines = append(lines, "  "+s.ID+"  "+shells.StatusText(s)+"  "+strings.Split(s.Command, "\n")[0])
			if s.LogPath != "" {
				lines = append(lines, th.Fg(tui.SlotDim, "        "+s.LogPath))
			}
		}
		lines = append(lines, th.Fg(tui.SlotDim, "  /shells run <cmd> · /shells kill <id> · /shells kill all · /shells <id> for its output"))
		say(lines...)
		return
	}
	verb := fields[0]
	switch verb {
	case "run":
		command := strings.TrimSpace(strings.TrimSpace(args)[len(verb):])
		if command == "" {
			t.fail("Usage: /shells run <command>")
			return
		}
		if shells.RunningCount(sid) >= shells.MaxBackground {
			t.fail("Already running %d shells — kill one first.", shells.MaxBackground)
			return
		}
		info, err := shells.Start(shells.StartOptions{SessionID: sid, Command: command, CWD: t.cfg.CWD})
		if err != nil {
			t.fail("Could not start: %s", err)
			return
		}
		pid := "?"
		if info.PID > 0 {
			pid = strconv.Itoa(info.PID)
		}
		say(info.ID + " started (pid " + pid + ") · " + command)
	case "kill":
		if len(fields) < 2 {
			t.fail("Usage: /shells kill <id> · /shells kill all")
			return
		}
		if fields[1] == "all" {
			n := shells.KillSession(sid)
			if n == 0 {
				t.dim("Nothing was running.")
			} else if n == 1 {
				say("Killed 1 shell.")
			} else {
				say("Killed " + strconv.Itoa(n) + " shells.")
			}
			return
		}
		info, ok := shells.Kill(sid, fields[1])
		if !ok {
			t.fail("No shell %s.", fields[1])
			return
		}
		say(info.ID + " killed.")
	default:
		// Reading along with the agent must not consume output it has not seen.
		r, ok := shells.Read(sid, verb, "", true)
		if !ok {
			t.fail("No shell %s. Try /shells.", verb)
			return
		}
		body := strings.TrimSpace(r.Text)
		if body == "" {
			body = th.Fg(tui.SlotDim, "(no output yet)")
		}
		say(heading(r.Info.ID+" — "+shells.StatusText(r.Info)), th.Fg(tui.SlotDim, strings.Split(r.Info.Command, "\n")[0]), "", body)
	}
}
