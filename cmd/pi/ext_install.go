package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/home"
	"github.com/notshekhar/pi/internal/core/mcp"
)

// pi's external extensions: Go main packages that speak MCP over stdio.
//
// Installing one is compiling it into ~/.loop/extensions/<name> and
// registering the binary as a global MCP server — that is what an external
// extension IS, which is why it speaks the protocol. Linking one registers
// `go -C <dir> run .` instead, so every connect builds the current source:
// loop's dev mode, without a copy to keep in sync.
//
// The MCP entry is the only record kept. pi must not write loop's
// extensions.json records for these — loop would try to load them as
// TypeScript packages.

// goExtension is one installed or linked Go extension.
type goExtension struct {
	name   string
	source string // the binary, or the linked package directory
	linked bool
	cfg    mcp.ServerConfig
}

func extensionsBinDir() string { return home.Path("extensions") }

// resolveExtensionSource is src made absolute against cwd, required to be a
// directory.
func resolveExtensionSource(cwd, src string) (string, error) {
	if !filepath.IsAbs(src) {
		src = filepath.Join(cwd, src)
	}
	src = filepath.Clean(src)
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s is not a directory", src)
	}
	return src, nil
}

func requireGoToolchain() error {
	if _, err := exec.LookPath("go"); err != nil {
		return errors.New("needs the go toolchain on PATH")
	}
	return nil
}

// buildGoExtension compiles the package at src into the extensions
// directory and returns its name and the binary's path.
func buildGoExtension(src string) (name, bin string, err error) {
	if err := requireGoToolchain(); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(extensionsBinDir(), 0o700); err != nil {
		return "", "", err
	}
	name = filepath.Base(src)
	bin = filepath.Join(extensionsBinDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", "", errors.New(strings.TrimSpace(string(out)))
	}
	return name, bin, nil
}

// linkedExtensionServer runs the package's current source on every connect.
func linkedExtensionServer(dir string) mcp.ServerConfig {
	return mcp.ServerConfig{Command: "go", Args: []string{"-C", dir, "run", "."}}
}

// registerGoExtension records the extension as a global MCP server.
func registerGoExtension(name string, cfg mcp.ServerConfig) error {
	return config.AddMCPServer(name, cfg)
}

// asGoExtension recognizes an MCP entry that install or link wrote.
func asGoExtension(name string, cfg mcp.ServerConfig) (goExtension, bool) {
	if cfg.Command == "go" && len(cfg.Args) == 4 && cfg.Args[0] == "-C" && cfg.Args[2] == "run" && cfg.Args[3] == "." {
		return goExtension{name: name, source: cfg.Args[1], linked: true, cfg: cfg}, true
	}
	if filepath.Dir(cfg.Command) == extensionsBinDir() {
		return goExtension{name: name, source: cfg.Command, cfg: cfg}, true
	}
	return goExtension{}, false
}

// goExtensions lists the installed and linked Go extensions, by name.
func goExtensions() []goExtension {
	var out []goExtension
	for name, cfg := range config.GlobalMCPServers() {
		if ext, ok := asGoExtension(name, cfg); ok {
			out = append(out, ext)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func findGoExtension(name string) (goExtension, bool) {
	for _, e := range goExtensions() {
		if e.name == name {
			return e, true
		}
	}
	return goExtension{}, false
}

// removeGoExtension unregisters it, forgets any sign-in, and deletes its
// built binary — also when it is linked, since a link replaces an earlier
// install's entry and would otherwise strand that binary. A linked package
// is the user's source and is never touched, and only a regular file is
// removed: a directory of that name is one of loop's extensions.
func removeGoExtension(ext goExtension) error {
	if _, err := config.RemoveMCPServer(ext.name); err != nil {
		return err
	}
	_ = mcp.ClearAuth(ext.name)
	bin := filepath.Join(extensionsBinDir(), ext.name)
	if st, err := os.Stat(bin); err == nil && st.Mode().IsRegular() {
		return os.Remove(bin)
	}
	return nil
}
