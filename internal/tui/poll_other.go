//go:build !darwin && !linux

package tui

import "time"

// waitReadable has no portable select here: probes report nothing, and the
// app runs on its defaults.
func waitReadable(int, time.Duration) bool { return false }
