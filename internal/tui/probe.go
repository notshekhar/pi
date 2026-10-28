package tui

import (
	"os"
	"time"
)

// Reading a terminal's answer to a query, without stealing the user's typing.
//
// A goroutine blocked on Read and abandoned on timeout is wrong: a terminal
// that never answers leaves it parked on stdin, consuming the first keystrokes
// the user types. And a Go read deadline is not available — a terminal stdin
// "does not support deadline", which silently failed every probe. So the wait
// is select(2) on the descriptor (waitReadable), and a read only happens when
// input is already there.

// askTerminal writes a query and reads the reply, or gives up.
func askTerminal(query string, timeout time.Duration) (string, bool) {
	if _, err := os.Stdout.WriteString(query); err != nil {
		return "", false
	}
	if !waitReadable(int(os.Stdin.Fd()), timeout) {
		return "", false
	}
	buf := make([]byte, 256)
	n, err := os.Stdin.Read(buf)
	if err != nil || n == 0 {
		return "", false
	}
	return string(buf[:n]), true
}
