package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHandles(t *testing.T) {
	ts, ok := FindDef("typescript")
	if !ok {
		t.Fatal("no typescript server")
	}
	for _, path := range []string{"/x/a.ts", "/x/a.TSX", "/x/a.mjs"} {
		if !ts.Handles(path) {
			t.Errorf("typescript should handle %s", path)
		}
	}
	if ts.Handles("/x/a.go") {
		t.Error("typescript claimed a .go file")
	}

	docker, ok := FindDef("dockerfile")
	if !ok {
		t.Fatal("no dockerfile server")
	}
	// Matched by FILENAME, not extension, and case-insensitively.
	if !docker.Handles("/x/dockerfile") {
		t.Error("dockerfile should match by filename")
	}
}

func TestLanguageIDVariesByExtension(t *testing.T) {
	ts, _ := FindDef("typescript")
	cases := map[string]string{
		"/x/a.ts": "typescript", "/x/a.tsx": "typescriptreact",
		"/x/a.js": "javascript", "/x/a.jsx": "javascriptreact",
	}
	for path, want := range cases {
		if got := ts.LanguageIDFor(path); got != want {
			t.Errorf("LanguageIDFor(%s) = %s, want %s", path, got, want)
		}
	}
}

// The nearest ancestor holding a marker wins. A monorepo with three tsconfigs
// gets three servers, each scoped to its own package — which is the whole
// reason the diagnostics are right.
func TestRootIsTheNearestMarker(t *testing.T) {
	workspace := t.TempDir()
	pkg := filepath.Join(workspace, "packages", "web")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(workspace, "tsconfig.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(pkg, "tsconfig.json"), []byte("{}"), 0o644)

	ts, _ := FindDef("typescript")
	file := filepath.Join(pkg, "src", "main.ts")
	if got := ts.Root(file, workspace); got != pkg {
		t.Errorf("Root = %s, want the nearest marker at %s", got, pkg)
	}
}

func TestRootFallsBackToTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	ts, _ := FindDef("typescript")
	file := filepath.Join(workspace, "a.ts")
	if got := ts.Root(file, workspace); got != workspace {
		t.Errorf("Root = %s, want the workspace", got)
	}
}

// A deno.json must stand the TypeScript server down, or every diagnostic is
// reported twice.
func TestDisqualifyMarkerStandsAServerDown(t *testing.T) {
	workspace := t.TempDir()
	file := filepath.Join(workspace, "a.ts")
	os.WriteFile(file, []byte(""), 0o644)

	before := ServersFor(file, workspace)
	os.WriteFile(filepath.Join(workspace, "deno.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(workspace, "tsconfig.json"), []byte("{}"), 0o644)
	after := ServersFor(file, workspace)

	named := func(list []ServerDef, key string) bool {
		for _, d := range list {
			if d.Key == key {
				return true
			}
		}
		return false
	}
	if !named(before, "typescript") {
		t.Fatal("typescript did not handle a .ts file to begin with")
	}
	if named(after, "typescript") {
		t.Error("typescript survived a deno.json")
	}
}

// Every entry has to be launchable in principle: a key, a way to find the
// binary, and a language id.
func TestEveryServerIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range Servers {
		if d.Key == "" {
			t.Fatal("a server has no key")
		}
		if seen[d.Key] {
			t.Errorf("duplicate key %q", d.Key)
		}
		seen[d.Key] = true
		if len(d.BinNames) == 0 {
			t.Errorf("%s has no binary names", d.Key)
		}
		if d.LanguageID == "" && d.LanguageIDFunc == nil {
			t.Errorf("%s has no language id", d.Key)
		}
		if len(d.Extensions) == 0 && len(d.Filenames) == 0 && len(d.RootMarkers) == 0 {
			t.Errorf("%s can never match anything", d.Key)
		}
	}
	if len(Servers) < 30 {
		t.Errorf("only %d servers — the table looks truncated", len(Servers))
	}
}
