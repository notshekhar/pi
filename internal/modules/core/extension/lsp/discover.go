package lsp

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"sync"
)

// Finding a server to run.
//
// The project's own copy wins over one on PATH, because the project's
// toolchain is the one whose answers match its build. Nothing is downloaded —
// see the registry's note.

// versionCache remembers `--version` answers. Launching a binary to ask its
// version costs a process, and the discovery path runs for every file.
var (
	versionMu    sync.Mutex
	versionCache = map[string]int{}
)

// versionPattern finds the first "N.M" in a version banner: "Version 5.9.3",
// "tsc 7.0.2", "gopls v0.16.1".
var versionPattern = regexp.MustCompile(`(\d+)\.\d+`)

// majorVersion reads a binary's major version, or -1 when it cannot be read.
func majorVersion(bin string) int {
	versionMu.Lock()
	if v, ok := versionCache[bin]; ok {
		versionMu.Unlock()
		return v
	}
	versionMu.Unlock()

	major := -1
	cmd := exec.Command(bin, "--version")
	if out, err := cmd.CombinedOutput(); err == nil || len(out) > 0 {
		if m := versionPattern.FindSubmatch(out); m != nil {
			if n, err := strconv.Atoi(string(m[1])); err == nil {
				major = n
			}
		}
	}

	versionMu.Lock()
	versionCache[bin] = major
	versionMu.Unlock()
	return major
}

// usable reports whether a candidate is new enough to speak LSP.
//
// An unreadable version FAILS when a minimum is declared. Launching a binary
// that does not understand `--lsp` costs a failed handshake and takes the
// whole language down with it, which is worse than reporting no server.
func usable(bin string, d ServerDef) bool {
	if d.MinMajorVersion == 0 {
		return true
	}
	major := majorVersion(bin)
	return major >= d.MinMajorVersion
}

// onPath resolves a binary name, or "" when it is not there.
func onPath(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return path
}

// Resolve finds a runnable command for a server, or "" when there is none.
func Resolve(d ServerDef, root string) string {
	// The project's own node_modules/.bin first: a repo pinning a server
	// version means that version's answers are the ones its build agrees
	// with.
	for _, name := range d.BinNames {
		if runtime.GOOS == "windows" {
			name += ".cmd"
		}
		local := filepath.Join(root, "node_modules", ".bin", name)
		if _, err := exec.LookPath(local); err == nil && usable(local, d) {
			return local
		}
	}
	for _, name := range d.BinNames {
		if bin := onPath(name); bin != "" && usable(bin, d) {
			return bin
		}
	}
	return ""
}

// RequirementsMet reports whether the server's toolchain is present.
//
// Checked before anything is launched: a server whose runtime is missing
// cannot run, and a jdtls without a Java 21 fails at class-load time with an
// error nobody can act on.
func RequirementsMet(d ServerDef) bool {
	for _, bin := range d.Requires {
		if onPath(bin) == "" {
			return false
		}
		if min, ok := d.RequiresMinVersion[bin]; ok && majorVersion(bin) < min {
			return false
		}
	}
	return true
}

// SpecFor builds a launchable spec, or ok=false when the server is not
// available on this machine.
func SpecFor(d ServerDef, root string) (Spec, bool) {
	if !RequirementsMet(d) {
		return Spec{}, false
	}
	bin := Resolve(d, root)
	if bin == "" {
		return Spec{}, false
	}

	command, args := bin, append([]string{}, d.Args...)
	switch d.Runtime {
	case "java":
		// The server is an executable JAR, so java runs it — and the JVM
		// flags go BEFORE -jar, or they are arguments to the application
		// instead of to the JVM.
		command = "java"
		args = append(append(append([]string{}, d.JVMArgs...), "-jar", bin), d.Args...)
	case "node":
		// A JS shim. Run it directly when it is executable, which the
		// node_modules/.bin wrappers are.
	}

	return Spec{
		Name:       d.Key,
		Command:    command,
		Args:       args,
		LanguageID: d.LanguageIDFor,
	}, true
}
