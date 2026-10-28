//go:build !windows

package shells

import (
	"os/exec"
	"syscall"
)

func setGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// killTree is loop's killProcessTree: SIGKILL the group, else the pid.
func killTree(pid int) {
	if syscall.Kill(-pid, syscall.SIGKILL) != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func signaled(cmd *exec.Cmd) bool {
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
		return ws.Signaled()
	}
	return false
}
