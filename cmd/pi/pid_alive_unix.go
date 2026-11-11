//go:build !windows

package main

import "syscall"

// pidAlive reports whether a process with this pid exists: signal 0 checks
// without delivering anything. EPERM means it exists but is someone else's.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
