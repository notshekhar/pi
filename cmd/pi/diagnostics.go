package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/core/catalog"
	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/hooks"
	"github.com/notshekhar/pi/internal/modules/core/memory"
	"github.com/notshekhar/pi/internal/modules/core/skills"
	"github.com/notshekhar/pi/internal/modules/tui"
)

// `/recap`, `/thinking`, `/alias`, `/doctor` — the small surface that makes a
// long session navigable.

// recap reports what the session has done, counted from its own history.
func (t *repl) recap() {
	if t.busy() {
		return
	}
	r := t.run.Session.Recap(t.cfg.CWD)

	t.app.Do(func() {
		th := t.app.Theme()
		if r.Turns == 0 && len(r.Tools) == 0 {
			t.app.Print(th.Fg(tui.SlotDim, "nothing to recap yet"))
			return
		}

		lines := []string{
			th.Fg(tui.SlotMuted, "turns    ") + th.Fg(tui.SlotText, fmt.Sprint(r.Turns)),
		}
		if len(r.Tools) > 0 {
			names := make([]string, 0, len(r.Tools))
			for name := range r.Tools {
				names = append(names, name)
			}
			sort.Slice(names, func(i, j int) bool { return r.Tools[names[i]] > r.Tools[names[j]] })
			parts := make([]string, 0, len(names))
			for _, name := range names {
				parts = append(parts, fmt.Sprintf("%s ×%d", name, r.Tools[name]))
			}
			lines = append(lines, th.Fg(tui.SlotMuted, "tools    ")+th.Fg(tui.SlotText, strings.Join(parts, "  ")))
		}

		section := func(label string, items []string, slot tui.Slot) {
			if len(items) == 0 {
				return
			}
			lines = append(lines, "", th.Fg(tui.SlotDim, label))
			for _, item := range items {
				lines = append(lines, "  "+th.Fg(slot, item))
			}
		}
		section("changed", r.Changed, tui.SlotSuccess)
		section("read", r.Read, tui.SlotMuted)
		// Only the tail: a long session's early commands are rarely what you
		// are trying to remember.
		if n := len(r.Commands); n > 0 {
			tail := r.Commands
			if n > 8 {
				tail = tail[n-8:]
			}
			section(fmt.Sprintf("commands (last %d of %d)", len(tail), n), tail, tui.SlotMuted)
		}
		t.app.Print(lines...)
	})
}

// thinkingLevelNames are the levels as a user names them, in order.
//
// The first one is "off", not "none": "none" is the value that goes on the
// wire, and the two are kept apart deliberately — a user turns thinking OFF,
// while the provider is told the effort is NONE. thinkingWire translates.
var thinkingLevelNames = []string{"off", "minimal", "low", "medium", "high", "xhigh"}

// thinkingWire maps a UI level onto the provider's effort value.
func thinkingWire(level string) provider.ReasoningEffort {
	if level == "off" {
		return provider.ReasoningNone
	}
	return provider.ReasoningEffort(level)
}

// thinkingName maps a stored effort back onto the name the UI uses, so a
// config written before the rename still shows the right row as current.
func thinkingName(effort provider.ReasoningEffort) string {
	if effort == provider.ReasoningNone {
		return "off"
	}
	return string(effort)
}

// setThinking changes the reasoning effort for the rest of the session.
func (t *repl) setThinking(rest string) {
	// A model that cannot reason has no level to set, and offering the picker
	// anyway ends in a setting that silently does nothing.
	if !t.modelReasons() {
		t.dim("current model does not support thinking")
		return
	}
	level := strings.ToLower(strings.TrimSpace(rest))
	if level == "" {
		// No argument means "show me the options" — naming them in a line of
		// prose makes the user retype what they were just shown.
		t.pickThinking()
		return
	}
	// "none" is still accepted: it is what older configs and the provider
	// layer call the same level, and rejecting it would break a habit for
	// nothing.
	if level == "none" {
		level = "off"
	}
	if !contains(thinkingLevelNames, level) {
		t.dim("unknown thinking level: %s. options: %s", level, strings.Join(thinkingLevelNames, ", "))
		return
	}

	t.cfg.Reasoning = thinkingWire(level)
	t.run.Config.Reasoning = t.cfg.Reasoning
	stored := string(t.cfg.Reasoning)
	if err := config.Update(func(s *config.Settings) { s.Reasoning = stored }); err != nil {
		t.dim("thinking → %s (not saved: %s)", level, err)
		return
	}
	t.app.Do(func() { t.app.SetThinking(level) })
	t.dim("thinking → %s", level)
}

// modelReasons reports whether the session's model reasons at all. A model
// the catalog does not know (a custom endpoint) is assumed to reason, because
// refusing to set a level the model does support is the worse mistake.
func (t *repl) modelReasons() bool {
	info, ok := catalog.Lookup(t.cfg.Provider, t.cfg.ModelID)
	return !ok || info.Reasoning
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// alias defines a shorthand for a command line.
//
// Expansion is a prefix substitution, so `/t high` becomes `/thinking high` —
// an alias that could not take arguments would only be worth having for the
// commands that need none.
func (t *repl) alias(rest string) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		t.pickAlias()
		return
	}
	first, expansion, hasExpansion := strings.Cut(rest, " ")
	expansion = strings.TrimSpace(expansion)

	// Removal is a verb, not the absence of an expansion. A bare name used to
	// delete the alias it named, which meant `/alias m` — the natural way to
	// ask "what is /m bound to?" — silently destroyed it.
	switch first {
	case "rm", "remove", "delete":
		name := strings.TrimPrefix(expansion, "/")
		if name == "" {
			t.fail("usage: /alias rm <name>")
			return
		}
		if _, ok := config.LoadSettings().Aliases[name]; !ok {
			t.fail("no such alias: /%s", name)
			return
		}
		if err := config.Update(func(s *config.Settings) { delete(s.Aliases, name) }); err != nil {
			t.fail("alias: %s", err)
			return
		}
		t.dim("removed alias /%s", name)
		return
	}

	name := strings.TrimPrefix(strings.TrimSpace(first), "/")
	if !hasExpansion || expansion == "" {
		if current, ok := config.LoadSettings().Aliases[name]; ok {
			t.dim("/%s → %s", name, current)
			return
		}
		t.fail("usage: /alias <name> </cmd args…>  e.g. /alias m /model")
		return
	}
	if !validAliasName(name) {
		t.fail("invalid alias name: %s", name)
		return
	}
	if !strings.HasPrefix(expansion, "/") {
		t.fail("expansion must be a /command, got: %s", expansion)
		return
	}
	// An alias may be redefined, but it may never shadow a real command —
	// including `/alias` itself, which would make the mistake unfixable.
	if _, existing := config.LoadSettings().Aliases[name]; !existing {
		if name == "alias" || lookupCommand(name) != nil {
			t.fail("/%s is an existing command — pick another name", name)
			return
		}
	}
	if err := config.Update(func(s *config.Settings) {
		if s.Aliases == nil {
			s.Aliases = map[string]string{}
		}
		s.Aliases[name] = expansion
	}); err != nil {
		t.fail("alias: %s", err)
		return
	}
	t.dim("/%s → %s", name, expansion)
}

// validAliasName is loop's rule: letters, digits, underscore, colon, hyphen.
// The colon is there so a skill command (`/skill:foo`) can be aliased.
func validAliasName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == ':' || r == '-':
		default:
			return false
		}
	}
	return true
}

func (t *repl) showAliases() {
	aliases := config.LoadSettings().Aliases
	t.app.Do(func() {
		th := t.app.Theme()
		if len(aliases) == 0 {
			t.app.Print(
				th.Fg(tui.SlotDim, "no aliases"),
				th.Fg(tui.SlotDim, "/alias <name> <command> to add one, /alias <name> to remove it"))
			return
		}
		names := make([]string, 0, len(aliases))
		for name := range aliases {
			names = append(names, name)
		}
		sort.Strings(names)
		lines := make([]string, 0, len(names))
		for _, name := range names {
			lines = append(lines, th.Fg(tui.SlotAccent, padRight("/"+name, 14))+
				th.Fg(tui.SlotMuted, aliases[name]))
		}
		t.app.Print(lines...)
	})
}

// expandAlias rewrites a command line through the alias table, once.
//
// Once, not repeatedly: an alias chain is a loop waiting to happen, and
// nobody has ever needed one.
func expandAlias(line string) string {
	name, rest, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	expansion, ok := config.LoadSettings().Aliases[name]
	if !ok {
		return line
	}
	if rest == "" {
		return expansion
	}
	return expansion + " " + rest
}

// doctor reports whether the environment is actually set up.
func (t *repl) doctor() {
	settingsPath, _ := config.SettingsPath()
	_, settingsErr := config.LoadSettings().HookConfig()
	memDir := ""
	if store, err := memory.Open(t.cfg.CWD); err == nil {
		memDir = store.Dir
	}
	skillCount := len(skills.Load(t.cfg.CWD))
	caps := tui.TerminalCaps()

	t.app.Do(func() {
		th := t.app.Theme()
		ok := func(good bool, yes, no string) string {
			if good {
				return th.Fg(tui.SlotSuccess, yes)
			}
			return th.Fg(tui.SlotWarning, no)
		}
		row := func(label, value string) string {
			return th.Fg(tui.SlotMuted, padRight(label, 14)) + value
		}

		lines := []string{
			row("go", th.Fg(tui.SlotText, runtime.Version()+" "+runtime.GOOS+"/"+runtime.GOARCH)),
			row("cwd", th.Fg(tui.SlotText, t.cfg.CWD)),
			row("model", th.Fg(tui.SlotText, t.cfg.FullID())),
			"",
			row("settings", th.Fg(tui.SlotText, settingsPath)),
		}
		if settingsErr != nil {
			lines = append(lines, row("", th.Fg(tui.SlotError, settingsErr.Error())))
		}
		lines = append(lines,
			row("memory", th.Fg(tui.SlotText, memDir)),
			row("skills", th.Fg(tui.SlotText, fmt.Sprintf("%d installed", skillCount))),
			row("hooks", th.Fg(tui.SlotText, hooks.EventNames())),
			"",
			row("truecolor", ok(caps.TrueColor, "yes", "no — colours will be approximated")),
			row("hyperlinks", ok(caps.Hyperlinks, "yes", "no — urls print inline")),
			row("clipboard", ok(clipboardAvailable(), "yes", "no — /copy will fail")),
			"",
			th.Fg(tui.SlotDim, "providers"))

		// Which providers can actually be used right now, which is the
		// question /doctor exists to answer.
		for _, prov := range catalog.Providers {
			state := ok(prov.Keyless || config.APIKey(prov.ID) != "", "ready", "no key")
			if prov.Keyless {
				state = th.Fg(tui.SlotSuccess, "keyless")
			}
			lines = append(lines, "  "+th.Fg(tui.SlotAccent, padRight(prov.ID, 16))+state)
		}
		t.app.Print(lines...)
	})
}

// clipboardAvailable reports whether /copy has a tool to pipe through.
func clipboardAvailable() bool {
	for _, name := range clipboardCandidates() {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	return false
}

func clipboardCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"pbcopy"}
	case "windows":
		return []string{"clip"}
	default:
		return []string{"wl-copy", "xclip"}
	}
}
