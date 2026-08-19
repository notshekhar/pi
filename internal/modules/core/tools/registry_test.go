package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadBeforeEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	if msg := r.CheckEdit(path, "a.go"); msg == "" {
		t.Fatal("unread file must be rejected")
	}

	info, _ := os.Stat(path)
	r.RecordRead(path, info.ModTime().UnixNano(), 1, 1, true)
	if msg := r.CheckEdit(path, "a.go"); msg != "" {
		t.Fatalf("after read: %s", msg)
	}

	// External change invalidates the read.
	if err := os.WriteFile(path, []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := r.CheckEdit(path, "a.go"); msg == "" || !contains(msg, "changed on disk") {
		t.Fatalf("stale read: %s", msg)
	}
}

func TestWriteRequiresCompleteRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	info, _ := os.Stat(path)
	r.RecordRead(path, info.ModTime().UnixNano(), 1, 1, false)
	if msg := r.CheckWrite(path, "a.go"); msg == "" || !contains(msg, "Only part") {
		t.Fatalf("partial read: %s", msg)
	}

	r.RecordRead(path, info.ModTime().UnixNano(), 2, 3, true)
	if msg := r.CheckWrite(path, "a.go"); msg != "" {
		t.Fatalf("full coverage: %s", msg)
	}
}

func TestNewFileWriteAllowed(t *testing.T) {
	r := NewRegistry()
	if msg := r.CheckWrite(filepath.Join(t.TempDir(), "new.go"), "new.go"); msg != "" {
		t.Fatalf("new file: %s", msg)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		stringIndex(s, sub) >= 0)
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
