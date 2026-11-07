package main

import (
	"path/filepath"
	"strings"
)

// cmux wraps every `pi` on PATH as though it were pi-mono's coding agent, and
// launches it as `pi -e <cmux-session.ts> <args…>` to load a TypeScript
// extension. pi's extensions are Go programs and it reports to cmux itself
// (internal/core/cmux), so that extension has nothing to add here. Left in,
// the flag parser stops at the unknown -e and prints its usage instead of
// running the command — `pi`, `pi upgrade`, everything.

// scriptExtensions are the extension files pi-mono loads and pi cannot.
var scriptExtensions = map[string]bool{".ts": true, ".js": true, ".mjs": true, ".cjs": true}

var extensionFlags = map[string]bool{"-e": true, "--e": true, "-extension": true, "--extension": true}

// withoutScriptExtensions drops every `-e <file.ts>` (or `--extension=…`) whose
// file is a script extension, and leaves every other argument where it was.
func withoutScriptExtensions(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		name, value, inline := strings.Cut(args[i], "=")
		if !extensionFlags[name] {
			out = append(out, args[i])
			continue
		}
		if !inline && i+1 < len(args) {
			value = args[i+1]
		}
		if !scriptExtensions[strings.ToLower(filepath.Ext(value))] {
			out = append(out, args[i])
			continue
		}
		if !inline {
			i++
		}
	}
	return out
}
