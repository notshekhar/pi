package tools

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Sandboxing shell commands.
//
// What this is NOT: a security boundary. A determined command escapes any
// wrapper a coding agent can apply from userspace, and pretending otherwise
// would be worse than not having it — someone would rely on it. The
// permissions layer is the thing that decides what may run; this bounds the
// damage of what does.
//
// What it IS: the OS's own confinement, where the OS provides one that costs
// nothing. macOS ships `sandbox-exec`, which can deny writes outside a
// directory. Linux has no equivalent that works without setup, so there the
// answer is honestly "no sandbox" rather than a wrapper that looks like one.

// SandboxMode is how much confinement to apply.
type SandboxMode string

const (
	// SandboxOff runs commands directly.
	SandboxOff SandboxMode = "off"
	// SandboxWorkspace denies writes outside the working directory.
	SandboxWorkspace SandboxMode = "workspace"
)

// SandboxAvailable reports whether the OS can confine a command.
func SandboxAvailable() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, err := exec.LookPath("sandbox-exec")
	return err == nil
}

// sandboxProfile denies writes outside the workspace while leaving reads and
// networking alone.
//
// Reads stay open on purpose: a build reads toolchains, caches and libraries
// from all over the filesystem, and a profile that blocked them would break
// every real command and be switched off within a day.
const sandboxProfile = `(version 1)
(allow default)
(deny file-write*)
(allow file-write*
  (subpath "%s")
  (subpath "/dev")
  (subpath "/private/tmp")
  (subpath "/private/var/tmp")
  (subpath "/tmp"))
`

// WrapSandbox returns the argv to run a command under confinement.
//
// Falls back to running it directly when the mode is off or the OS cannot
// confine it — a command that would not run at all is a worse outcome than
// one that runs unconfined, and the caller is told which happened.
func WrapSandbox(mode SandboxMode, cwd, command string) (argv []string, confined bool) {
	if mode != SandboxWorkspace || !SandboxAvailable() || cwd == "" {
		return []string{"sh", "-c", command}, false
	}
	profile := fmt.Sprintf(sandboxProfile, cwd)
	return []string{"sandbox-exec", "-p", profile, "sh", "-c", command}, true
}

// SandboxNote explains the current state, for /doctor.
func SandboxNote(mode SandboxMode) string {
	switch {
	case mode != SandboxWorkspace:
		return "off — commands run unconfined"
	case SandboxAvailable():
		return "workspace — writes outside the working directory are denied"
	case runtime.GOOS == "darwin":
		return "requested, but sandbox-exec is missing"
	default:
		return "requested, but " + runtime.GOOS + " has no sandbox this can use"
	}
}

// sandboxModeFromString parses a setting.
func sandboxModeFromString(s string) SandboxMode {
	if strings.EqualFold(strings.TrimSpace(s), string(SandboxWorkspace)) {
		return SandboxWorkspace
	}
	return SandboxOff
}

// SandboxFromSettings parses the stored mode.
func SandboxFromSettings(s string) SandboxMode { return sandboxModeFromString(s) }
