package tui

import (
	"fmt"
	"os"
	"time"
)

// readStdin is a variable so a test can drive the decoder without a tty. The
// decoder's timing behaviour — what happens when a read and an escape timeout
// race — cannot be tested any other way.
var readStdin = func(buf []byte) (int, error) { return os.Stdin.Read(buf) }

func afterMs(ms int) <-chan time.Time {
	return time.After(time.Duration(ms) * time.Millisecond)
}

// TerminalSize is the current grid, for callers outside this package — the
// Windows resize watcher polls it, having no SIGWINCH to wait on.
func TerminalSize() Size { return size(os.Stdout) }

// Size is the terminal grid in cells.
type Size struct {
	Cols int
	Rows int
}

// InlineScreen prepares the NORMAL screen buffer for a live region.
//
// It does not switch to the alternate screen and does not clear: the app
// draws inline, growing downward from wherever the shell left the cursor,
// exactly as loop does. What scrolls off the top becomes the terminal's own
// scrollback and outlives the process.
//
// All it does is hide the cursor, so nothing blinks between here and the
// first paint; the returned func puts it back.
func InlineScreen() func() {
	fmt.Fprint(os.Stdout, "\x1b[?25l")
	return func() {
		fmt.Fprint(os.Stdout, "\x1b[?25h")
	}
}

// EnableBracketedPaste makes the terminal wrap pastes in ESC[200~ … ESC[201~.
func EnableBracketedPaste() func() {
	fmt.Fprint(os.Stdout, "\x1b[?2004h")
	return func() { fmt.Fprint(os.Stdout, "\x1b[?2004l") }
}
