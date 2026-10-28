package extension

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// memStore is a settings store that lives for one test.
type memStore map[string]string

func (m memStore) Get(key, fallback string) string {
	if v, ok := m[key]; ok && v != "" {
		return v
	}
	return fallback
}
func (m memStore) Set(key, value string) error { m[key] = value; return nil }

func personaNamed(t *testing.T, name string) (*persona, memStore) {
	t.Helper()
	for _, e := range All() {
		p, ok := e.(*persona)
		if !ok || p.Name() != name {
			continue
		}
		// A copy, so a test cannot leave a mode set on the registered one.
		clone := *p
		store := memStore{}
		clone.UseStore(store)
		return &clone, store
	}
	t.Fatalf("no %s extension registered", name)
	return nil, nil
}

// Only the ACTIVE level's intensity row survives; every other line is a rule
// and is kept verbatim.
func TestFilterKeepsOnlyTheActiveModesRows(t *testing.T) {
	p, store := personaNamed(t, "caveman")
	store.Set("mode", "ultra")
	prompt := p.SystemPrompt("")

	if !strings.Contains(prompt, "CAVEMAN MODE ACTIVE — level: ultra") {
		t.Errorf("banner missing:\n%s", prompt[:min(200, len(prompt))])
	}
	// The frontmatter is metadata for a skill loader, not for a prompt.
	if strings.Contains(prompt, "description: >") {
		t.Error("frontmatter leaked into the prompt")
	}
	for _, row := range []string{"| **lite**", "| **full**"} {
		if strings.Contains(prompt, row) {
			t.Errorf("a non-active intensity row survived: %s", row)
		}
	}
	if !strings.Contains(prompt, "| **ultra**") {
		t.Error("the active intensity row was dropped")
	}
}

func TestOffInjectsNothing(t *testing.T) {
	p, store := personaNamed(t, "ponytail")
	store.Set("mode", "off")
	if got := p.SystemPrompt(""); got != "" {
		t.Errorf("off still injected %d chars", len(got))
	}
}

// The phrase turns it off; merely mentioning it does not.
func TestDeactivationPhrase(t *testing.T) {
	p, store := personaNamed(t, "caveman")
	store.Set("mode", "full")

	p.OnBeforeTurn("how do I stop caveman in a script?")
	if p.mode() != "full" {
		t.Error("a message mentioning the phrase switched it off")
	}
	p.OnBeforeTurn("  Stop Caveman.  ")
	if p.mode() != "off" {
		t.Errorf("the phrase did not switch it off, mode = %q", p.mode())
	}
}

func TestCommandSwitchesAndReportsMode(t *testing.T) {
	p, store := personaNamed(t, "ponytail")
	cmd := p.Commands()[0]

	out, _, err := cmd.Run(context.Background(), "", "")
	if err != nil || !strings.Contains(out, "ponytail mode: full") {
		t.Errorf("status: %q %v", out, err)
	}

	if _, _, err := cmd.Run(context.Background(), "", "ultra"); err != nil {
		t.Fatal(err)
	}
	if store["mode"] != "ultra" {
		t.Errorf("mode = %q", store["mode"])
	}

	// A typo must be an error, not a silent switch-off — normalize() answers
	// "off" for anything unknown, and using it here would lose the mode.
	if _, _, err := cmd.Run(context.Background(), "", "ulra"); err == nil {
		t.Fatal("a typo was accepted")
	}
	if store["mode"] != "ultra" {
		t.Errorf("a rejected mode still changed the setting: %q", store["mode"])
	}
}

// A table row whose label is not a mode name is a rule, and belongs in every
// level.
func TestNonModeTableRowsSurvive(t *testing.T) {
	p, store := personaNamed(t, "caveman")
	for _, mode := range []string{"lite", "full", "ultra"} {
		store.Set("mode", mode)
		if !strings.Contains(p.SystemPrompt(""), "Respond terse like smart caveman") {
			t.Errorf("%s lost the body's opening rule", mode)
		}
	}
}

// rtk without its binary must be a silent no-op: enabling it on a machine
// that does not have rtk must never break bash.
func TestRtkWithoutBinaryRewritesNothing(t *testing.T) {
	r := &rtk{store: memStore{}}
	// Force the probe to "not installed" without touching PATH.
	r.once.Do(func() {})

	if got := r.RewriteCall("bash", map[string]any{"command": "git status"}); got != nil {
		t.Errorf("rewrote without the binary: %v", got)
	}
	if r.Status() != "no binary" {
		t.Errorf("status = %q", r.Status())
	}
}

// It only touches bash, and only when rewriting is switched on.
func TestRtkScope(t *testing.T) {
	store := memStore{}
	r := &rtk{store: store}
	r.once.Do(func() { r.installed = true })

	if got := r.RewriteCall("read", map[string]any{"path": "a.go"}); got != nil {
		t.Errorf("rewrote a non-bash call: %v", got)
	}
	if got := r.RewriteCall("bash", map[string]any{}); got != nil {
		t.Errorf("rewrote a call with no command: %v", got)
	}

	store["enabled"] = "off"
	if got := r.RewriteCall("bash", map[string]any{"command": "git status"}); got != nil {
		t.Errorf("rewrote while switched off: %v", got)
	}
	if r.Status() != "off" {
		t.Errorf("status = %q", r.Status())
	}
}

// A heredoc is left alone — line-oriented rewriting mangles them.
func TestRtkSkipsHeredocs(t *testing.T) {
	r := &rtk{store: memStore{}}
	r.once.Do(func() { r.installed = true })
	if got := r.rewrite("cat <<EOF\nhi\nEOF"); got != "" {
		t.Errorf("rewrote a heredoc: %q", got)
	}
}

// /rtk-toggle flips the switch and says which way.
func TestRtkToggle(t *testing.T) {
	store := memStore{}
	r := &rtk{store: store}
	var toggle Command
	for _, c := range r.Commands() {
		if c.Name == "rtk-toggle" {
			toggle = c
		}
	}
	if toggle.Run == nil {
		t.Fatal("no /rtk-toggle command")
	}
	out, _, err := toggle.Run(context.Background(), "", "")
	if err != nil || !strings.Contains(out, "off") {
		t.Errorf("first toggle: %q %v", out, err)
	}
	if store["enabled"] != "off" {
		t.Errorf("enabled = %q", store["enabled"])
	}
	out, _, _ = toggle.Run(context.Background(), "", "")
	if !strings.Contains(out, "on") {
		t.Errorf("second toggle: %q", out)
	}
}

// Against the REAL binary when one is on PATH. rtk's exit protocol is the
// part that cannot be checked by reading this file: exit 3 means "a rule says
// ask" and still carries a rewrite on stdout, and treating it as a failure —
// the obvious reading of a non-zero exit — silently disables the extension
// for exactly the commands it is most useful on.
func TestRtkAgainstTheRealBinary(t *testing.T) {
	if _, err := exec.LookPath("rtk"); err != nil {
		t.Skip("rtk not on PATH")
	}
	r := &rtk{store: memStore{}}
	if !r.available() {
		t.Skip("rtk on PATH but not runnable")
	}

	got := r.RewriteCall("bash", map[string]any{"command": "git status"})
	if got == nil {
		t.Fatal("rtk did not rewrite `git status`")
	}
	command, _ := got["command"].(string)
	if !strings.HasPrefix(command, "rtk ") {
		t.Errorf("rewrote to %q, want an rtk-prefixed command", command)
	}
	// rtk warns on stderr when no hook is installed; that must not end up in
	// the command.
	if strings.Contains(command, "[rtk]") {
		t.Errorf("stderr leaked into the command: %q", command)
	}

	// A command rtk has no equivalent for (exit 1) is left alone.
	if got := r.RewriteCall("bash", map[string]any{"command": "echo hello"}); got != nil {
		t.Errorf("rewrote a command rtk has no equivalent for: %v", got)
	}
}
