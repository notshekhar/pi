package tools

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/sandbox"
)

// runBash runs one bash call through the real tool and returns its text.
func runBash(t *testing.T, c *Context, args map[string]any) (string, bool) {
	t.Helper()
	return runTool(t, Bash(c), args)
}

// runTool runs one call through a tool; the bool reports an error result.
func runTool(t *testing.T, tool ai.Tool, args map[string]any) (string, bool) {
	t.Helper()
	input, _ := json.Marshal(args, jsonopt.Write)
	res, err := tool.Execute(context.Background(), input)
	if err != nil {
		return err.Error(), true
	}
	_, failed := res.Output().(provider.ToolOutputErrorText)
	return resultText(res), failed
}

type fixedPlanMode bool

func (m fixedPlanMode) Active() bool                                     { return bool(m) }
func (fixedPlanMode) Set(bool)                                           {}
func (fixedPlanMode) Approve(context.Context, PlanApproval, string) bool { return false }

func requireSandbox(t *testing.T) {
	t.Helper()
	if !sandbox.Supported() || len(sandbox.CheckDependencies()) > 0 {
		t.Skip("no OS sandbox on this machine")
	}
}

// Plan mode and read-only agents get a kernel-enforced read-only bash: it
// reads, and it cannot write — not even inside the working directory.
func TestReadOnlyBashCannotWrite(t *testing.T) {
	requireSandbox(t)
	for name, c := range map[string]*Context{
		"read-only agent": {ReadOnlyBash: true},
		"plan mode":       {PlanMode: fixedPlanMode(true)},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			c.CWD = dir
			if err := os.WriteFile(filepath.Join(dir, "in.txt"), []byte("readable"), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, failed := runBash(t, c, map[string]any{"command": "cat in.txt"}); failed || !strings.Contains(out, "readable") {
				t.Fatalf("read failed: %q", out)
			}
			runBash(t, c, map[string]any{"command": "echo x > out.txt"})
			if _, err := os.Stat(filepath.Join(dir, "out.txt")); err == nil {
				t.Fatal("read-only bash wrote a file")
			}
		})
	}
}

// The same bash with plan mode off writes normally: the sandbox comes from
// the mode, not from the tool.
func TestWritableBashOutsidePlanMode(t *testing.T) {
	dir := t.TempDir()
	c := &Context{CWD: dir, PlanMode: fixedPlanMode(false)}
	if out, failed := runBash(t, c, map[string]any{"command": "echo x > out.txt"}); failed {
		t.Fatalf("bash failed: %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.txt")); err != nil {
		t.Fatal("bash could not write with plan mode off")
	}
}

// A read-only agent must not leave a daemon behind.
func TestReadOnlyBashRefusesTheBackground(t *testing.T) {
	c := &Context{CWD: t.TempDir(), ReadOnlyBash: true, BackgroundShells: true}
	out, failed := runBash(t, c, map[string]any{"command": "sleep 1", "run_in_background": true})
	if !failed || !strings.Contains(out, "read-only") {
		t.Fatalf("got %q (failed=%v)", out, failed)
	}
}

func TestSettingNetwork(t *testing.T) {
	cases := map[string]sandbox.Network{
		``:                           {Mode: "deny"},
		`"allow"`:                    {Mode: "allow"},
		`"deny"`:                     {Mode: "deny"},
		`{"allow":["*.github.com"]}`: {Mode: "list", Allow: []string{"*.github.com"}},
	}
	for raw, want := range cases {
		got := settingNetwork(jsontext.Value(raw))
		if got.Mode != want.Mode || strings.Join(got.Allow, ",") != strings.Join(want.Allow, ",") {
			t.Errorf("settingNetwork(%s) = %+v, want %+v", raw, got, want)
		}
	}
}

// The user's sandbox setting confines writes to the working directory.
func TestSandboxSettingConfinesWrites(t *testing.T) {
	requireSandbox(t)
	dir, outside := t.TempDir(), t.TempDir()
	on := true
	c := &Context{CWD: dir, Sandbox: &config.SandboxSetting{Enabled: &on}}
	runBash(t, c, map[string]any{"command": "echo x > " + filepath.Join(outside, "escaped.txt") + "; echo y > inside.txt"})
	if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); err == nil {
		t.Error("the sandbox let a write out of the working directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "inside.txt")); err != nil {
		t.Error("the sandbox blocked a write inside the working directory")
	}
}
