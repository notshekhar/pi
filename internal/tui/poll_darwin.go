//go:build darwin

package tui

import (
	"syscall"
	"time"
)

// waitReadable blocks until fd has input or d passes (select(2)). A terminal
// stdin cannot carry a Go read deadline, so this is how a probe waits for an
// answer without leaving a reader parked on the user's keyboard.
func waitReadable(fd int, d time.Duration) bool {
	var set syscall.FdSet
	set.Bits[fd/32] |= 1 << (uint(fd) % 32)
	tv := syscall.NsecToTimeval(max(int64(d), 0))
	for {
		err := syscall.Select(fd+1, &set, nil, nil, &tv)
		if err == syscall.EINTR {
			continue
		}
		return err == nil && set.Bits[fd/32]&(1<<(uint(fd)%32)) != 0
	}
}
