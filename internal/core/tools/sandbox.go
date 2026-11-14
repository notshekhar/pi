package tools

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/sandbox"
	"github.com/notshekhar/pi/internal/core/shells"
)

// How a bash command meets the OS sandbox — loop's resolveSandbox
// (tools/bash.ts). Two postures:
//
//   - read-only agents and plan mode get a kernel-enforced read-only sandbox
//     that fails CLOSED: when it cannot hold, bash is refused, never run
//     unconfined;
//   - everyone else gets the user's `sandbox` setting, which fails OPEN with a
//     warning: a command the user asked for still runs, and they are told it
//     ran without isolation.

// readOnlyConfig is loop's READ_ONLY: nothing writable, no network.
var readOnlyConfig = sandbox.Config{ReadOnly: true, Network: sandbox.Network{Mode: "deny"}}

// sandboxRun is how one command will be spawned: an argv (nil means the plain
// shell) and a warning to put in front of its output.
type sandboxRun struct {
	argv    []string
	warning string
}

// enforceSandbox wraps a command in a sandbox that MUST hold. what completes
// "<what>, which isn't available…", so the model learns why bash is gone.
func enforceSandbox(command, cwd, what string, cfg sandbox.Config) ([]string, error) {
	if !sandbox.Supported() {
		return nil, fmt.Errorf("%s, which isn't available on this platform — bash is disabled here. Investigate with read/ls/grep/find instead.", what)
	}
	if missing := sandbox.CheckDependencies(); len(missing) > 0 {
		return nil, fmt.Errorf("%s, but it's unavailable (%s). Refusing to run bash unsandboxed. Use read/ls/grep/find, or install the missing dependency.", what, strings.Join(missing, ", "))
	}
	shell, _ := shells.ShellConfig()
	wrapped, err := sandbox.Wrap(command, shell, cwd, cfg)
	if err != nil {
		return nil, fmt.Errorf("The sandbox failed to start (%s). Refusing to run bash unsandboxed.", err)
	}
	return wrapped.Argv, nil
}

// resolveSandbox decides how a command runs under the sandbox.
func resolveSandbox(command string, t *Context) (sandboxRun, error) {
	if t.ReadOnlyBash || t.planMode() {
		who := "Plan mode runs"
		if t.ReadOnlyBash {
			who = "This agent runs"
		}
		argv, err := enforceSandbox(command, t.CWD, who+" bash in a read-only OS sandbox", readOnlyConfig)
		return sandboxRun{argv: argv}, err
	}
	if !t.Sandbox.On() {
		return sandboxRun{}, nil
	}
	if !sandbox.Supported() {
		return sandboxRun{warning: "sandbox is enabled but not supported on this platform — ran WITHOUT isolation."}, nil
	}
	if missing := sandbox.CheckDependencies(); len(missing) > 0 {
		return sandboxRun{warning: fmt.Sprintf("sandbox is enabled but unavailable (%s) — ran WITHOUT isolation.", strings.Join(missing, ", "))}, nil
	}
	shell, _ := shells.ShellConfig()
	wrapped, err := sandbox.Wrap(command, shell, t.CWD, settingConfig(t.Sandbox))
	if err != nil {
		return sandboxRun{warning: fmt.Sprintf("sandbox failed to start (%s) — ran WITHOUT isolation.", err)}, nil
	}
	return sandboxRun{argv: wrapped.Argv, warning: wrapped.Warning}, nil
}

// settingConfig is the `sandbox` setting as a sandbox config.
func settingConfig(s *config.SandboxSetting) sandbox.Config {
	return sandbox.Config{
		AllowWrite:     s.AllowWrite,
		DenyWrite:      s.DenyWrite,
		DenyRead:       s.DenyRead,
		AllowRead:      s.AllowRead,
		AllowGitConfig: s.AllowGitConfig != nil && *s.AllowGitConfig,
		Network:        settingNetwork(s.Network),
	}
}

// settingNetwork reads loop's SandboxNetwork: "allow", "deny", or
// { allow, deny } — absent means "deny".
func settingNetwork(raw jsontext.Value) sandbox.Network {
	var mode string
	if json.Unmarshal(raw, &mode, jsonopt.Read) == nil && (mode == "allow" || mode == "deny") {
		return sandbox.Network{Mode: mode}
	}
	var list struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	}
	if json.Unmarshal(raw, &list, jsonopt.Read) == nil && list.Allow != nil {
		return sandbox.Network{Mode: "list", Allow: list.Allow, Deny: list.Deny}
	}
	return sandbox.Network{Mode: "deny"}
}
