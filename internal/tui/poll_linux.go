//go:build linux

package tui

import (
	"syscall"
	"time"
)

// waitReadable blocks until fd has input or d passes (select(2)); see
// poll_darwin.go.
func waitReadable(fd int, d time.Duration) bool {
	var set syscall.FdSet
	set.Bits[fd/64] |= 1 << (uint(fd) % 64)
	tv := syscall.NsecToTimeval(max(int64(d), 0))
	for {
		n, err := syscall.Select(fd+1, &set, nil, nil, &tv)
		if err == syscall.EINTR {
			continue
		}
		return err == nil && n > 0
	}
}
