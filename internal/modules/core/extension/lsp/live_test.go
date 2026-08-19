package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Against a REAL language server when one is installed.
//
// The fixture tests prove the framing; this proves the whole thing — that the
// handshake a real server expects is the one being sent, that the capability
// names match, and that a genuine compiler's diagnostics come back. Skipped
// where the server is absent, so the suite stays portable.

func skipWithout(t *testing.T, bin string) {
	t.Helper()
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("%s not installed", bin)
	}
}

// clangd is a pull-model server, so this also exercises the branch that asks
// rather than waits.
func TestLiveClangdDiagnostics(t *testing.T) {
	skipWithout(t, "clangd")

	dir := t.TempDir()
	file := filepath.Join(dir, "main.c")
	// A genuine error: `undeclared` is not defined anywhere.
	body := "int main(void) {\n    return undeclared;\n}\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(dir)
	defer manager.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	clients := manager.ClientsFor(ctx, file)
	if len(clients) == 0 {
		t.Skip("clangd is on PATH but did not start")
	}

	diagnostics := manager.Diagnose(ctx, file)
	if len(diagnostics) == 0 {
		t.Fatal("clangd reported no problems for a file that does not compile")
	}
	var found bool
	for _, d := range diagnostics {
		if strings.Contains(strings.ToLower(d.Message), "undeclared") {
			found = true
		}
	}
	if !found {
		t.Errorf("the real error was not among %d diagnostics: %+v", len(diagnostics), diagnostics)
	}
}

// And a real navigation operation, which is the other half of the extension.
func TestLiveClangdNavigation(t *testing.T) {
	skipWithout(t, "clangd")

	dir := t.TempDir()
	file := filepath.Join(dir, "main.c")
	body := "int answer(void) { return 42; }\n\nint main(void) { return answer(); }\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(dir)
	defer manager.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	clients := manager.ClientsFor(ctx, file)
	if len(clients) == 0 {
		t.Skip("clangd is on PATH but did not start")
	}
	for _, c := range clients {
		c.OpenDocument(file, body)
	}

	// The call on line 3 should resolve to the definition on line 1. The
	// column is 1-based, as the tool supplies it.
	in := Input{Operation: "goToDefinition", File: file, Line: 3, Character: 25}
	var lines []string
	for _, c := range clients {
		lines = append(lines, Run(ctx, c, in, dir).Lines...)
	}
	if len(lines) == 0 {
		t.Fatal("goToDefinition found nothing for a call with a definition in the same file")
	}
	// "main.c:1:5" — the definition, one-based, repo-relative.
	if !strings.HasPrefix(lines[0], "main.c:1:") {
		t.Errorf("resolved to %q, want the definition on line 1", lines[0])
	}

	outline := Run(ctx, clients[0], Input{Operation: "documentSymbol", File: file}, dir)
	if len(outline.Lines) < 2 {
		t.Errorf("documentSymbol returned %v, want both functions", outline.Lines)
	}
}
