package tui

import (
	"os"
	"time"
)

// Reading a terminal's answer to a query, without stealing the user's typing.
//
// The obvious implementation — a goroutine blocked on Read, abandoned on
// timeout — is wrong, and wrong in a way that only shows up under a real
// person's hands: a terminal that never answers leaves that goroutine parked
// on stdin, and it consumes the FIRST KEYSTROKES the user types instead. The
// prompt swallowed `he` from `hello` before this was fixed.
//
// A read deadline has no such tail. When it expires the read returns, the
// goroutine is gone, and nothing is left holding the file.

// askTerminal writes a query and reads the reply, or gives up.
//
// Returns ok=false when the terminal does not answer, does not support
// deadlines, or errors — every one of which means "assume nothing".
func askTerminal(query string, timeout time.Duration) (string, bool) {
	if err := os.Stdin.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		// A file that cannot carry a deadline cannot be safely probed: there
		// would be no way to stop reading without abandoning a reader.
		return "", false
	}
	defer os.Stdin.SetReadDeadline(time.Time{})

	if _, err := os.Stdout.WriteString(query); err != nil {
		return "", false
	}

	buf := make([]byte, 64)
	n, err := os.Stdin.Read(buf)
	if err != nil || n == 0 {
		return "", false
	}
	return string(buf[:n]), true
}
