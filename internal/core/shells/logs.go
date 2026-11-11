package shells

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/home"
)

// Background shell logs on disk (loop's tools/utils/shell-logs.ts): where
// they live, and what keeps them from growing for good. A dev server left
// running can print for days, and nothing reads a log whose process has gone.

const (
	// MaxLogBytes is the most one log may hold on disk.
	MaxLogBytes = 32 * 1024 * 1024
	// KeptLogBytes is what a log keeps when it reaches the cap. Far more
	// than one read (MaxReadBytes), so a read never reaches past it; a
	// quarter of the cap, so the copy happens once per 24 MB of output.
	KeptLogBytes = 8 * 1024 * 1024
	// LogRetention is how long a session's logs stay after their last write.
	LogRetention = 7 * 24 * time.Hour
)

func logRoot() string { return home.Path("shells") }

// compactLog cuts a log down to its newest bytes. It closes log and returns
// the reopened file and how many bytes stayed.
func compactLog(path string, log *os.File, size int64) (*os.File, int64, error) {
	kept := min(int64(KeptLogBytes), size)
	tail := make([]byte, kept)
	reader, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	_, err = reader.ReadAt(tail, size-kept)
	_ = reader.Close()
	if err != nil && err != io.EOF {
		return nil, 0, err
	}
	_ = log.Close()
	if err := os.WriteFile(path, tail, 0o644); err != nil {
		return nil, 0, err
	}
	reopened, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	return reopened, kept, err
}

// lastWrite is the newest modification under a session's log directory.
func lastWrite(dir string) time.Time {
	newest := time.Time{}
	if info, err := os.Stat(dir); err == nil {
		newest = info.ModTime()
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

var pruneOnce sync.Once

// pruneLogs deletes the logs of sessions nothing has written to for
// LogRetention. Once per process, on its first shell: before that this
// process has no logs of its own to lose, and a shell another process still
// runs quietly only loses its log file.
func pruneLogs() {
	pruneOnce.Do(func() { pruneLogsBefore(time.Now().Add(-LogRetention)) })
}

func pruneLogsBefore(cutoff time.Time) {
	root := logRoot()
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if dir := filepath.Join(root, entry.Name()); lastWrite(dir).Before(cutoff) {
			_ = os.RemoveAll(dir)
		}
	}
}
