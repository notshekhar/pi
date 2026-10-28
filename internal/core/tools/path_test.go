package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRelative(t *testing.T) {
	got := Resolve("foo/bar", "/tmp/proj")
	if got != filepath.Join("/tmp/proj", "foo/bar") {
		t.Errorf("got %s", got)
	}
}

func TestResolveAbsolute(t *testing.T) {
	got := Resolve("/etc/hosts", "/tmp/proj")
	if got != "/etc/hosts" {
		t.Errorf("got %s", got)
	}
}

func TestResolveHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	got := Resolve("~/x", "/tmp")
	if got != filepath.Join(home, "x") {
		t.Errorf("got %s", got)
	}
}

func TestInside(t *testing.T) {
	if !Inside("/tmp/proj/a.go", "/tmp/proj") {
		t.Error("child should be inside")
	}
	if Inside("/tmp/other", "/tmp/proj") {
		t.Error("sibling should not be inside")
	}
}
