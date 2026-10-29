package tui

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// PI_KEY_LOG=<path> records every chunk the terminal sends, raw, with
// whether the kitty protocol was in force — the one question that settles
// "this key does nothing in my terminal": what did the terminal actually
// send?

var (
	keyLogOnce sync.Once
	keyLog     *os.File
)

func logKeyBytes(chunk []byte) {
	keyLogOnce.Do(func() {
		if path := os.Getenv("PI_KEY_LOG"); path != "" {
			keyLog, _ = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		}
	})
	if keyLog == nil {
		return
	}
	fmt.Fprintf(keyLog, "%s kitty=%v %q\n", time.Now().Format("15:04:05.000"), KittyActive(), chunk)
}
