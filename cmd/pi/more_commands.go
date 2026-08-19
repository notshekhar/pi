package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/modules/core/catalog"
	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/gateway"
	"github.com/notshekhar/pi/internal/modules/core/permissions"
	"github.com/notshekhar/pi/internal/modules/core/serve"
	"github.com/notshekhar/pi/internal/modules/core/session"
	"github.com/notshekhar/pi/internal/modules/tui"
)

// The rest of the command surface.

// paste inserts the system clipboard into the draft.
// clipboardRead pulls text from the platform's clipboard tool.
func clipboardRead() (string, error) {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "pbpaste"
	case "windows":
		name, args = "powershell", []string{"-command", "Get-Clipboard"}
	default:
		if _, err := exec.LookPath("wl-paste"); err == nil {
			name = "wl-paste"
		} else {
			name, args = "xclip", []string{"-selection", "clipboard", "-o"}
		}
	}
	if _, err := exec.LookPath(name); err != nil {
		return "", fmt.Errorf("%s not found", name)
	}
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

// cloneSession copies the conversation into a new one and switches to it.
//
// Distinct from /fork only in intent — fork says "branch from here", clone
// says "keep this one and give me a copy" — so it shares the implementation
// and differs in what it tells you.
func (t *repl) cloneSession() {
	if t.busy() {
		return
	}
	cloned, err := session.Fork(t.run.Session)
	if err != nil {
		t.fail("clone: %s", err)
		return
	}
	original := t.run.Session.Meta.Label()
	t.run.Session = cloned
	t.dim("cloned %s — you are now on the copy", original)
}

// sessionTree draws the fork tree and lets a branch be switched to.
//
// Grouped by working directory first, because a session list spanning every
// repository you have touched is not a tree anyone reads. Within a
// directory, forks nest under the session they came from — which is only
// possible because a fork RECORDS its parent: the conversations themselves
// are byte-identical up to the branch point, so the structure cannot be
// recovered from them afterwards.
func (t *repl) sessionTree() {
	metas, err := session.List()
	if err != nil {
		t.fail("tree: %s", err)
		return
	}
	if len(metas) == 0 {
		t.dim("no saved sessions")
		return
	}

	byDir := map[string][]session.Meta{}
	for _, m := range metas {
		dir := m.CWD
		if dir == "" {
			dir = "(unknown)"
		}
		byDir[dir] = append(byDir[dir], m)
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Slice(dirs, func(i, j int) bool {
		// The current repository first; it is why the command was run.
		if dirs[i] == t.cfg.CWD {
			return true
		}
		if dirs[j] == t.cfg.CWD {
			return false
		}
		return dirs[i] < dirs[j]
	})

	var items []tui.Item
	initial := 0
	t.app.Do(func() {
		th := t.app.Theme()
		var lines []string
		for _, dir := range dirs {
			label := tui.ShortenPath(dir)
			if dir == t.cfg.CWD {
				label += "  (here)"
			}
			lines = append(lines, th.Fg(tui.SlotAccent, label))
			for _, row := range treeRows(byDir[dir]) {
				marker := "  "
				if row.meta.ID == t.run.Session.ID {
					marker = "→ "
					initial = len(items)
				}
				// Truncate as well as pad: padRight only ever grows a
				// string, so a long title ran straight into the detail
				// column and wrapped the row.
				name := tui.Truncate(row.prefix+row.meta.Label(), 44)
				lines = append(lines, marker+th.Fg(tui.SlotText, padRight(name, 45))+
					th.Fg(tui.SlotDim, row.meta.Detail()))
				items = append(items, tui.Item{
					Value:       row.meta.ID,
					Label:       row.prefix + row.meta.Label(),
					Description: row.meta.Detail(),
				})
			}
			lines = append(lines, "")
		}
		t.app.Print(lines...)
	})

	// Pick blocks on the render loop, so it must not run on it.
	go func() {
		choice := t.app.Pick("Switch branch", items, initial, t.run.Session.ID)
		if choice == nil || choice.Value == t.run.Session.ID {
			return
		}
		t.resume(choice.Value)
	}()
}

// treeRow is one session and the branch art that leads to it.
type treeRow struct {
	meta   session.Meta
	prefix string
}

// treeRows nests forks under their parents, depth-first.
//
// A session whose parent is not in this directory is treated as a ROOT: a
// fork made before a /cd would otherwise vanish from the listing entirely,
// which is worse than showing it unattached.
func treeRows(metas []session.Meta) []treeRow {
	byParent := map[string][]session.Meta{}
	present := map[string]bool{}
	for _, m := range metas {
		present[m.ID] = true
	}
	var roots []session.Meta
	for _, m := range metas {
		if m.Parent != "" && present[m.Parent] {
			byParent[m.Parent] = append(byParent[m.Parent], m)
			continue
		}
		roots = append(roots, m)
	}
	// Oldest first WITHIN the tree: a parent that sorted after its child read
	// as though the branch came first.
	sortOldestFirst(roots)
	for id := range byParent {
		sortOldestFirst(byParent[id])
	}

	var out []treeRow
	var walk func(m session.Meta, depth int)
	walk = func(m session.Meta, depth int) {
		prefix := ""
		if depth > 0 {
			prefix = strings.Repeat("  ", depth-1) + "└─ "
		}
		out = append(out, treeRow{meta: m, prefix: prefix})
		for _, child := range byParent[m.ID] {
			walk(child, depth+1)
		}
	}
	for _, root := range roots {
		walk(root, 0)
	}
	return out
}

func sortOldestFirst(metas []session.Meta) {
	sort.Slice(metas, func(i, j int) bool { return metas[i].Created.Before(metas[j].Created) })
}

func (t *repl) share() {
	if t.busy() {
		return
	}
	if t.run.Session.Text() == "" {
		t.dim("nothing to share yet")
		return
	}
	if _, err := exec.LookPath("gh"); err != nil {
		t.dim("/share needs the gh CLI — /export writes a file instead")
		return
	}

	path, err := t.exportTo(filepath.Join(os.TempDir(),
		fmt.Sprintf("pi-agent-%s.md", time.Now().Format("20060102-150405"))))
	if err != nil {
		t.fail("share: %s", err)
		return
	}

	// Uploading is network work, so it must not run on the render loop.
	go func() {
		defer os.Remove(path)
		// SECRET, not public: a transcript routinely contains file paths,
		// source, and whatever the user pasted in. Secret gists are
		// unlisted, so the link is the only way in.
		out, err := exec.Command("gh", "gist", "create", path,
			"--desc", "pi-agent session "+t.run.Session.Meta.Label()).CombinedOutput()
		if err != nil {
			t.fail("share: %s", strings.TrimSpace(string(out)))
			return
		}
		url := ""
		for _, line := range strings.Fields(string(out)) {
			if strings.HasPrefix(line, "https://") {
				url = line
			}
		}
		if url == "" {
			t.dim("shared, but gh printed no url:\n%s", strings.TrimSpace(string(out)))
			return
		}
		t.app.Do(func() {
			th := t.app.Theme()
			t.app.Print(th.Fg(tui.SlotSuccess, "shared ") + th.Fg(tui.SlotAccent, url))
		})
		_ = clipboardWrite(url)
	}()
}

// setUIMode reports the look. Noir is the only one, by design — see the
// package comment in the tui module.
func (t *repl) setUIMode(rest string) {
	if name := strings.ToLower(strings.TrimSpace(rest)); name != "" && name != "noir" {
		t.dim("noir is the only ui mode; /theme switches night and day")
		return
	}
	t.dim("ui noir · theme %s", orDefault(config.LoadSettings().Theme, "night"))
}

// hotkeys lists the keys, which are otherwise only discoverable by accident.
func (t *repl) hotkeys() {
	// One flat table, not sections. The sections were tidier to write and
	// worse to use: the question this command answers is "what is the key for
	// X", and grouping makes you first decide which group X is in.
	//
	// The rows also name the terminal-dependent chords honestly. A binding
	// that only works under the kitty protocol says so, rather than looking
	// broken on a terminal that cannot send it.
	bindings := [][2]string{
		{"Enter", "submit"},
		{"Alt+Enter", "newline"},
		{"Tab", "completion (slash commands, @ files)"},
		{"Shift+Tab", "cycle agent"},
		{"@ / #", "file completion while typing"},
		{"Up / Down", "history (Up on first line, like a shell)"},
		{"Ctrl+A / Ctrl+E", "line start / line end"},
		{"Ctrl+W / Ctrl+U", "delete word · delete line"},
		{"Ctrl+K", "delete to end of line"},
		{"Ctrl+Z / Ctrl+Y", "undo · redo"},
		{"Opt+←/→", "word jump · Opt+Backspace delete word"},
		{"Esc", "abort current turn"},
		{"Ctrl+C", "abort, twice to quit"},
		{"Ctrl+D", "quit (empty)"},
		{"Ctrl+G", `send "continue" (resume interrupted work)`},
		{"Ctrl+L", "clear screen"},
		{"Ctrl+P", "cycle scoped models"},
		{"PgUp / PgDn", "scroll the transcript"},
		{"Ctrl+E", "navigate transcript;"},
		{"", "inside: arrows select, e expand all, Esc exit"},
	}

	// One column width for the whole table, sized to the widest key. A fixed
	// width silently collides with anything longer than the guess.
	width := 0
	for _, b := range bindings {
		width = max(width, tui.VisibleWidth(b[0]))
	}
	width += 2

	t.app.Do(func() {
		th := t.app.Theme()
		lines := make([]string, 0, len(bindings))
		for _, b := range bindings {
			lines = append(lines,
				th.Fg(tui.SlotAccent, tui.PadRight(b[0], width))+th.Fg(tui.SlotMuted, b[1]))
		}
		t.app.Print(lines...)
	})
}

// bashDeny adds a deny rule for a shell command pattern — the common case of
// /permissions, worth its own verb because it is the one people reach for.
func (t *repl) bashDeny(rest string) {
	pattern := strings.TrimSpace(rest)
	if pattern == "" {
		t.pickBashDeny()
		return
	}
	t.permissions(fmt.Sprintf("deny bash(%s)", pattern))
}

func (t *repl) showBashDeny() {
	stored := config.LoadSettings().Permissions
	t.app.Do(func() {
		th := t.app.Theme()
		var lines []string
		for _, raw := range stored {
			if rule, err := permissions.ParseRule(raw); err == nil &&
				rule.Tool == "bash" && rule.Mode == permissions.Deny {
				lines = append(lines, "  "+th.Fg(tui.SlotError, rule.Pattern))
			}
		}
		if len(lines) == 0 {
			t.app.Print(th.Fg(tui.SlotDim, "no bash deny rules beyond the built-in ones — /permissions to see those"))
			return
		}
		t.app.Print(append([]string{th.Fg(tui.SlotDim, "denied bash patterns")}, lines...)...)
	})
}

// logout forgets a provider's stored credential.
//
// With no argument it opens the picker rather than printing usage — the
// registry has always described it as "opens picker", and being told the
// syntax when you asked to be shown the options is the specific failure this
// whole family of commands exists to avoid.
func (t *repl) logout(rest string) {
	if name := strings.TrimSpace(rest); name != "" {
		t.signOut(name)
		return
	}
	t.pickLogout()
}

// signOutAll is the sentinel row that clears every credential at once.
const signOutAll = "\x00all"

// pickLogout offers the providers that actually have a credential stored.
func (t *repl) pickLogout() {
	var items []tui.Item
	for _, p := range catalog.Providers {
		if !config.Authorized(p.ID) {
			continue
		}
		desc := ""
		if p.ID == t.cfg.Provider {
			desc = "(active)"
		}
		items = append(items, tui.Item{Value: p.ID, Label: p.ID, Description: desc})
	}
	if len(items) == 0 {
		t.dim("no providers to sign out from")
		return
	}
	items = append(items, tui.Item{
		Value:       signOutAll,
		Label:       "all providers",
		Description: "Sign out from every provider",
	})
	t.pick("Sign out of provider (type to filter)", items, 0, "", func(choice tui.Item) {
		t.signOut(choice.Value)
	})
}

// signOut clears one provider's credential, or every one.
func (t *repl) signOut(name string) {
	if name == signOutAll {
		var failed []string
		for _, p := range catalog.Providers {
			if !config.Authorized(p.ID) {
				continue
			}
			if err := config.Logout(p.ID); err != nil {
				failed = append(failed, p.ID)
			}
		}
		if len(failed) > 0 {
			t.fail("could not sign out of: %s", strings.Join(failed, ", "))
			return
		}
		t.dim("signed out of all providers")
		return
	}
	if err := config.Logout(name); err != nil {
		t.fail("logout: %s", err)
		return
	}
	t.dim("signed out of %s", name)
}

// reload re-reads settings and reconnects the things built from them.
func (t *repl) reload() {
	if t.busy() {
		return
	}
	t.applySettings()
	// Skills and prompt files are commands, so a reload has to rebuild the
	// completion catalog too — otherwise a skill added during the session is
	// runnable but not offerable, which reads as it not existing.
	t.app.Do(func() { t.app.SetSources(t.commandItems(), fileItems(t.cfg.CWD)) })
	t.dim("reloaded settings, skills, and commands · /mcp reconnect for tool servers")
}

// changelog points at where the work is recorded rather than inventing a
// version history this project does not keep.
func (t *repl) changelog() {
	t.app.Do(func() {
		th := t.app.Theme()
		t.app.Print(
			th.Fg(tui.SlotMuted, "pi-agent "+version),
			th.Fg(tui.SlotDim, "progress against loop is tracked in PARITY.md"),
			th.Fg(tui.SlotDim, "conventions and traps are in AGENTS.md"))
	})
}

// scopedModels shows or sets the per-scope model overrides.
// scopedModels is `/scoped-models`: which models ctrl+p cycles through.
//
// Not the per-job overrides it used to be — those are `/settings`, under
// subagentModel and compactModel. This is loop's meaning of the word: a short
// list of models you actually switch between during a session, reachable with
// one chord instead of six keystrokes through the model picker.
func (t *repl) scopedModels(rest string) {
	rest = strings.TrimSpace(rest)
	verb, id, _ := strings.Cut(rest, " ")
	id = strings.TrimSpace(id)

	switch strings.ToLower(verb) {
	case "":
		t.pickScopedModels()
	case "add":
		if id == "" {
			t.dim("usage: /scoped-models add <provider/model>")
			return
		}
		t.setScopedModels(append(config.LoadSettings().ScopedModels, id))
	case "rm", "remove":
		kept := []string{}
		for _, existing := range config.LoadSettings().ScopedModels {
			if existing != id {
				kept = append(kept, existing)
			}
		}
		t.setScopedModels(kept)
	default:
		// A bare list, so `/scoped-models` in a script prints rather than
		// opening a panel nothing can answer.
		t.showScopedModels()
	}
}

// setScopedModels stores the cycle list, deduplicated in the order given.
func (t *repl) setScopedModels(ids []string) {
	seen := map[string]bool{}
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		kept = append(kept, id)
	}
	if err := config.Update(func(s *config.Settings) { s.ScopedModels = kept }); err != nil {
		t.fail("scoped-models: %s", err)
		return
	}
	if len(kept) == 0 {
		t.dim("scoped models cleared — ctrl+p disabled")
		return
	}
	t.dim("scoped models (%d): %s", len(kept), strings.Join(kept, ", "))
}

func (t *repl) showScopedModels() {
	ids := config.LoadSettings().ScopedModels
	if len(ids) == 0 {
		t.dim("no scoped models — pick some with /scoped-models")
		return
	}
	t.dim("scoped models (%d): %s", len(ids), strings.Join(ids, ", "))
}

// cycleModel is ctrl+p: step to the next model in the scoped list.
func (t *repl) cycleModel() {
	ids := config.LoadSettings().ScopedModels
	if len(ids) == 0 {
		t.dim("no scoped models — pick some with /scoped-models")
		return
	}
	current := t.cfg.FullID()
	at := -1
	for i, id := range ids {
		if id == current {
			at = i
			break
		}
	}
	next := ids[(at+1)%len(ids)]
	provider, model := catalog.Parse(next, t.cfg.Provider)
	if model == "" {
		t.fail("scoped model %q is not a provider/model id", next)
		return
	}
	t.cfg.Provider, t.cfg.ModelID = provider, model
	t.apply()
}

// customProviders shows or edits user-defined endpoints.
func (t *repl) customProviders(rest string) {
	fields := strings.Fields(strings.TrimSpace(rest))
	switch {
	case len(fields) == 0:
		t.showCustomProviders()
	case fields[0] == "remove" && len(fields) == 2:
		if err := config.RemoveCustomProvider(fields[1]); err != nil {
			t.fail("%s", err)
			return
		}
		t.dim("removed provider %s", fields[1])
	case len(fields) >= 2:
		p := config.CustomProvider{BaseURL: fields[1]}
		if len(fields) > 2 {
			p.EnvVar = fields[2]
		}
		for _, id := range fields[3:] {
			p.Models = append(p.Models, config.CustomModel{ID: id})
		}
		if err := config.AddCustomProvider(fields[0], p); err != nil {
			t.fail("%s", err)
			return
		}
		t.dim("added provider %s → %s", fields[0], p.BaseURL)
	default:
		t.dim("usage: /provider add <name> <baseUrl> [ENV_VAR] [model…] · /provider remove <name>")
	}
}

func (t *repl) showCustomProviders() {
	names := config.CustomProviderNames()
	t.app.Do(func() {
		th := t.app.Theme()
		if len(names) == 0 {
			t.app.Print(
				th.Fg(tui.SlotDim, "no custom providers"),
				th.Fg(tui.SlotDim, "/provider add <name> <baseUrl> [ENV_VAR] [model…] — any OpenAI-compatible endpoint"))
			return
		}
		var lines []string
		for _, name := range names {
			p, _ := config.LookupCustom(name)
			state := th.Fg(tui.SlotSuccess, "key found")
			if p.CustomKey() == "" {
				state = th.Fg(tui.SlotWarning, "no key")
			}
			lines = append(lines, th.Fg(tui.SlotAccent, tui.PadRight(name, 16))+
				th.Fg(tui.SlotMuted, tui.PadRight(p.BaseURL, 44))+state)
		}
		t.app.Print(lines...)
	})
}

// goal runs a goal to a verified conclusion.
//
// Takes the turn lock for the whole run — it is several turns and a session
// rewrite would interleave badly — and reports each phase as it happens, so a
// long run is legible rather than a frozen prompt.
func (t *repl) goal(parent context.Context, rest string) {
	goal := strings.TrimSpace(rest)
	if goal == "" {
		t.dim("usage: /goal <what you want to be true when this is done>")
		return
	}

	t.mu.Lock()
	if t.turning {
		t.mu.Unlock()
		t.dim("(turn in progress — Esc to interrupt)")
		return
	}
	t.turning = true
	ctx, cancel := context.WithCancel(parent)
	t.cancel = cancel
	t.mu.Unlock()

	t.app.Do(func() { t.app.UserEcho("/goal " + goal) })
	t.dim("planning…")

	go func() {
		defer func() {
			t.mu.Lock()
			t.turning = false
			t.cancel = nil
			t.mu.Unlock()
			t.app.Do(func() {
				t.app.LoaderStop()
				t.app.InterruptOpen()
			})
		}()
		t.app.Do(func() { t.app.LoaderStart() })

		// The work phase reuses the ordinary turn machinery, so it renders
		// exactly like every other turn.
		work := func(ctx context.Context, prompt string) error {
			_, err := t.run.TurnStream(ctx, prompt, t.consume)
			return err
		}

		result, err := t.run.RunGoal(ctx, goal, work)
		if err != nil {
			if ctx.Err() != nil {
				t.dim("(interrupted)")
				return
			}
			t.fail("goal: %s", err)
			return
		}

		t.app.Do(func() {
			th := t.app.Theme()
			if result.Passed {
				t.app.Print(th.Fg(tui.SlotSuccess, fmt.Sprintf(
					"goal verified after %d attempt(s)", result.Attempts)))
			} else {
				t.app.Print(th.Fg(tui.SlotWarning, fmt.Sprintf(
					"goal NOT verified after %d attempt(s) — the review said:", result.Attempts)))
			}
			// The verdict is markdown, so it renders as prose rather than as
			// a wall of notice lines.
			t.app.AssistantDelta(result.Verdict)
			t.app.AssistantEnd()
		})
	}()
}

// login stores a provider credential.
//
// The key is taken as an argument rather than prompted for, because a TUI
// prompt would echo it into the transcript and from there into the session
// file on disk. An argument is at worst in shell history, which the user
// already controls.
func (t *repl) login(rest string) {
	fields := strings.Fields(strings.TrimSpace(rest))
	switch len(fields) {
	case 0:
		// No argument opens the picker, then asks for the key — the listing
		// alone made the user read the provider ids and retype one.
		t.pickLogin()
		return
	case 1:
		t.beginLogin(fields[0])
		return
	}
	t.saveKey(fields[0], fields[1])
}

// pickLogin offers every provider, then asks for that provider's key.
func (t *repl) pickLogin() {
	// First, because it is the only row that ADDS a provider rather than
	// signing in to one that already exists.
	items := make([]tui.Item, 0, len(catalog.Providers)+1)
	items = append(items, tui.Item{
		Value: "custom", Label: "custom",
		Description: "add any OpenAI-compatible endpoint — a gateway, a proxy, a local server",
	})
	for _, p := range catalog.Providers {
		state := "no key"
		switch {
		case p.Keyless:
			state = "keyless — nothing to sign in to"
		case p.ID == "xai" && config.XaiSignedIn():
			state = "subscription — signed in with SuperGrok"
		case config.APIKey(p.ID) != "":
			state = "ready — signing in again replaces the key"
		case p.ID == "xai":
			state = "no key — subscription sign-in available"
		}
		items = append(items, tui.Item{Value: p.ID, Label: p.ID, Description: p.Name + " · " + state})
	}
	t.pick("Sign in to provider (type to filter)", items, 0, t.cfg.Provider, func(choice tui.Item) {
		t.beginLogin(choice.Value)
	})
}

// customProviderWizard is `/login custom` — loop's flow for pointing pi at any
// OpenAI-compatible endpoint: a gateway, a proxy, a self-hosted server.
//
// A wizard rather than the one-line `/provider add` form that already exists,
// because the one-line form only reaches the two simplest cases. The
// interesting endpoints authenticate in ways a positional argument cannot
// express — a key that lives in a vault, a header that is not `Authorization`
// — and a flow that cannot express them sends people to the settings file.
func (t *repl) customProviderWizard() {
	name := strings.ToLower(strings.TrimSpace(t.ask("A name for this endpoint (used as the provider id)", "")))
	if name == "" {
		return
	}
	if _, taken := catalog.LookupProvider(name); taken {
		t.fail("%q is a built-in provider — pick another name", name)
		return
	}
	baseURL := strings.TrimSpace(t.ask("Base URL (e.g. https://gateway.internal/v1)", ""))
	if baseURL == "" {
		return
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		t.fail("the base URL must start with http:// or https://")
		return
	}

	p := config.CustomProvider{BaseURL: baseURL}
	method := t.app.Select("How does "+name+" authenticate?", []tui.Item{
		{Value: "apikey", Label: "API key",
			Description: "a key you paste now · sent as Authorization: Bearer"},
		{Value: "env", Label: "Environment variable",
			Description: "read at request time · nothing stored on disk"},
		{Value: "command", Label: "Command",
			Description: "a shell command whose output is the key · vault, SSO helper"},
		{Value: "none", Label: "None or headers only",
			Description: "an open endpoint, mTLS, or a credential carried in a header"},
	}, 0, "")
	if method == nil {
		return
	}
	switch method.Value {
	case "apikey":
		// Through the prompt, never an argument: a key on the command line is
		// in the shell history and in the transcript.
		p.APIKey = strings.TrimSpace(t.ask("API key (not echoed back)", ""))
		if p.APIKey == "" {
			return
		}
	case "env":
		p.EnvVar = strings.TrimSpace(t.ask("Environment variable holding the key", ""))
		if p.EnvVar == "" {
			return
		}
	case "command":
		p.KeyCommand = strings.TrimSpace(t.ask("Command whose output is the key", ""))
		if p.KeyCommand == "" {
			return
		}
	}

	if headers := strings.TrimSpace(t.ask(`Extra headers, optional — "X-Tenant: acme; X-Env: prod"`, "")); headers != "" {
		p.Headers = parseHeaders(headers)
	}

	// Models last, because they are the one answer the endpoint cannot be
	// asked for: an OpenAI-compatible gateway need not serve /models, and a
	// model nobody can name is a model nobody can pick.
	models := strings.TrimSpace(t.ask("Model ids it serves, comma separated (the first is the default)", ""))
	for _, id := range strings.Split(models, ",") {
		if id = strings.TrimSpace(id); id != "" {
			p.Models = append(p.Models, config.CustomModel{ID: id})
		}
	}
	if len(p.Models) == 0 {
		t.fail("no models given — %q not saved, since nothing could be selected on it", name)
		return
	}

	// The context window, which is not a nicety: auto-compaction is a
	// fraction OF it, so a window of zero means a session that never compacts
	// and eventually just fails on a too-long request.
	if ctx := strings.TrimSpace(t.ask("Context window in tokens, optional (e.g. 200000)", "")); ctx != "" {
		if n, err := strconv.Atoi(strings.ReplaceAll(ctx, ",", "")); err == nil && n > 0 {
			p.Context = n
			for i := range p.Models {
				p.Models[i].Context = n
			}
		}
	}

	// And the price. A gateway is not in the catalog, so nothing else knows
	// what its tokens cost — and a model with no rate bills at zero, which
	// makes /cost report the work as free.
	if rates := strings.TrimSpace(t.ask(`Price $/million tokens, optional — "input,output" (e.g. 3,15)`, "")); rates != "" {
		in, out, _ := strings.Cut(rates, ",")
		cost := &config.CustomCost{}
		cost.Input, _ = strconv.ParseFloat(strings.TrimSpace(in), 64)
		cost.Output, _ = strconv.ParseFloat(strings.TrimSpace(out), 64)
		if cost.Input > 0 || cost.Output > 0 {
			for i := range p.Models {
				p.Models[i].Cost = cost
			}
		}
	}

	if err := config.AddCustomProvider(name, p); err != nil {
		t.fail("%s", err)
		return
	}
	t.dim("added %s → %s (%d models)", name, p.BaseURL, len(p.Models))
	// Selected straight away: someone who just configured an endpoint wants
	// to use it, and making them run /provider afterwards is a step with no
	// decision in it.
	t.cfg.Provider, t.cfg.ModelID = name, p.Models[0].ID
	t.apply()
}

// parseHeaders reads `Name: value; Other: value`.
func parseHeaders(raw string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ";") {
		name, value, ok := strings.Cut(pair, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if ok && name != "" && value != "" {
			out[name] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// beginLogin starts the right flow for a provider: most have only a key, and
// the ones with a subscription option ask which the user wants.
func (t *repl) beginLogin(provider string) {
	switch provider {
	case "custom":
		t.customProviderWizard()
	case "xai":
		t.signInXai(context.Background())
	default:
		t.askForKey(provider)
	}
}

// signInXai offers the two ways into xAI.
//
// The subscription path exists because the two are not interchangeable: an
// API key is pay-as-you-go, and a SuperGrok subscriber who is only offered
// the key pays for the same tokens twice.
func (t *repl) signInXai(parent context.Context) {
	choice := t.app.Select("xAI — how do you want to sign in?", []tui.Item{
		{Value: "oauth", Label: "Sign in with xAI",
			Description: "SuperGrok subscription · opens your browser"},
		{Value: "apikey", Label: "Use an API key",
			Description: "pay-as-you-go · XAI_API_KEY"},
	}, 0, "")
	if choice == nil {
		return
	}
	if choice.Value == "apikey" {
		t.askForKey("xai")
		return
	}
	t.dim("opening your browser to authorize xAI…")
	// On its own goroutine: the flow waits on a browser round-trip, and doing
	// that on the render loop would freeze the screen for as long as the user
	// takes to click Approve.
	go func() {
		ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
		defer cancel()
		err := config.XaiLogin(ctx, func(url, instructions string) {
			t.app.Do(func() {
				th := t.app.Theme()
				t.app.Print(
					th.Fg(tui.SlotDim, instructions),
					th.Fg(tui.SlotAccent, url),
				)
			})
			// Best effort: a machine with no browser still has the URL
			// printed above, which is the whole reason it is printed.
			openBrowser(url)
		})
		if err != nil {
			t.fail("xai login: %s", err)
			return
		}
		t.dim("signed in to xAI — your SuperGrok subscription is now in use")
		t.applySettings()
	}()
}

// openBrowser asks the desktop to open a URL, and says nothing if it cannot.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// askForKey prompts for a provider's credential.
//
// Through the prompt rather than the command line, so the key never lands in
// the shell history or the transcript — `/login anthropic sk-…` is one
// screenshot away from being shared.
func (t *repl) askForKey(provider string) {
	if p, ok := catalog.LookupProvider(provider); ok && p.Keyless {
		t.dim("%s needs no credential", provider)
		return
	}
	key := strings.TrimSpace(t.ask(provider+" API key (not echoed back, not stored in history)", ""))
	if key == "" {
		return
	}
	t.saveKey(provider, key)
}

func (t *repl) saveKey(provider, key string) {
	if err := config.SaveKey(provider, key); err != nil {
		t.fail("login: %s", err)
		return
	}
	// Never echo the key, not even a prefix.
	t.dim("stored a credential for %s", provider)
	t.applySettings()
}

func (t *repl) showLogin() {
	t.app.Do(func() {
		th := t.app.Theme()
		var lines []string
		for _, p := range catalog.Providers {
			state := th.Fg(tui.SlotWarning, "no key")
			switch {
			case p.Keyless:
				state = th.Fg(tui.SlotDim, "keyless")
			case p.ID == "xai" && config.XaiSignedIn():
				state = th.Fg(tui.SlotSuccess, "subscription")
			case config.APIKey(p.ID) != "":
				state = th.Fg(tui.SlotSuccess, "ready")
			}
			lines = append(lines, th.Fg(tui.SlotAccent, tui.PadRight(p.ID, 14))+
				th.Fg(tui.SlotMuted, tui.PadRight(p.Name, 32))+state)
		}
		lines = append(lines, "",
			th.Fg(tui.SlotDim, "/login <provider> <key> · /login custom · /logout <provider>"),
			th.Fg(tui.SlotDim, "credentials are shared with loop in ~/.loop/auth.json"))
		t.app.Print(lines...)
	})
}

// gatewayPanel is `/gateways` with no argument.
func (t *repl) gatewayPanel(parent context.Context) {
	t.manage(func() (string, []tui.Item) {
		settings := config.LoadSettings()
		state, about := "not configured", "set this gateway up"
		switch {
		case t.telegram != nil:
			state, about = "running", "a paired chat is driving this machine"
		case settings.TelegramToken != "":
			state, about = "configured", "start it, or change the credentials"
		}
		return "Gateways — remote chat control (Esc to close)", []tui.Item{{
			Value:       "telegram",
			Label:       "Telegram — " + state,
			Description: about,
		}}
	}, func(choice tui.Item) bool {
		t.telegramActions(parent)
		return keepPanel
	})
}

// telegramActions is the per-gateway menu.
func (t *repl) telegramActions(parent context.Context) {
	settings := config.LoadSettings()
	var items []tui.Item
	if t.telegram != nil {
		items = append(items, tui.Item{Value: "stop", Label: "stop", Description: "disconnect the bot"})
	} else if settings.TelegramToken != "" {
		items = append(items, tui.Item{Value: "start", Label: "start", Description: "connect and start polling"})
	}
	items = append(items,
		tui.Item{Value: "token", Label: "set bot token", Description: "from @BotFather"},
		tui.Item{Value: "chat", Label: "set chat id", Description: "the only chat allowed to drive this agent"})

	action := t.choose("Telegram", items...)
	if action == nil {
		return
	}
	switch action.Value {
	case "start":
		t.gateways(parent, "telegram")
	case "stop":
		t.gateways(parent, "stop")
	case "token":
		// Never shown back: a bot token is a credential, and pre-filling the
		// prompt with it would put it on screen every time it is edited.
		token := strings.TrimSpace(t.ask("Telegram bot token (from @BotFather)", ""))
		if token == "" {
			return
		}
		if err := config.Update(func(s *config.Settings) { s.TelegramToken = token }); err != nil {
			t.fail("gateways: %s", err)
			return
		}
		t.dim("telegram token stored")
	case "chat":
		raw := strings.TrimSpace(t.ask("Telegram chat id (only this chat may drive the agent)",
			fmt.Sprint(settings.TelegramChatID)))
		if raw == "" {
			return
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			t.fail("chat id must be a number, got %q", raw)
			return
		}
		if err := config.Update(func(s *config.Settings) { s.TelegramChatID = id }); err != nil {
			t.fail("gateways: %s", err)
			return
		}
		t.dim("telegram chat id set to %d", id)
	}
}

// serve exposes the session over loopback HTTP.
func (t *repl) serve(parent context.Context, rest string) {
	rest = strings.TrimSpace(rest)
	// Off by default and checked here, because starting the server is what
	// exposes the machine: anyone with the url and token drives this agent.
	if rest != "stop" && !config.LoadSettings().ServeOn() {
		t.dim("serve is off — turn it on in /settings first")
		return
	}
	if rest == "stop" {
		if t.server == nil {
			t.dim("not serving")
			return
		}
		t.server.Stop()
		t.server = nil
		t.dim("stopped serving")
		return
	}
	if t.server != nil {
		t.dim("already serving — %s", t.server.URL())
		return
	}

	port := 0
	if rest != "" {
		n, err := strconv.Atoi(rest)
		if err != nil || n < 1 || n > 65535 {
			t.dim("usage: /serve [port] · /serve stop")
			return
		}
		port = n
	}

	srv := serve.New(func(prompt string) {
		// A prompt from the network runs exactly like a typed one, including
		// the echo — so what a remote client asked for is visible here.
		t.app.Do(func() { t.app.UserEcho(prompt) })
		t.startTurn(parent, prompt)
	})
	if err := srv.Start("", port); err != nil {
		t.fail("serve: %s", err)
		return
	}
	t.server = srv
	t.app.Do(func() {
		th := t.app.Theme()
		t.app.Print(
			th.Fg(tui.SlotSuccess, "serving on "+srv.Addr),
			th.Fg(tui.SlotDim, srv.URL()),
			th.Fg(tui.SlotDim, "loopback only · the token is required · /serve stop to end it"))
	})
}

// gateways reports the chat connections, and starts the Telegram one.
func (t *repl) gateways(parent context.Context, rest string) {
	rest = strings.TrimSpace(rest)
	settings := config.LoadSettings()

	switch rest {
	case "":
		// A panel, not a listing: the answer to "telegram is not configured"
		// is to configure it, and the listing left the user to find the
		// settings file and the two key names by hand.
		t.gatewayPanel(parent)
	case "telegram":
		if t.telegram != nil {
			t.dim("telegram is already running")
			return
		}
		bot := gateway.NewTelegram(settings.TelegramToken, settings.TelegramChatID)
		if bot == nil {
			t.dim("no telegramToken in settings")
			return
		}
		t.telegram = bot
		go t.pollTelegram(parent, bot)
		t.dim("telegram connected — messages from the configured chat run as prompts")
	default:
		t.dim("usage: /gateways [telegram]")
	}
}

// pollTelegram feeds inbound messages in as prompts.
//
// A failed poll backs off rather than spinning: the usual cause is the
// network being away, and hammering it helps nobody.
func (t *repl) pollTelegram(ctx context.Context, bot *gateway.Telegram) {
	backoff := time.Second
	for ctx.Err() == nil {
		messages, err := bot.Poll(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			t.dim("telegram: %s", err)
			time.Sleep(backoff)
			if backoff < time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		for _, m := range messages {
			prompt := m.Text
			t.app.Do(func() { t.app.UserEcho("[telegram] " + prompt) })
			t.startTurn(ctx, prompt)
		}
	}
}

// clipboardImage writes any image on the clipboard to a temp file and
// returns its path, or "" when the clipboard holds no image.
//
// Via a temp file rather than bytes because everything downstream — the
// attachment detector, the size and type checks — is written against a path,
// and because it gives the user something to look at if a send goes wrong.
func clipboardImage() (string, error) {
	if runtime.GOOS != "darwin" {
		// pngpaste is the usual macOS tool; on Linux `xclip -selection
		// clipboard -t image/png -o` is the equivalent, but which clipboard
		// tool exists varies by session type, so this stays unsupported
		// rather than guessing wrong.
		return "", nil
	}
	f, err := os.CreateTemp("", "pi-clip-*.png")
	if err != nil {
		return "", err
	}
	path := f.Name()
	f.Close()

	// AppleScript rather than pngpaste: it is always present. `the clipboard
	// as «class PNGf»` throws when the clipboard is not an image, which is
	// how "no image" is detected.
	script := fmt.Sprintf(
		`set p to (POSIX file %q)
set d to (the clipboard as «class PNGf»)
set fh to open for access p with write permission
set eof fh to 0
write d to fh
close access fh`, path)
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		os.Remove(path)
		return "", nil // not an image; not an error worth reporting
	}
	if st, err := os.Stat(path); err != nil || st.Size() == 0 {
		os.Remove(path)
		return "", nil
	}
	return path, nil
}
