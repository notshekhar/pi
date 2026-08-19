package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/modules/core/catalog"
	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/hooks"
	"github.com/notshekhar/pi/internal/modules/core/permissions"
	"github.com/notshekhar/pi/internal/modules/core/skills"
	"github.com/notshekhar/pi/internal/modules/tui"
)

// Pickers: what a command does when you give it no argument.
//
// loop's rule is uniform — no argument means "show me the options and let me
// choose". pi-agent printed a line of prose naming the options instead, which
// is a worse answer to the same question: it makes the user retype something
// they were just shown, and a typo puts them back where they started. The
// text form survives as the ARGUMENT form (`/thinking high` still works), so
// scripts and habits are unaffected.

// pick runs a picker off the render loop and hands the choice to f.
//
// Pick BLOCKS on the render goroutine, so every caller must be off it; this
// wrapper exists so that fact lives in one place rather than in a dozen
// hand-written `go func()`s that each have to remember it.
func (t *repl) pick(title string, items []tui.Item, initial int, current string, f func(tui.Item)) {
	if len(items) == 0 {
		return
	}
	if t.inPanel.Load() {
		if choice := t.app.Pick(title, items, initial, current); choice != nil {
			f(*choice)
		}
		return
	}
	go func() {
		t.inPanel.Store(true)
		defer t.inPanel.Store(false)
		if choice := t.app.Pick(title, items, initial, current); choice != nil {
			f(*choice)
		}
	}()
}

// pickPlain is the same, without a filter box — for a list short enough that
// every option is already on screen. See tui.ModalSelect.
func (t *repl) pickPlain(title string, items []tui.Item, initial int, current string, f func(tui.Item)) {
	if len(items) == 0 {
		return
	}
	if t.inPanel.Load() {
		if choice := t.app.Select(title, items, initial, current); choice != nil {
			f(*choice)
		}
		return
	}
	go func() {
		t.inPanel.Store(true)
		defer t.inPanel.Store(false)
		if choice := t.app.Select(title, items, initial, current); choice != nil {
			f(*choice)
		}
	}()
}

// manage runs loop's manager loop: build the rows, pick one, act on it, and
// go back to the list.
//
// The loop is the whole point, and it is what pi-agent's panels were missing.
// A manager that closes after one action turns "add three rules" into typing
// the command three times, and the state you are editing disappears between
// each one — so you cannot see what you just did. Reopening on the row you
// last touched (`last`) is part of the same property: a list that snaps back
// to the top after every edit makes a run of edits an exercise in re-finding
// your place.
//
// build is called fresh each pass so the rows reflect the edit just made.
// Returning no rows ends the loop — a manager with nothing to manage.
// closePanel / keepPanel are what an action returns to say whether the
// manager should reopen its list.
const (
	closePanel = false
	keepPanel  = true
)

func (t *repl) manage(build func() (string, []tui.Item), act func(tui.Item) bool) {
	// A panel opened FROM a panel (a /settings row that opens the denylist)
	// runs inline on the goroutine that is already waiting for it. Spawning a
	// second goroutine there would leave two loops both trying to own the
	// keyboard, and the outer one would repaint its list over the inner one's.
	if t.inPanel.Load() {
		t.managePanel(build, act)
		return
	}
	go func() {
		t.inPanel.Store(true)
		defer t.inPanel.Store(false)
		t.managePanel(build, act)
	}()
}

// managePanel is the loop itself, run on whichever goroutine already owns the
// panel.
// A manager LOOPS by default — list, act, back to the list — because that is
// what editing a list is: adding three rules should not mean typing the
// command three times.
//
// A SELECTION is not an edit. Picking a model is the terminal action, and
// reopening the list afterwards leaves the user looking at a menu they have
// finished with, hunting for the key that dismisses it. Those actions return
// closePanel.
func (t *repl) managePanel(build func() (string, []tui.Item), act func(tui.Item) bool) {
	last := 0
	for first := true; ; first = false {
		title, items := build()
		if len(items) == 0 {
			// Never vanish. A panel that opened nothing and said nothing is
			// indistinguishable from a broken command — which is exactly how
			// `/background` read on a fresh session before it grew an add
			// row. Only on the FIRST pass: a manager whose last entry was
			// just deleted should close quietly, not accuse itself.
			if first {
				t.dim("%s — nothing to show", title)
			}
			return
		}
		if last >= len(items) {
			last = len(items) - 1
		}
		choice := t.app.Pick(title, items, last, "")
		if choice == nil {
			return
		}
		last = indexOf(items, choice.Value)
		if !act(*choice) {
			return
		}
	}
}

// choose is a short menu with no filter box — loop's selectOnce. Safe to call
// from inside manage's goroutine, which is where every use of it is.
func (t *repl) choose(title string, items ...tui.Item) *tui.Item {
	return t.app.Select(title, items, 0, "")
}

// ask puts a one-line question. Blank is a cancel, as it is in loop.
func (t *repl) ask(label, initial string) string {
	return t.app.Prompt(label, initial)
}

// confirmRemove is the two-row "remove / cancel" menu loop puts in front of
// every destructive row, so a stray Enter on a list cannot delete anything.
func (t *repl) confirmRemove(title, what string) bool {
	pick := t.choose(title,
		tui.Item{Value: "remove", Label: "remove", Description: what},
		tui.Item{Value: "cancel", Label: "cancel", Description: "keep it"},
	)
	return pick != nil && pick.Value == "remove"
}

// indexOf finds the current value's row so the picker opens on it.
func indexOf(items []tui.Item, value string) int {
	for i, it := range items {
		if it.Value == value {
			return i
		}
	}
	return 0
}

// thinkingLevels are the reasoning efforts, with the budget each one buys.
//
// The descriptions name a token figure rather than a mood ("long reasoning"):
// the level is a cost decision, and an order of magnitude is the only thing
// that makes it one a user can take.
var thinkingLevels = []tui.Item{
	{Value: "off", Label: "off", Description: "No reasoning"},
	{Value: "minimal", Label: "minimal", Description: "Very brief reasoning (~1k tokens)"},
	{Value: "low", Label: "low", Description: "Light reasoning (~2k tokens)"},
	{Value: "medium", Label: "medium", Description: "Moderate reasoning (~8k tokens)"},
	{Value: "high", Label: "high", Description: "Deep reasoning (~16k tokens)"},
	{Value: "xhigh", Label: "xhigh", Description: "Maximum reasoning (~32k tokens)"},
}

// pickThinking is `/thinking` and `/effort` with no argument.
func (t *repl) pickThinking() {
	// The modal marks the current row itself, so nothing is appended here —
	// doing both printed "(current)" twice on the same line.
	current := thinkingName(t.cfg.Reasoning)
	items := make([]tui.Item, len(thinkingLevels))
	copy(items, thinkingLevels)
	// Opens at the top, not on the current row: the levels are an ordered
	// scale, and starting at "off" every time means the cursor's distance
	// from the top is the level, which is quicker to aim at than a cursor
	// that starts somewhere different each time.
	t.pickPlain("Thinking level", items, 0, current, func(choice tui.Item) {
		t.setThinking(choice.Value)
	})
}

// pickScopedModels is `/scoped-models` with no argument: check the models
// ctrl+p should cycle through.
//
// A toggle panel rather than a picker, because the answer is a SET. Asking
// for one model at a time and reopening the list after each would make
// choosing four of them four trips.
func (t *repl) pickScopedModels() {
	available := t.everyAvailableModel()
	if len(available) == 0 {
		t.dim("no available models — /login first")
		return
	}
	checked := map[string]bool{}
	for _, id := range config.LoadSettings().ScopedModels {
		checked[id] = true
	}
	go func() {
		t.inPanel.Store(true)
		defer t.inPanel.Store(false)
		picked := t.app.Toggle("Scoped models — ctrl+p cycles the checked ones", available, checked)
		if picked == nil {
			return
		}
		t.setScopedModels(picked)
	}()
}

// everyAvailableModel is the full-id catalog across authorised providers —
// the cycle list is not confined to the session's provider, because switching
// provider is half the reason to cycle at all.
func (t *repl) everyAvailableModel() []string {
	var out []string
	for _, p := range catalog.Providers {
		if !config.Authorized(p.ID) {
			continue
		}
		for _, m := range catalog.Models(p.ID, config.APIKey(p.ID)) {
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out
}

// pickAlias is `/alias` with no argument: the list, printed.
//
// Printed rather than offered as a picker, which is what this used to do. An
// alias list is reference material — you read it to remember what `/m` was
// bound to — and a picker makes the only thing you can do to it "remove one",
// which is not what you opened it for. Removal has its own spelling,
// `/alias rm <name>`.
func (t *repl) pickAlias() {
	stored := config.LoadSettings().Aliases
	if len(stored) == 0 {
		t.dim("No aliases defined. Usage: /alias <name> </cmd args…> · /alias rm <name>")
		return
	}
	names := make([]string, 0, len(stored))
	for name := range stored {
		names = append(names, name)
	}
	// Sorted, so the listing does not reshuffle itself between openings: Go
	// map iteration order is deliberately random.
	sort.Strings(names)

	width := 0
	for _, name := range names {
		width = max(width, len(name))
	}
	t.app.Do(func() {
		th := t.app.Theme()
		lines := make([]string, 0, len(names))
		for _, name := range names {
			lines = append(lines, th.Fg(tui.SlotAccent, th.Bold("/"+padRight(name, width)))+
				th.Fg(tui.SlotMuted, " → "+stored[name]))
		}
		t.app.Print(lines...)
	})
}

// pickBashDeny is `/bashdeny` with no argument: the denylist manager.
//
// The entries are permission rules under the hood (`deny bash(<pattern>)`),
// which is why the pattern has to be dug back out of the rule for display —
// showing the rule syntax here would leak a representation the user of this
// command never has to think about.
func (t *repl) pickBashDeny() {
	const addPattern = "\x00add"
	t.manage(func() (string, []tui.Item) {
		denied := bashDenyPatterns()
		items := []tui.Item{{
			Value:       addPattern,
			Label:       "+ add command",
			Description: `block a command (e.g. "rm" or "git commit")`,
		}}
		for _, rule := range denied {
			items = append(items, tui.Item{
				Value:       rule.rule,
				Label:       rule.pattern,
				Description: "select to remove",
			})
		}
		return fmt.Sprintf("Bash denylist · %d", len(denied)), items
	}, func(choice tui.Item) bool {
		if choice.Value == addPattern {
			t.addBashDeny()
			return keepPanel
		}
		if t.confirmRemove(choice.Label, fmt.Sprintf("stop blocking %q", choice.Label)) {
			t.removePermissionRule(choice.Value)
			t.dim("unblocked %q", choice.Label)
		}
		return keepPanel
	})
}

// denyEntry pairs a stored rule with the pattern the user typed to make it.
type denyEntry struct{ rule, pattern string }

// bashDenyPatterns is the stored denylist, rules and patterns both.
func bashDenyPatterns() []denyEntry {
	var out []denyEntry
	for _, raw := range config.LoadSettings().Permissions {
		rule, err := permissions.ParseRule(raw)
		if err != nil || rule.Tool != "bash" || rule.Mode != permissions.Deny {
			continue
		}
		out = append(out, denyEntry{rule: raw, pattern: rule.Pattern})
	}
	return out
}

// addBashDeny asks for a command and blocks it.
func (t *repl) addBashDeny() {
	pattern := strings.TrimSpace(t.ask(`command to block (e.g. "rm" or "git commit")`, ""))
	if pattern == "" {
		return
	}
	for _, existing := range bashDenyPatterns() {
		if existing.pattern == pattern {
			t.dim("%q is already in the denylist", pattern)
			return
		}
	}
	rule, err := permissions.ParseRule(fmt.Sprintf("deny bash(%s)", pattern))
	if err != nil {
		t.fail("%s", err)
		return
	}
	if err := config.Update(func(s *config.Settings) {
		s.Permissions = append(s.Permissions, rule.String())
	}); err != nil {
		t.fail("bashdeny: %s", err)
		return
	}
	t.run.Permissions = t.loadPolicy()
	t.dim("blocked %q", pattern)
}

// permissionActions are the three verdicts, strictest first — the order the
// policy resolves them in, so the list reads as the precedence it documents.
var permissionActions = []tui.Item{
	{Value: "deny", Label: "deny", Description: "always refused — wins over everything"},
	{Value: "ask", Label: "ask", Description: "always prompts for approval (fails closed headless)"},
	{Value: "allow", Label: "allow", Description: "runs without the approval prompt"},
}

// pickPermission is `/permissions` with no argument: the rule manager.
//
// Adding is IN the panel rather than only on the command line. A rule has two
// halves — a verdict and a pattern — and asking for them separately is what
// makes the verdict's meaning visible at the moment it is chosen; typed as
// one string, "ask" and "allow" look interchangeable until something runs
// that should not have.
func (t *repl) pickPermission() {
	const addRule = "\x00add"
	t.manage(func() (string, []tui.Item) {
		stored := config.LoadSettings().Permissions
		items := []tui.Item{{
			Value:       addRule,
			Label:       "+ add rule",
			Description: `e.g. deny "bash(rm -rf *)" or allow "bash(git *)"`,
		}}
		for _, rule := range stored {
			items = append(items, tui.Item{Value: rule, Label: rule, Description: "select to remove"})
		}
		return fmt.Sprintf("Permission rules · %d (deny > ask > allow)", len(stored)), items
	}, func(choice tui.Item) bool {
		if choice.Value == addRule {
			t.addPermissionRule()
			return keepPanel
		}
		if t.confirmRemove(choice.Value, "drop this rule") {
			t.removePermissionRule(choice.Value)
		}
		return keepPanel
	})
}

// addPermissionRule is the two-step add: the verdict, then the pattern.
func (t *repl) addPermissionRule() {
	action := t.app.Select("Rule action", permissionActions, 0, "")
	if action == nil {
		return
	}
	raw := strings.TrimSpace(t.ask(`rule (e.g. "bash(git *)", "read(secrets/**)", bare "read", or "*")`, ""))
	if raw == "" {
		return
	}
	rule, err := permissions.ParseRule(action.Value + " " + raw)
	if err != nil {
		t.fail("unrecognized rule: %s", raw)
		t.dim("expected tool(pattern), a bare tool name, or *")
		return
	}
	stored := rule.String()
	for _, existing := range config.LoadSettings().Permissions {
		if existing == stored {
			t.dim("%q is already a %s rule", raw, action.Value)
			return
		}
	}
	if err := config.Update(func(s *config.Settings) {
		s.Permissions = append(s.Permissions, stored)
	}); err != nil {
		t.fail("permissions: %s", err)
		return
	}
	t.run.Permissions = t.loadPolicy()
	t.dim("%s rule added: %q", action.Value, raw)
}

// removePermissionRule drops one stored rule and reloads the policy.
func (t *repl) removePermissionRule(rule string) {
	if err := config.Update(func(s *config.Settings) {
		kept := make([]string, 0, len(s.Permissions))
		for _, r := range s.Permissions {
			if r != rule {
				kept = append(kept, r)
			}
		}
		s.Permissions = kept
	}); err != nil {
		t.fail("permissions: %s", err)
		return
	}
	t.run.Permissions = t.loadPolicy()
	t.dim("removed: %s", rule)
}

// modelItems is the catalog as picker rows, shared by every picker that has
// to choose a model.
func (t *repl) modelItems() []tui.Item {
	ms := catalog.Models(t.cfg.Provider, config.APIKey(t.cfg.Provider))
	items := make([]tui.Item, 0, len(ms))
	for _, m := range ms {
		desc := fmt.Sprintf("%s · ctx %s", m.Name, commaInt(m.Context))
		if m.Reasoning {
			desc += " · think"
		}
		items = append(items, tui.Item{Value: m.ShortID, Label: m.ShortID, Description: desc})
	}
	return items
}

// pickSkill is `/skills` with no argument: choose one to read.
func (t *repl) pickSkill() {
	found := skills.Load(t.cfg.CWD)
	if len(found) == 0 {
		t.dim("no skills — put SKILL.md directories under .pi-agent/skills or ~/.pi-agent/skills")
		return
	}
	// A loop, so reading one skill and then another is two keystrokes rather
	// than two commands — which is what you do when you are deciding which
	// one applies.
	t.manage(func() (string, []tui.Item) {
		found := skills.Load(t.cfg.CWD)
		items := make([]tui.Item, 0, len(found))
		for _, s := range found {
			items = append(items, tui.Item{Value: s.Name, Label: s.Name, Description: s.Description})
		}
		return fmt.Sprintf("Skills · %d", len(found)), items
	}, func(choice tui.Item) bool {
		t.skillsCmd(choice.Value)
		return keepPanel
	})
}

// pickHook is `/hooks` with no argument: the hook manager.
//
// Adding is here rather than in a settings file. A hook is three answers —
// which event, which tools, what to run — and the JSON form makes you know
// the event names before you can ask what they are.
func (t *repl) pickHook() {
	const addHook = "\x00add"
	t.manage(func() (string, []tui.Item) {
		cfg, _ := config.LoadSettings().HookConfig()
		entries := hooks.List(cfg)
		path, _ := config.SettingsPath()
		items := []tui.Item{{
			Value:       addHook,
			Label:       "+ add hook",
			Description: "register a hook in " + path,
		}}
		for _, e := range entries {
			label := string(e.Event)
			if e.Matcher != "" {
				label += " [" + e.Matcher + "]"
			}
			items = append(items, tui.Item{
				Value:       string(e.Event) + "\x00" + e.Matcher + "\x00" + e.Command.Command,
				Label:       label,
				Description: elide(e.Command.Command, 70),
			})
		}
		return fmt.Sprintf("Hooks — %d loaded (Esc to close)", len(entries)), items
	}, func(choice tui.Item) bool {
		if choice.Value == addHook {
			t.addHook()
			return keepPanel
		}
		parts := strings.SplitN(choice.Value, "\x00", 3)
		if len(parts) != 3 {
			return keepPanel
		}
		if t.confirmRemove(parts[0]+": "+elide(parts[2], 50), "delete this hook") {
			t.removeHook(hooks.Event(parts[0]), parts[1], parts[2])
		}
		return keepPanel
	})
}

// addHook asks which event, which tools, then what to run.
func (t *repl) addHook() {
	events := make([]tui.Item, 0, len(hooks.Events))
	for _, e := range hooks.Events {
		events = append(events, tui.Item{Value: string(e), Label: string(e), Description: hooks.About(e)})
	}
	event := t.app.Select("Hook event", events, 0, "")
	if event == nil {
		return
	}

	// Only the tool events have anything to match against, so only they ask.
	matcher := ""
	if e := hooks.Event(event.Value); e == hooks.PreToolUse || e == hooks.PostToolUse {
		matcher = strings.TrimSpace(t.ask(
			"matcher for "+event.Value+" (tool name, bash|edit, regex — blank = all)", ""))
	}

	command := strings.TrimSpace(t.ask("hook command (runs via sh, JSON payload on stdin)", ""))
	if command == "" {
		return
	}
	if err := t.updateHooks(func(cfg hooks.Config) hooks.Config {
		return hooks.Add(cfg, hooks.Event(event.Value), matcher, command)
	}); err != nil {
		t.fail("hooks: %s", err)
		return
	}
	label := event.Value
	if matcher != "" {
		label += " [" + matcher + "]"
	}
	t.dim("hook added: %s → %s", label, command)
}

// removeHook drops one hook command.
func (t *repl) removeHook(event hooks.Event, matcher, command string) {
	if err := t.updateHooks(func(cfg hooks.Config) hooks.Config {
		return hooks.Remove(cfg, event, matcher, command)
	}); err != nil {
		t.fail("hooks: %s", err)
		return
	}
	t.dim("hook removed: %s → %s", event, elide(command, 60))
}

// updateHooks reads the table, applies f, and writes it back.
//
// Through Parse and Marshal rather than editing the stored JSON: the settings
// file may hold either accepted shape, and normalising on the way through is
// what lets the panel edit a hand-written matcher group without discarding
// the matcher.
func (t *repl) updateHooks(f func(hooks.Config) hooks.Config) error {
	cfg, err := config.LoadSettings().HookConfig()
	if err != nil {
		return err
	}
	if cfg == nil {
		cfg = hooks.Config{}
	}
	raw, err := hooks.Marshal(f(cfg))
	if err != nil {
		return err
	}
	return config.Update(func(s *config.Settings) { s.Hooks = raw })
}

// elide shortens a command for a one-line row, marking the cut.
func elide(s string, width int) string {
	if len(s) <= width {
		return s
	}
	return s[:width-3] + "…"
}

// agentsCmd is `/agents`: a picker with no argument, a direct switch with one.
func (t *repl) agentsCmd(rest string) {
	if name := strings.TrimSpace(rest); name != "" {
		t.setAgent(name)
		return
	}
	t.pickAgent()
}

// skillsMenu is `/skills`: a picker with no argument, the skill itself with one.
func (t *repl) skillsMenu(rest string) {
	if arg := strings.TrimSpace(rest); arg != "" {
		if arg == "new" {
			t.newSkill()
			return
		}
		t.skillsCmd(rest)
		return
	}
	t.pickSkill()
}

// newSkill is `/skills new`: author one.
//
// A skill is three things — a name, a description saying WHEN it applies, and
// the instructions — and the only fiddly part is the frontmatter, which is
// exactly the part a hand-written file gets wrong in a way the loader can
// only respond to by silently skipping it. This writes it and reads it back.
func (t *repl) newSkill() {
	name := strings.TrimSpace(t.ask("Skill name (also its directory)", ""))
	if name == "" {
		return
	}
	// Asked for in the model's own terms. A description that says what the
	// skill IS ("git helper") rather than when it APPLIES ("use when writing
	// commit messages") is the single most common reason a skill is never
	// chosen — it is all the model has to go on.
	desc := strings.TrimSpace(t.ask("When should the agent use it? (one line — this is all the model sees)", ""))
	if desc == "" {
		t.dim("a skill with no description can never be chosen — nothing written")
		return
	}

	where := t.app.Select("Where does it live?", []tui.Item{
		{Value: "project", Label: "this project",
			Description: ".pi-agent/skills — committed with the repo, shared with the team"},
		{Value: "user", Label: "just me",
			Description: "~/.pi-agent/skills — available in every project"},
	}, 0, "")
	if where == nil {
		return
	}
	dir := skills.ProjectDir(t.cfg.CWD)
	if where.Value == "user" {
		userDir, err := skills.UserDir()
		if err != nil {
			t.fail("skills: %s", err)
			return
		}
		dir = userDir
	}

	// The body goes through $EDITOR rather than a one-line prompt:
	// instructions are the long part, and a composer that submits on Enter is
	// the wrong shape for writing them.
	body := "# " + name + "\n\nWrite the instructions here. They replace the agent's\n" +
		"default approach for this kind of work, so be specific about what to\n" +
		"do and what not to.\n"
	edited, err := t.editText(name+"-SKILL.md", body)
	if err != nil {
		t.fail("skills: %s", err)
		return
	}
	path, err := skills.Create(dir, name, desc, edited)
	if err != nil {
		t.fail("skills: %s", err)
		return
	}
	t.dim("wrote %s", path)
	// The index is built from disk on every turn, so it is already live —
	// worth saying, because "do I have to restart" is the next question.
	t.dim("the agent can use it from the next message")
}

// hooksMenu is `/hooks`: the manager.
//
// The manager alone, not a listing followed by one. Printing the table and
// then opening a panel showing the same table said everything twice, and the
// printed copy scrolled away the moment the panel closed.
func (t *repl) hooksMenu() { t.pickHook() }
