package main

import (
	"context"
	"strings"

	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/permissions"
	"github.com/notshekhar/pi/internal/tui"
)

// loadPolicy builds the run's policy: the shipped defaults plus whatever the
// user has stored.
//
// The defaults are appended to, never replaced. The always-on deny list is
// what stops a stored `allow bash` from opening the door to `rm -rf /` — and
// since the strictest rule wins regardless of order, a user rule cannot
// weaken one of them even by trying.
func (t *repl) loadPolicy() permissions.Policy { return policyFor(t.cfg.CWD) }

// policyFor is the policy for a working directory, from settings alone — the
// TUI and the RPC server build theirs the same way.
func policyFor(cwd string) permissions.Policy {
	policy := permissions.Default(cwd)
	settings := config.LoadSettings()
	// bashApprove asks before EVERY bash command. It is prepended as a rule
	// rather than special-cased at the call site so that a more specific
	// allow rule can still exempt a command — an approval prompt you cannot
	// escape is one people learn to answer without reading.
	if settings.BashApproveOn() {
		if rule, err := permissions.ParseRule("ask bash"); err == nil {
			policy.Rules = append(policy.Rules, rule)
		}
	}
	for _, pattern := range settings.BashDeny {
		policy.Rules = append(policy.Rules, permissions.Rule{Tool: "bash", Pattern: pattern + "*", Mode: permissions.Deny,
			Reason: "bashDeny `" + pattern + "`"})
	}
	if p := settings.Permissions; p != nil {
		policy.Rules = append(policy.Rules, permissions.FromSetting(p.Allow, p.Ask, p.Deny)...)
	}
	return policy
}

// permissions shows the active rules, or adds one.
func (t *repl) permissions(rest string) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		t.showPermissions()
		t.pickPermission()
		return
	}
	if rest == "reset" {
		if err := config.Update(func(s *config.Settings) { s.Permissions = nil }); err != nil {
			t.fail("permissions: %s", err)
			return
		}
		t.run.Permissions = t.loadPolicy()
		t.dim("permissions reset to defaults")
		return
	}

	mode, rule, _ := strings.Cut(rest, " ")
	if _, ok := permissions.ParseMode(mode); !ok {
		t.dim("syntax: /permissions <allow|ask|deny> <Tool>[(<pattern>)]")
		return
	}
	if _, ok := permissions.ParseRuleString(permissions.Mode(mode), rule); !ok {
		t.fail("unrecognized rule: %s", rule)
		return
	}
	if err := config.AddPermission(mode, strings.TrimSpace(rule)); err != nil {
		t.fail("permissions: %s", err)
		return
	}
	t.run.Permissions = t.loadPolicy()
	t.dim("added: %s %s", mode, rule)
}

func (t *repl) showPermissions() {
	policy := t.run.Permissions
	stored := config.LoadSettings().Permissions.Entries()

	t.do(func() {
		th := t.app.Theme()
		slot := func(m permissions.Mode) tui.Slot {
			switch m {
			case permissions.Deny:
				return tui.SlotError
			case permissions.Ask:
				return tui.SlotWarning
			}
			return tui.SlotSuccess
		}

		lines := []string{
			th.Fg(tui.SlotMuted, "default   ") + th.Fg(slot(policy.Default), string(policy.Default)),
			th.Fg(tui.SlotMuted, "confined  ") + th.Fg(tui.SlotText, policy.CWD),
			"",
			th.Fg(tui.SlotDim, "always on"),
		}
		for _, r := range policy.Rules[:max(0, len(policy.Rules)-len(permissions.FromSetting(nil, nil, nil))-countStored(stored))] {
			lines = append(lines, "  "+th.Fg(slot(r.Mode), string(r.Mode))+" "+
				th.Fg(tui.SlotMuted, r.Tool+"("+r.Pattern+")"))
		}
		if len(stored) > 0 {
			lines = append(lines, "", th.Fg(tui.SlotDim, "yours"))
			for _, s := range stored {
				lines = append(lines, "  "+th.Fg(tui.SlotText, s))
			}
		}
		lines = append(lines, "",
			th.Fg(tui.SlotDim, "/permissions <allow|ask|deny> <tool>[(<glob>)] · /permissions reset"))
		t.app.Print(lines...)
	})
}

// setPlanMode toggles read-only planning.
//
// Not persisted: a mode that survived a restart would silently refuse the
// next session's edits, and the user would have no idea why. It is a stance
// for the current conversation, not a preference.
func (t *repl) setPlanMode(rest string) {
	if t.busy() {
		return
	}
	// `/plan <task>` enters plan mode AND submits the task in one step, which
	// is what the command's own help has always promised. Treating anything
	// that is not on/off as a usage error made the documented form an error
	// message.
	on := !t.run.Planning
	var task string
	switch arg := strings.TrimSpace(rest); strings.ToLower(arg) {
	case "on":
		on = true
	case "off":
		on = false
	case "":
	default:
		on, task = true, arg
	}

	t.run.Planning = on
	// A gate set here is not one the agent cycle may clear — see planViaAgent.
	t.planViaAgent = false
	t.do(func() { t.app.SetPlanning(on) })
	if on {
		t.dim("plan mode — write and edit are refused, bash asks; /plan off to execute")
		if task != "" {
			t.submit(context.Background(), task)
		}
		return
	}
	t.dim("plan mode off")
}

// countStored is how many policy rules the stored entries expand to.
func countStored(entries []string) int {
	n := 0
	for _, e := range entries {
		mode, rule, _ := strings.Cut(e, " ")
		rules, _ := permissions.ParseRuleString(permissions.Mode(mode), rule)
		n += len(rules)
	}
	return n
}
