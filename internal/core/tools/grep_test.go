package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ripgrep's --json spells its keys in lower case ("text"). encoding/json v1
// matched them to an untagged Text field case-insensitively; v2 does not, so
// every match came back with no path and was dropped.
func TestGrepWithRipgrepReadsMatches(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep not installed")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("alpha\nneedle here\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	matches, err := grepWithRipgrep(context.Background(), rg, dir, grepArgs{Pattern: "needle"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("got %d matches, want 1: %+v", len(matches), matches)
	}
	m := matches[0]
	if m.file != file || m.line != 2 || !m.hasText || m.text != "needle here\n" {
		t.Errorf("match = %+v, want %s:2 %q", m, file, "needle here\n")
	}
}
