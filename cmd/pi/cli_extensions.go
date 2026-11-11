package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/extension"
	"github.com/notshekhar/pi/internal/core/mcp"
)

// `pi install|link|remove|extensions|enable|disable` — loop's ext-commands.ts.
//
// Three kinds of extension answer to these names: pi's built-ins, pi's Go
// extensions (ext_install.go), and loop's installed TypeScript extensions,
// which share the home and are listed and toggled here but only run in loop.

func installExtensionCommand(c *cliCall) int {
	if len(c.args.positional) == 0 {
		// loop's bare install re-syncs npm dependencies; a Go extension is a
		// binary with nothing to sync, so there is nothing to do bare.
		fmt.Fprintf(os.Stderr, "usage: %s install <path to a Go main package>\n", productName)
		return 1
	}
	src, err := resolveExtensionSource(c.cwd(), c.args.positional[0])
	if err == nil {
		var name, bin string
		if name, bin, err = buildGoExtension(src); err == nil {
			if err = registerGoExtension(name, mcp.ServerConfig{Command: bin}); err == nil {
				fmt.Printf("Installed %s\n", name)
				return 0
			}
		}
	}
	fmt.Fprintf(os.Stderr, "Install failed: %s\n", err)
	return 1
}

func linkExtensionCommand(c *cliCall) int {
	if len(c.args.positional) == 0 {
		fmt.Fprintf(os.Stderr, "usage: %s link <path>\n", productName)
		return 1
	}
	dir, err := resolveExtensionSource(c.cwd(), c.args.positional[0])
	if err == nil {
		err = requireGoToolchain()
	}
	if err == nil {
		err = registerGoExtension(filepath.Base(dir), linkedExtensionServer(dir))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Link failed: %s\n", err)
		return 1
	}
	fmt.Printf("Linked %s (dev mode)\n", filepath.Base(dir))
	return 0
}

func listExtensionsCommand(*cliCall) int {
	fmt.Printf("Built-in (toggle with: %s enable|disable <name>):\n", productName)
	state := config.ExtensionState()
	for _, e := range extension.All() {
		fmt.Printf("  [%s] %s  — %s\n", onOff(extension.IsEnabled(e, state)), e.Name(), e.About())
	}
	goExts, records := goExtensions(), loopExtensionRecords()
	if len(goExts) == 0 && len(records) == 0 {
		return 0
	}
	fmt.Println("\nInstalled:")
	for _, e := range goExts {
		linked := ""
		if e.linked {
			linked = " (linked)"
		}
		fmt.Printf("  [%s] %s%s  — %s\n", onOff(!e.cfg.Disabled()), e.name, linked, e.source)
	}
	for _, r := range records {
		version, linked := "", ""
		if r.Version != "" {
			version = "@" + r.Version
		}
		if r.LinkPath != "" {
			linked = " (linked)"
		}
		fmt.Printf("  [%s] %s%s%s  — %s (loop only)\n", onOff(r.Enabled), r.Name, version, linked, r.Source)
	}
	return 0
}

// onOff is loop's fixed-width state column.
func onOff(on bool) string {
	if on {
		return "on "
	}
	return "off"
}

// loopExtensionRecords are loop's installed extensions, by name.
func loopExtensionRecords() []config.ExtensionRecord {
	var out []config.ExtensionRecord
	for _, r := range config.ExtensionRecords() {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func removeExtensionCommand(c *cliCall) int {
	if len(c.args.positional) == 0 {
		fmt.Fprintf(os.Stderr, "usage: %s remove <name>\n", productName)
		return 1
	}
	name := c.args.positional[0]
	ext, ok := findGoExtension(name)
	if !ok {
		// loop's own extensions are removed by loop, which owns their files.
		fmt.Printf("Not installed: %s\n", name)
		return 0
	}
	if err := removeGoExtension(ext); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("Removed %s\n", name)
	return 0
}

func setExtensionCommand(c *cliCall, on bool) int {
	verb, done := "disable", "Disabled"
	if on {
		verb, done = "enable", "Enabled"
	}
	if len(c.args.positional) == 0 {
		fmt.Fprintf(os.Stderr, "usage: %s %s <name>\n", productName, verb)
		return 1
	}
	name := c.args.positional[0]
	ok, builtin, err := setExtensionEnabled(name, on)
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	case builtin:
		fmt.Printf("%s %s (built-in)\n", done, name)
	case ok:
		fmt.Printf("%s %s\n", done, name)
	default:
		fmt.Printf("Not installed: %s\n", name)
	}
	return 0
}

// setExtensionEnabled flips whichever kind of extension has the name.
func setExtensionEnabled(name string, on bool) (found, builtin bool, err error) {
	if _, ok := extension.Find(name); ok {
		return true, true, config.SetBuiltinEnabled(name, on)
	}
	if _, ok := findGoExtension(name); ok {
		_, err := config.SetMCPServerEnabled(name, on)
		return true, false, err
	}
	if rec, ok := config.ExtensionRecords()[name]; ok {
		rec.Enabled = on
		return true, false, config.PutExtensionRecord(rec)
	}
	return false, false, nil
}
