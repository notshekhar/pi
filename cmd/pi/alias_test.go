package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/notshekhar/pi/internal/modules/core/config"
)

func withAliases(t *testing.T, aliases map[string]string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if aliases == nil {
		return
	}
	if err := os.MkdirAll(filepath.Join(home, ".pi-agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{Aliases: aliases}); err != nil {
		t.Fatal(err)
	}
}

func TestExpandAlias(t *testing.T) {
	withAliases(t, map[string]string{"t": "/thinking", "p": "/plan on"})

	cases := []struct{ in, want string }{
		// Arguments survive the substitution, which is what makes an alias
		// worth having for anything but the zero-argument commands.
		{"/t high", "/thinking high"},
		{"/t", "/thinking"},
		{"/p", "/plan on"},
		// Unknown names pass through untouched.
		{"/unknown thing", "/unknown thing"},
		{"/model kimi/k3", "/model kimi/k3"},
	}
	for _, c := range cases {
		if got := expandAlias(c.in); got != c.want {
			t.Errorf("expandAlias(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExpandAliasWithNoneConfigured(t *testing.T) {
	withAliases(t, nil)
	if got := expandAlias("/thinking high"); got != "/thinking high" {
		t.Errorf("got %q", got)
	}
}

// An alias chain is a loop waiting to happen, and nobody has needed one.
func TestExpandAliasDoesNotChain(t *testing.T) {
	withAliases(t, map[string]string{"a": "/b", "b": "/thinking"})
	if got := expandAlias("/a"); got != "/b" {
		t.Errorf("expandAlias chained: %q", got)
	}
}
