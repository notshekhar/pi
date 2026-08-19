package main

import (
	"strings"
	"testing"
	"time"

	"github.com/notshekhar/pi/internal/modules/core/extension"
	"github.com/notshekhar/pi/internal/modules/core/schedule"
	"github.com/notshekhar/pi/internal/modules/core/session"
)

// Dispatch, /help, and the palette all read the same table. They used to be
// three hand-kept lists, and the palette had silently fallen a third behind.
func TestPaletteCoversEveryCommand(t *testing.T) {
	items := slashItems()
	// The palette is the builtins PLUS whatever the enabled extensions add;
	// an extension command you have to already know about is not
	// discoverable.
	want := len(commands()) + len(extension.CommandsFrom(activeExtensions()))
	if len(items) != want {
		t.Fatalf("palette has %d entries, want %d", len(items), want)
	}
	for _, c := range commands() {
		if lookupCommand(c.name) == nil {
			t.Errorf("/%s is in the table but not dispatchable", c.name)
		}
	}
}

// An extension must not be able to shadow a core command: dispatch checks
// the builtins first, and this guards the table against a name collision
// being introduced silently.
func TestExtensionsDoNotShadowCoreCommands(t *testing.T) {
	for _, c := range extension.CommandsFrom(extension.All()) {
		if lookupCommand(c.Name) != nil {
			t.Errorf("extension command /%s collides with a builtin", c.Name)
		}
	}
}

// Every builtin extension has to be findable and describable, since that is
// all the manager shows.
func TestBuiltinExtensionsAreComplete(t *testing.T) {
	all := extension.All()
	if len(all) == 0 {
		t.Fatal("no builtin extensions registered")
	}
	for _, e := range all {
		if e.Name() == "" || e.About() == "" {
			t.Errorf("extension %T is missing a name or description", e)
		}
		if _, ok := extension.Find(e.Name()); !ok {
			t.Errorf("%s is registered but not findable", e.Name())
		}
	}
}

// Extensions are OPT-IN. A builtin with a persona — caveman rewrites every
// reply — must never be on until someone says so.
func TestExtensionsAreOptIn(t *testing.T) {
	if on := extension.Enabled(nil); len(on) != 0 {
		names := make([]string, 0, len(on))
		for _, e := range on {
			names = append(names, e.Name())
		}
		t.Errorf("extensions enabled with no stored choice: %v", names)
	}
}

// Enabling takes effect, and disabling takes the extension's commands with
// it — a command that kept working would be the clearest sign the switch
// does nothing.
func TestEnableAndDisable(t *testing.T) {
	all := extension.All()
	if len(all) == 0 {
		t.Skip("no extensions registered")
	}
	name := all[0].Name()

	on := extension.Enabled(map[string]bool{name: true})
	if len(on) != 1 || on[0].Name() != name {
		t.Fatalf("enabling %s gave %v", name, on)
	}
	for _, e := range extension.Enabled(map[string]bool{name: false}) {
		if e.Name() == name {
			t.Fatalf("%s survived being disabled", name)
		}
	}
}

// Every command needs a handler and a description; a nil handler is a panic
// waiting for whoever types the name.
func TestEveryCommandIsComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range commands() {
		if c.run == nil {
			t.Errorf("/%s has no handler", c.name)
		}
		if strings.TrimSpace(c.description) == "" {
			t.Errorf("/%s has no description", c.name)
		}
		if seen[c.name] {
			t.Errorf("/%s is registered twice", c.name)
		}
		seen[c.name] = true
	}
}

// Lookup takes the name with or without the slash, because dispatch has the
// slash and tests and aliases do not.
func TestLookupAcceptsEitherForm(t *testing.T) {
	if lookupCommand("/help") == nil || lookupCommand("help") == nil {
		t.Fatal("lookup should accept both /help and help")
	}
	if lookupCommand("nope") != nil {
		t.Fatal("unknown command resolved")
	}
}

// The aliases loop teaches must exist here, or a user types the name they
// know and is told it does not exist.
func TestLoopAliasesExist(t *testing.T) {
	for _, name := range []string{
		"effort", "sessions", "resume", "rename", "name", "cwd", "cd",
		"bg", "background", "reminders", "reminder", "exit", "quit",
		"release-notes", "changelog", "paste", "attach", "perms", "config",
	} {
		if lookupCommand(name) == nil {
			t.Errorf("/%s is missing", name)
		}
	}
}

// Forks nest under the session they came from.
func TestTreeNestsForksUnderTheirParent(t *testing.T) {
	base := time.Now()
	metas := []session.Meta{
		{ID: "root", Title: "root", Created: base},
		{ID: "child", Title: "child", Parent: "root", Created: base.Add(time.Minute)},
		{ID: "grandchild", Title: "grandchild", Parent: "child", Created: base.Add(2 * time.Minute)},
		{ID: "other", Title: "other", Created: base.Add(3 * time.Minute)},
	}
	rows := treeRows(metas)
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4", len(rows))
	}
	want := []struct{ id, prefix string }{
		{"root", ""},
		{"child", "└─ "},
		{"grandchild", "  └─ "},
		{"other", ""},
	}
	for i, w := range want {
		if rows[i].meta.ID != w.id || rows[i].prefix != w.prefix {
			t.Errorf("row %d = %s %q, want %s %q", i, rows[i].meta.ID, rows[i].prefix, w.id, w.prefix)
		}
	}
}

// A fork whose parent is not in this listing must still appear. Dropping it
// loses the session entirely, which is worse than showing it unattached.
func TestTreeShowsOrphansAsRoots(t *testing.T) {
	rows := treeRows([]session.Meta{{ID: "orphan", Parent: "elsewhere"}})
	if len(rows) != 1 || rows[0].prefix != "" {
		t.Fatalf("orphan was not shown as a root: %+v", rows)
	}
}

// Durations are what /timer and /reminder are typed in.
func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"30s":    30 * time.Second,
		"5m":     5 * time.Minute,
		"1h30m":  90 * time.Minute,
		"2d":     48 * time.Hour,
		"1d2h3m": 26*time.Hour + 3*time.Minute,
		// A bare number is minutes: "/timer 20" means twenty minutes, and
		// time.ParseDuration would read it as twenty nanoseconds.
		"20": 20 * time.Minute,
	}
	for in, want := range cases {
		got, ok := parseDuration(in)
		if !ok || got != want {
			t.Errorf("parseDuration(%q) = %v %v, want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "abc", "5x", "m", "-3m"} {
		if _, ok := parseDuration(bad); ok {
			t.Errorf("parseDuration(%q) should have failed", bad)
		}
	}
}

// The three spellings a one-time reminder accepts, and the one it must
// refuse: a bare number could be minutes or a clock time, and a reminder that
// fires at the wrong hour is worse than one that will not be set.
func TestParseOnceWhen(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.Local)

	at, ok := parseOnceWhen("10m", now)
	if !ok || !at.Equal(now.Add(10*time.Minute)) {
		t.Errorf("duration: got %v ok=%v", at, ok)
	}

	at, ok = parseOnceWhen("18:30", now)
	if !ok || at.Hour() != 18 || at.Minute() != 30 || at.Day() != 18 {
		t.Errorf("clock today: got %v ok=%v", at, ok)
	}

	// A time that has already passed today means tomorrow.
	at, ok = parseOnceWhen("09:00", now)
	if !ok || at.Day() != 19 {
		t.Errorf("clock tomorrow: got %v ok=%v", at, ok)
	}

	at, ok = parseOnceWhen("2026-06-15 09:00", now)
	if !ok || at.Year() != 2026 || at.Month() != time.June || at.Day() != 15 {
		t.Errorf("stamp: got %v ok=%v", at, ok)
	}

	for _, bad := range []string{"", "20", "later", "25:00", "2026-13-01 09:00"} {
		if at, ok := parseOnceWhen(bad, now); ok && bad != "2026-13-01 09:00" {
			t.Errorf("parseOnceWhen(%q) accepted as %v", bad, at)
		}
	}
}

// A cron reminder missed while the machine slept must fire ONCE and be
// rescheduled from now, not fire once per interval it slept through.
func TestDueCronReschedulesFromNow(t *testing.T) {
	now := time.Now()
	cron, err := schedule.Parse("0 * * * * *")
	if err != nil {
		t.Fatal(err)
	}
	next := cron.Next(now)
	if !next.After(now) {
		t.Fatal("next firing is not in the future")
	}
	if next.Sub(now) > time.Minute {
		t.Fatalf("rescheduled too far out: %v", next.Sub(now))
	}
}

// The transcript is full of angle brackets from code; pasting it raw into
// HTML both breaks the page and lets a session inject markup into it.
func TestExportHTMLEscapes(t *testing.T) {
	out := exportHTML("m", `<script>alert("x")</script> & more`)
	if strings.Contains(out, "<script>") {
		t.Fatal("raw markup survived into the page")
	}
	for _, want := range []string{"&lt;script&gt;", "&amp; more"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}
