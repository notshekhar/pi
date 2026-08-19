package main

import (
	"time"

	"github.com/notshekhar/pi/internal/modules/tui"
)

// resizePoll is how often the Windows build checks the terminal's size.
//
// Slow enough to be free, fast enough that dragging a window edge settles
// before you have finished reading the result.
const resizePoll = 250 * time.Millisecond

// watchResize calls onResize when the terminal changes size.
//
// Windows has no SIGWINCH, so the size is polled. A poll is the honest
// implementation here rather than a lesser one: the console API's resize
// events arrive on the input handle, and reading them would mean competing
// with the key decoder for the very same handle.
func watchResize(onResize func()) func() {
	stop := make(chan struct{})
	go func() {
		last := tui.TerminalSize()
		ticker := time.NewTicker(resizePoll)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if now := tui.TerminalSize(); now != last {
					last = now
					onResize()
				}
			}
		}
	}()
	return func() { close(stop) }
}
