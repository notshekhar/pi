//go:build windows

package shells

import (
	"os/exec"
	"strconv"
)

func setGroup(*exec.Cmd) {}

func killTree(pid int) { _ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Start() }

func signaled(*exec.Cmd) bool { return true }
