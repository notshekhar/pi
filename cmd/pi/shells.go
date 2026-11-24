package main

import (
	"strconv"
	"strings"

	"github.com/notshekhar/pi/internal/core/shells"
	"github.com/notshekhar/pi/internal/tui"
)

// /shells — loop's shells-handlers.ts: the user's half of background shells.
// The agent reaches them through the `shells` tool; this is the same registry
// from the other side, so a process the agent started (or forgot) can always
// be inspected and stopped by hand.
//
// `/shells` opens a picker: choose a shell, then stop it or read its output ·
// `/shells run <cmd>` starts one · `/shells kill <id>` or `/shells kill all`
// stops them without a menu · `/shells <id>` prints the tail of one log.
//
// `run` is the user's own hand on the keyboard, so it skips the denylist and
// the approval prompt.

// stopAllShells is the picker's "stop every running shell" row — not a shell
// id, so it cannot collide with one.
const stopAllShells = "\x00stop-all"

func (t *repl) shellsCmd(args string) {
	trimmed := strings.TrimSpace(args)
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		t.pickShell()
		return
	}
	switch verb := fields[0]; verb {
	case "run":
		t.runShell(strings.TrimSpace(trimmed[len(verb):]))
	case "kill", "stop":
		if len(fields) < 2 {
			t.fail("Usage: /shells kill <id> · /shells kill all")
		} else if fields[1] == "all" {
			t.stopAllShells()
		} else {
			t.stopShell(fields[1])
		}
	default:
		t.showShellOutput(verb)
	}
}

// shellPickerItems is one row per shell, running ones first, plus "stop all"
// on top when more than one is running.
func shellPickerItems(list []shells.Info) []tui.Item {
	var running, finished []tui.Item
	for _, s := range list {
		item := tui.Item{Value: s.ID, Label: s.ID + "  " + firstLine(s.Command), Description: shells.StatusText(s)}
		if s.Status == "running" {
			running = append(running, item)
		} else {
			finished = append(finished, item)
		}
	}
	items := append(running, finished...)
	if len(running) > 1 {
		all := tui.Item{Value: stopAllShells, Label: "Stop all " + strconv.Itoa(len(running)) + " running shells"}
		items = append([]tui.Item{all}, items...)
	}
	return items
}

// pickShell is `/shells` alone: the picker, the visible way to stop something.
func (t *repl) pickShell() {
	list := shells.List(t.run.Session.ID)
	if len(list) == 0 {
		t.dim("No background shells. /shells run <cmd> starts one, or bash run_in_background does.")
		return
	}
	t.pickPlain("Background shells — pick one to stop or read", shellPickerItems(list), 0, "", func(it tui.Item) {
		if it.Value == stopAllShells {
			t.stopAllShells()
			return
		}
		for _, s := range list {
			if s.ID == it.Value {
				t.actOnShell(s)
			}
		}
	})
}

// actOnShell asks what to do with the shell picked: stop it (when it runs) or read it.
func (t *repl) actOnShell(s shells.Info) {
	var actions []tui.Item
	if s.Status == "running" {
		actions = append(actions, tui.Item{Value: "stop", Label: "Stop it"})
	}
	actions = append(actions, tui.Item{Value: "output", Label: "Show its output"})
	choice := t.choose(s.ID+" · "+firstLine(s.Command), actions...)
	switch {
	case choice == nil:
	case choice.Value == "stop":
		t.stopShell(s.ID)
	case choice.Value == "output":
		t.showShellOutput(s.ID)
	}
}

func (t *repl) say(lines ...string) {
	t.do(func() { t.app.Print(strings.Split(strings.Join(lines, "\n"), "\n")...) })
}

func (t *repl) runShell(command string) {
	sid := t.run.Session.ID
	if command == "" {
		t.fail("Usage: /shells run <command>")
		return
	}
	if shells.RunningCount(sid) >= shells.MaxBackground {
		t.fail("Already running %d shells — stop one first.", shells.MaxBackground)
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
	t.say(info.ID + " started (pid " + pid + ") · " + command)
}

func (t *repl) stopAllShells() {
	switch n := shells.KillSession(t.run.Session.ID); n {
	case 0:
		t.dim("Nothing was running.")
	case 1:
		t.say("Stopped 1 shell.")
	default:
		t.say("Stopped " + strconv.Itoa(n) + " shells.")
	}
}

func (t *repl) stopShell(id string) {
	info, ok := shells.Kill(t.run.Session.ID, id)
	if !ok {
		t.fail("No shell %s.", id)
		return
	}
	t.say(info.ID + " stopped.")
}

// showShellOutput prints a shell's recent output. Reading along with the
// agent must not consume output it has not seen.
func (t *repl) showShellOutput(id string) {
	r, ok := shells.Read(t.run.Session.ID, id, "", true)
	if !ok {
		t.fail("No shell %s. Try /shells.", id)
		return
	}
	th := t.app.Theme()
	body := strings.TrimSpace(r.Text)
	if body == "" {
		body = th.Fg(tui.SlotDim, "(no output yet)")
	}
	t.say(th.Fg(tui.SlotAccent, r.Info.ID+" — "+shells.StatusText(r.Info)), th.Fg(tui.SlotDim, firstLine(r.Info.Command)), "", body)
}
