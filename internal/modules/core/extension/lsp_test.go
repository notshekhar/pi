package extension

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/core/extension/lsp"
)

// The model is handed line numbers by `read` and `grep` but never columns, so
// naming the symbol is the intended way in. Getting this wrong lands the
// request on whitespace and answers "No results found", which reads like the
// tool being broken rather than the position being off.
func TestResolveColumn(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	body := "package main\n\nfunc runTurn() {}\n\tfunc run() {}\n// héllo runTurn here\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// Word boundaries: `run` must not match inside `runTurn`.
	col, err := resolveColumn(file, 3, "run")
	if err == nil {
		t.Errorf("`run` matched inside `runTurn` at column %d", col)
	}
	if col, err := resolveColumn(file, 3, "runTurn"); err != nil || col != 6 {
		t.Errorf("runTurn = %d, %v; want column 6", col, err)
	}

	// A tab counts as one column, and a non-ASCII character before the symbol
	// must not push the column past it — the offset is in CHARACTERS.
	if col, err := resolveColumn(file, 5, "runTurn"); err != nil || col != 10 {
		t.Errorf("after a non-ASCII character: %d, %v; want 10", col, err)
	}
}

func TestResolveColumnErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	os.WriteFile(file, []byte("package main\n"), 0o644)

	if _, err := resolveColumn(file, 9, "x"); err == nil ||
		!strings.Contains(err.Error(), "past the end") {
		t.Errorf("past the end: %v", err)
	}
	// The error QUOTES the line, so the model can see why its guess missed.
	_, err := resolveColumn(file, 1, "nope")
	if err == nil || !strings.Contains(err.Error(), "package main") {
		t.Errorf("missing symbol: %v", err)
	}
	if _, err := resolveColumn(filepath.Join(dir, "gone.go"), 1, "x"); err == nil {
		t.Error("a missing file was not reported")
	}
}

// Errors only. Warnings are mostly style, and the agent acts on everything it
// is shown.
func TestReportDiagnosticsFiltersToErrors(t *testing.T) {
	cwd := t.TempDir()
	file := filepath.Join(cwd, "pkg", "a.go")

	at := func(line int) lsp.Range {
		return lsp.Range{Start: lsp.Position{Line: line, Character: 4}}
	}
	block := reportDiagnostics(cwd, file, []lsp.Diagnostic{
		{Range: at(0), Severity: lsp.SeverityWarn, Message: "style"},
		{Range: at(1), Severity: lsp.SeverityError, Message: "undefined:  x"},
		{Range: at(2), Severity: lsp.SeverityHint, Message: "hint"},
	})

	if strings.Contains(block, "style") || strings.Contains(block, "hint") {
		t.Errorf("a non-error was reported:\n%s", block)
	}
	// Path is repo-relative, position is 1-based, whitespace is collapsed.
	for _, want := range []string{`file="pkg/a.go"`, "ERROR [2:5]", "undefined: x"} {
		if !strings.Contains(block, want) {
			t.Errorf("block is missing %q:\n%s", want, block)
		}
	}
}

func TestReportDiagnosticsCleanFileSaysNothing(t *testing.T) {
	cwd := t.TempDir()
	if got := reportDiagnostics(cwd, filepath.Join(cwd, "a.go"), nil); got != "" {
		t.Errorf("a clean file produced %q", got)
	}
	warnings := []lsp.Diagnostic{{Severity: lsp.SeverityWarn, Message: "style"}}
	if got := reportDiagnostics(cwd, filepath.Join(cwd, "a.go"), warnings); got != "" {
		t.Errorf("warnings alone produced %q", got)
	}
}

// One catastrophically broken file must not flood the turn.
func TestReportDiagnosticsIsCapped(t *testing.T) {
	cwd := t.TempDir()
	var many []lsp.Diagnostic
	for i := 0; i < maxPerFile+7; i++ {
		many = append(many, lsp.Diagnostic{Severity: lsp.SeverityError, Message: "boom"})
	}
	block := reportDiagnostics(cwd, filepath.Join(cwd, "a.go"), many)
	if strings.Count(block, "ERROR") != maxPerFile {
		t.Errorf("got %d errors, want the cap of %d", strings.Count(block, "ERROR"), maxPerFile)
	}
	if !strings.Contains(block, "and 7 more") {
		t.Errorf("the remainder was not reported:\n%s", block)
	}
}

// A severity the server left unset means error, which is what LSP says.
func TestUnsetSeverityIsAnError(t *testing.T) {
	cwd := t.TempDir()
	block := reportDiagnostics(cwd, filepath.Join(cwd, "a.go"),
		[]lsp.Diagnostic{{Message: "no severity given"}})
	if !strings.Contains(block, "no severity given") {
		t.Errorf("an unset severity was dropped:\n%s", block)
	}
}
