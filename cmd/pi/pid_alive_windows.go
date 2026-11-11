package main

import "os"

// pidAlive reports whether a process with this pid exists: on Windows
// FindProcess opens a handle, which fails for a pid nothing holds.
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
