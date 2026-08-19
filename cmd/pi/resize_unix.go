//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// watchResize calls onResize when the terminal changes size.
//
// SIGWINCH is how a unix terminal reports it, and it does not exist on
// Windows — which is the whole reason this is a per-platform file rather than
// an inline signal.Notify. The Windows build polls instead; see the sibling.
func watchResize(onResize func()) func() {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			onResize()
		}
	}()
	return func() { signal.Stop(winch) }
}
