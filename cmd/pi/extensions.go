package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/extension"
	"github.com/notshekhar/pi/internal/modules/core/mcp"
	"github.com/notshekhar/pi/internal/modules/tui"
)

// /extensions and /install.
//
// See internal/modules/core/extension for why extensions are Go rather than
// a scripting language, and why external ones are separate PROCESSES.

// activeExtensions are the enabled builtins.
func activeExtensions() []extension.Extension {
	return extension.Enabled(config.LoadSettings().ExtensionState())
}

// extensionCommands are the slash commands the enabled extensions add.
//
// Looked up at DISPATCH time rather than baked into the registry: an
// extension can be switched off mid-session, and a command that kept working
// after its extension was disabled would be the clearest possible sign the
// switch does nothing.
func extensionCommand(name string) (extension.Command, bool) {
	for _, c := range extension.CommandsFrom(activeExtensions()) {
		if c.Name == name {
			return c, true
		}
	}
	return extension.Command{}, false
}

// extensionUI lets an extension command open the app's own picker and prompt.
//
// The adapter lives here, at the one place the two layers meet: the extension
// package declares what it needs and knows nothing about the terminal, and
// the terminal knows nothing about extensions.
type extensionUI struct{ t *repl }

func (u extensionUI) Select(title string, items []extension.Item, current string) string {
	rows := make([]tui.Item, 0, len(items))
	for _, i := range items {
		rows = append(rows, tui.Item{Value: i.Value, Label: i.Label, Description: i.Description})
	}
	if len(rows) == 0 {
		return ""
	}
	choice := u.t.app.Pick(title, rows, indexOf(rows, current), current)
	if choice == nil {
		return ""
	}
	return choice.Value
}

func (u extensionUI) Prompt(label, initial string) string {
	return u.t.app.Prompt(label, initial)
}

// runExtensionCommand executes one and shows whatever it produced.
func (t *repl) runExtensionCommand(parent context.Context, cmd extension.Command, rest string) {
	go func() {
		// An extension command may open a picker, and Pick blocks on the
		// render goroutine — so this one must be marked as owning the panel,
		// or a picker opened from inside it would spawn a second goroutine
		// and race this one for the keyboard.
		t.inPanel.Store(true)
		defer t.inPanel.Store(false)
		out, prompt, err := cmd.Run(parent, t.cfg.CWD, rest)
		if err != nil {
			t.fail("%s", err)
			return
		}
		if out != "" {
			t.app.Do(func() { t.app.Print(strings.Split(out, "\n")...) })
		}
		// A returned prompt is sent as though the user had typed it, which
		// is how a command like /wayfinder turns into a real turn.
		if prompt != "" {
			t.app.Events <- tui.Event{Kind: tui.EvSubmit, Text: prompt}
		}
	}()
}

// extensionsCmd is `/extensions`: the extension manager.
//
// The list is not just a toggle. loop's panel offers an action menu per
// extension — enable, disable, info, and uninstall for the ones that were
// installed rather than built in — and an install row at the top, because
// "what is this thing and where did it come from" is asked far more often
// than it is switched.
func (t *repl) extensionsCmd(rest string) {
	if trimmed := strings.TrimSpace(rest); trimmed != "" {
		if spec, found := strings.CutPrefix(trimmed, "install"); found {
			t.installExtension(strings.TrimSpace(spec))
			return
		}
		t.toggleExtension(trimmed)
		return
	}
	const installRow = "\x00install"
	t.manage(func() (string, []tui.Item) {
		state := config.LoadSettings().ExtensionState()
		all := extension.All()
		items := []tui.Item{{
			Value:       installRow,
			Label:       "+ install",
			Description: "compile a Go main package that speaks MCP over stdio",
		}}
		for _, e := range all {
			// A filled dot for on, hollow for off — readable down the column
			// at a glance, which "(on)"/"(off)" is not.
			marker := "○"
			if extension.IsEnabled(e, state) {
				marker = "●"
			}
			label := marker + " " + e.Name() + "  ·  built-in"
			if status := extension.StatusOf(e); status != "" {
				label = marker + " " + e.Name() + " (" + status + ")  ·  built-in"
			}
			items = append(items, tui.Item{Value: e.Name(), Label: label, Description: e.About()})
		}
		return "Extensions (type to filter, Esc to close)", items
	}, func(choice tui.Item) bool {
		if choice.Value == installRow {
			if spec := strings.TrimSpace(t.ask("install (path to a Go main package)", "")); spec != "" {
				t.installExtension(spec)
			}
			return keepPanel
		}
		t.extensionActions(choice.Value)
		return keepPanel
	})
}

// extensionActions is the per-extension menu.
func (t *repl) extensionActions(name string) {
	e, ok := extension.Find(name)
	if !ok {
		return
	}
	on := extension.IsEnabled(e, config.LoadSettings().ExtensionState())
	toggle, toggleAbout := "enable", "load it now"
	if on {
		toggle, toggleAbout = "disable", "stop loading it"
	}
	action := t.choose(name+" (built-in)",
		tui.Item{Value: toggle, Label: toggle, Description: toggleAbout},
		tui.Item{Value: "info", Label: "info", Description: "show details"},
	)
	if action == nil {
		return
	}
	switch action.Value {
	case "enable", "disable":
		t.setExtension(name, action.Value == "enable")
	case "info":
		status := extension.StatusOf(e)
		t.app.Do(func() {
			th := t.app.Theme()
			lines := []string{
				th.Fg(tui.SlotAccent, th.Bold(name)) + th.Fg(tui.SlotDim, " (built-in)"),
				"  " + th.Fg(tui.SlotMuted, e.About()),
				"  " + th.Fg(tui.SlotDim, fmt.Sprintf("enabled: %v", on)),
			}
			if status != "" {
				lines = append(lines, "  "+th.Fg(tui.SlotDim, "mode: "+status))
			}
			if _, adds := e.(extension.CommandProvider); adds {
				var names []string
				for _, c := range extension.CommandsFrom([]extension.Extension{e}) {
					names = append(names, "/"+c.Name)
				}
				lines = append(lines, "  "+th.Fg(tui.SlotDim, "commands: "+strings.Join(names, " ")))
			}
			t.app.Print(lines...)
		})
	}
}

// toggleExtension flips one, for the argument form.
func (t *repl) toggleExtension(name string) {
	e, ok := extension.Find(name)
	if !ok {
		t.fail("no extension named %q — /extensions to list them", name)
		return
	}
	t.setExtension(name, !extension.IsEnabled(e, config.LoadSettings().ExtensionState()))
}

// setExtension records an explicit choice and applies it immediately.
//
// Both directions are stored, never just the off-list: an extension that
// ships enabled cannot be expressed as "absent from off", and an extension
// that ships disabled cannot be turned on that way at all.
func (t *repl) setExtension(name string, on bool) {
	if err := config.Update(func(s *config.Settings) {
		s.ExtensionsOn = without(s.ExtensionsOn, name)
		s.ExtensionsOff = without(s.ExtensionsOff, name)
		if on {
			s.ExtensionsOn = append(s.ExtensionsOn, name)
		} else {
			s.ExtensionsOff = append(s.ExtensionsOff, name)
		}
	}); err != nil {
		t.fail("extensions: %s", err)
		return
	}
	// Tools and prompts take effect on the next turn; the completion catalog
	// has to be rebuilt here, or a just-enabled extension's command is
	// runnable but not offerable.
	t.applySettings()
	t.app.Do(func() { t.app.SetSources(t.commandItems(), fileItems(t.cfg.CWD)) })
	if on {
		t.dim("enabled %s", name)
		return
	}
	t.dim("disabled %s", name)
}

// without returns list with name removed.
func without(list []string, name string) []string {
	kept := make([]string, 0, len(list))
	for _, n := range list {
		if n != name {
			kept = append(kept, n)
		}
	}
	return kept
}

// installExtension compiles a Go program and registers it as an external
// extension.
//
// `go build` rather than anything cleverer: an external extension is an
// ordinary Go main package that speaks MCP over stdio, so installing one is
// compiling it and remembering where the binary went.
func (t *repl) installExtension(rest string) {
	src := strings.TrimSpace(rest)
	if src == "" {
		t.dim("usage: /install <path to a Go main package>")
		return
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.dim("/install needs the go toolchain on PATH")
		return
	}
	src = tui.UnquotePath(src)
	if !filepath.IsAbs(src) {
		src = filepath.Join(t.cfg.CWD, src)
	}
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		t.fail("install: %s is not a directory", src)
		return
	}

	name := filepath.Base(src)
	dir, err := config.Dir()
	if err != nil {
		t.fail("install: %s", err)
		return
	}
	binDir := filepath.Join(dir, "extensions")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.fail("install: %s", err)
		return
	}
	out := filepath.Join(binDir, name)

	// Building is slow and must not block the render loop.
	go func() {
		t.dim("building %s…", name)
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Dir = src
		if combined, err := cmd.CombinedOutput(); err != nil {
			t.fail("install: %s", strings.TrimSpace(string(combined)))
			return
		}
		// Registered as an MCP server, which is what an external extension
		// IS — that is the whole reason it speaks that protocol.
		if err := config.Update(func(s *config.Settings) {
			if s.MCPServers == nil {
				s.MCPServers = map[string]mcp.ServerConfig{}
			}
			s.MCPServers[name] = mcp.ServerConfig{Command: out}
		}); err != nil {
			t.fail("install: %s", err)
			return
		}
		t.dim("installed %s — /mcp reconnect to load its tools", name)
	}()
}
