package tools

import (
	"runtime"
	"strings"
	"testing"
)

func TestWrapSandboxOffRunsDirectly(t *testing.T) {
	argv, confined := WrapSandbox(SandboxOff, "/repo", "echo hi")
	if confined {
		t.Error("off should not confine")
	}
	if argv[0] != "sh" || argv[len(argv)-1] != "echo hi" {
		t.Errorf("argv = %v", argv)
	}
}

// A command that will not run at all is worse than one that runs unconfined,
// so an unavailable sandbox falls back rather than failing.
func TestWrapSandboxFallsBackWhenUnavailable(t *testing.T) {
	argv, confined := WrapSandbox(SandboxWorkspace, "/repo", "echo hi")
	if runtime.GOOS == "darwin" && SandboxAvailable() {
		if !confined || argv[0] != "sandbox-exec" {
			t.Errorf("expected confinement on darwin, got %v", argv)
		}
		// The profile must name the workspace, or it confines nothing useful.
		if !strings.Contains(strings.Join(argv, " "), "/repo") {
			t.Error("the profile does not mention the working directory")
		}
		return
	}
	if confined {
		t.Error("claimed confinement without a sandbox")
	}
	if argv[0] != "sh" {
		t.Errorf("argv = %v", argv)
	}
}

// An empty cwd means there is nothing to confine writes to.
func TestWrapSandboxNeedsAWorkspace(t *testing.T) {
	if _, confined := WrapSandbox(SandboxWorkspace, "", "echo hi"); confined {
		t.Error("confined with no working directory")
	}
}

func TestSandboxNoteIsHonest(t *testing.T) {
	if note := SandboxNote(SandboxOff); !strings.Contains(note, "off") {
		t.Errorf("note = %q", note)
	}
	note := SandboxNote(SandboxWorkspace)
	if runtime.GOOS != "darwin" && !strings.Contains(note, runtime.GOOS) {
		t.Errorf("note should say why it cannot confine: %q", note)
	}
}

func TestSandboxFromSettings(t *testing.T) {
	if SandboxFromSettings("workspace") != SandboxWorkspace {
		t.Error("workspace not parsed")
	}
	for _, s := range []string{"", "off", "nonsense"} {
		if SandboxFromSettings(s) != SandboxOff {
			t.Errorf("%q should be off", s)
		}
	}
}
