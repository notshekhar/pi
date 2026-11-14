// Package shells is loop's background-shell registry
// (tools/utils/shell-registry.ts): commands that outlive the tool call that
// started them. Shells belong to a session, live only in this process, log to
// ~/.loop/shells/<session>/<id>.log from byte zero, and are killed on exit.
package shells

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/notshekhar/pi/internal/core/home"
)

// MaxBackground is loop's MAX_BACKGROUND_SHELLS.
const MaxBackground = 8

// MaxReadBytes caps one read; older unread output is dropped.
const MaxReadBytes = 50_000

const previewLines = 3

// Info is loop's ShellInfo.
type Info struct {
	ID        string   `json:"id"`
	Command   string   `json:"command"`
	CWD       string   `json:"cwd"`
	PID       int      `json:"pid,omitzero"`
	Status    string   `json:"status"` // running | exited | killed | failed
	ExitCode  *int     `json:"exitCode"`
	StartedAt int64    `json:"startedAt"`
	EndedAt   *int64   `json:"endedAt"`
	LogPath   string   `json:"logPath"`
	Bytes     int64    `json:"bytes"`
	Cursor    int64    `json:"cursor"`
	Preview   []string `json:"preview"`
	Promoted  bool     `json:"promoted"`
}

type record struct {
	Info
	child *Child
	log   *os.File
	// logStart is the offset of the log file's first byte in the shell's
	// whole output: offsets count every byte the shell ever printed, while
	// the file keeps only its recent part (logs.go).
	logStart int64
	// outputOpen: the shell's pipe is still held. It outlives the shell when
	// it leaves a process behind, and only until it closes can the process
	// group be trusted to exist and be killed.
	outputOpen    bool
	noticePending bool
	killRequested bool
	lastEmit      time.Time
}

type bucket struct {
	counter int
	byID    map[string]*record
	order   []string
}

const shared = "__shared__"

var (
	mu        sync.Mutex
	sessions  = map[string]*bucket{}
	listeners = map[int]func(Info){}
	nextL     int
)

func key(sessionID string) string {
	if sessionID == "" {
		return shared
	}
	return sessionID
}

func bucketOf(sessionID string) *bucket {
	k := key(sessionID)
	b := sessions[k]
	if b == nil {
		b = &bucket{byID: map[string]*record{}}
		sessions[k] = b
	}
	return b
}

var unsafeSlug = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func logDir(sessionID string) string {
	s := unsafeSlug.ReplaceAllString(key(sessionID), "_")
	if len(s) > 64 {
		s = s[:64]
	}
	return home.Path("shells", s)
}

// OnChange subscribes to start/exit/kill (and throttled output) changes.
func OnChange(fn func(Info)) func() {
	mu.Lock()
	defer mu.Unlock()
	id := nextL
	nextL++
	listeners[id] = fn
	return func() {
		mu.Lock()
		delete(listeners, id)
		mu.Unlock()
	}
}

// emit runs listeners outside the lock.
func emitLocked(r *record) func() {
	info := r.snapshot()
	fns := make([]func(Info), 0, len(listeners))
	for _, fn := range listeners {
		fns = append(fns, fn)
	}
	return func() {
		for _, fn := range fns {
			func() {
				defer func() { _ = recover() }()
				fn(info)
			}()
		}
	}
}

func (r *record) snapshot() Info {
	i := r.Info
	i.Preview = append([]string{}, r.Preview...)
	return i
}

// Sanitize is loop's sanitizeBinaryOutput: drop control characters except
// tab/newline/CR, and U+FFF9–FFFB.
func Sanitize(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c == '\t' || c == '\n' || c == '\r' || (c > 0x1f && (c < 0xfff9 || c > 0xfffb)) {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func (r *record) pushPreview(chunk string) {
	text := Sanitize(chunk)
	if text == "" {
		return
	}
	lines := strings.Split(text, "\n")
	last := ""
	if n := len(r.Preview); n > 0 {
		last = r.Preview[n-1]
		r.Preview = r.Preview[:n-1]
	}
	r.Preview = append(r.Preview, last+lines[0])
	r.Preview = append(r.Preview, lines[1:]...)
	for len(r.Preview) > previewLines {
		r.Preview = r.Preview[1:]
	}
}

// emitEvery throttles output-driven repaints: a dev server can print
// thousands of lines a second. The counters and the log are always exact;
// only the panel's view of them lags, by at most this long.
const emitEvery = 400 * time.Millisecond

type sink struct{ r *record }

func (s sink) Write(p []byte) (int, error) {
	mu.Lock()
	r := s.r
	r.pushPreview(string(p))
	r.appendToLog(p)
	var fire func()
	if time.Since(r.lastEmit) >= emitEvery {
		r.lastEmit = time.Now()
		fire = emitLocked(r)
	}
	mu.Unlock()
	if fire != nil {
		fire()
	}
	return len(p), nil
}

// appendToLog counts bytes only once they are on disk, so a read is never
// pointed at output that is not there.
func (r *record) appendToLog(p []byte) {
	if r.log == nil {
		return
	}
	n, err := r.log.Write(p)
	r.Bytes += int64(n)
	if err == nil && r.Bytes-r.logStart > MaxLogBytes {
		var kept int64
		r.log, kept, err = compactLog(r.LogPath, r.log, r.Bytes-r.logStart)
		r.logStart = r.Bytes - kept
	}
	if err != nil {
		r.closeLog()
	}
}

func (r *record) closeLog() {
	if r.log != nil {
		_ = r.log.Close()
		r.log = nil
	}
}

// releaseOutput: nothing more can arrive. The log closes, and the process
// group stops being ours.
func (r *record) releaseOutput() {
	r.outputOpen = false
	r.closeLog()
	r.child.Release()
}

// ShellConfig is loop's getShellConfig: /bin/bash, else bash on PATH, else sh.
func ShellConfig() (string, []string) {
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("bash"); err == nil {
			return p, []string{"-c"}
		}
	}
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash", []string{"-c"}
	}
	if p, err := exec.LookPath("bash"); err == nil {
		return p, []string{"-c"}
	}
	return "sh", []string{"-c"}
}

// ShellEnv is loop's getShellEnv: ~/.loop/bin first on PATH.
func ShellEnv() []string {
	bin := home.Path("bin")
	env := os.Environ()
	for i, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, "path") {
			for _, e := range filepath.SplitList(v) {
				if e == bin {
					return env
				}
			}
			if v == "" {
				env[i] = k + "=" + bin
			} else {
				env[i] = k + "=" + bin + string(os.PathListSeparator) + v
			}
			return env
		}
	}
	return append(env, "PATH="+bin)
}

// RegisterOptions is loop's StartShellOptions: a spawned child to keep.
type RegisterOptions struct {
	SessionID string
	Command   string
	CWD       string
	Child     *Child
	// Seed is output already collected in the foreground, replayed into the
	// log so a promoted run keeps the lines it printed before promotion.
	Seed     string
	Promoted bool
}

// Register keeps a spawned child as a background shell and starts logging it.
func Register(o RegisterOptions) Info {
	pruneLogs()
	mu.Lock()
	b := bucketOf(o.SessionID)
	b.counter++
	id := fmt.Sprintf("bash_%d", b.counter)
	r := &record{
		Info: Info{ID: id, Command: o.Command, CWD: o.CWD, PID: o.Child.PID(), Status: "running",
			StartedAt: time.Now().UnixMilli(), Preview: []string{}, Promoted: o.Promoted},
		child:      o.Child,
		outputOpen: true,
	}
	r.openLog(logDir(o.SessionID))
	b.byID[id] = r
	b.order = append(b.order, id)
	mu.Unlock()

	out := sink{r}
	if o.Seed != "" {
		_, _ = out.Write([]byte(o.Seed))
	}
	o.Child.SetSink(out)
	go r.settleOnExit()
	go r.releaseOnClose()

	mu.Lock()
	fire := emitLocked(r)
	info := r.snapshot()
	mu.Unlock()
	fire()
	return info
}

// openLog truncates: byte offsets are the whole read model, and a log left
// by a previous process (a resumed session reusing its id) would put every
// offset in this run past somebody else's output. A log that cannot be
// opened is a degradation, not a failure — the shell still runs and reads
// come back empty.
func (r *record) openLog(dir string) {
	path := filepath.Join(dir, r.ID+".log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	if f, err := os.Create(path); err == nil {
		r.log, r.LogPath = f, path
	}
}

func (r *record) settleOnExit() {
	<-r.child.Exited()
	mu.Lock()
	// A killed shell reports as killed even though the OS calls it a signal
	// exit: the distinction is what the user did, not how it ended.
	status := "exited"
	if r.killRequested && r.child.Signaled() {
		status = "killed"
	}
	var code *int
	if c := r.child.ExitCode(); c >= 0 {
		code = &c
	}
	r.settle(status, code)
	fire := emitLocked(r)
	mu.Unlock()
	fire()
}

// releaseOnClose waits for the pipe, not the exit: whatever the shell left
// behind is still writing, and still ours to kill.
func (r *record) releaseOnClose() {
	<-r.child.Closed()
	mu.Lock()
	r.releaseOutput()
	mu.Unlock()
}

func (r *record) settle(status string, code *int) {
	if r.Status != "running" {
		return
	}
	r.Status, r.ExitCode = status, code
	now := time.Now().UnixMilli()
	r.EndedAt = &now
	r.noticePending = true
}

// StartOptions is a command for Start to spawn and register.
type StartOptions struct {
	SessionID string
	Command   string
	CWD       string
}

// Start spawns command in its own process group and registers it.
func Start(o StartOptions) (Info, error) {
	child, err := Spawn(SpawnOptions{Command: o.Command, CWD: o.CWD})
	if err != nil {
		return Info{}, err
	}
	return Register(RegisterOptions{SessionID: o.SessionID, Command: o.Command, CWD: o.CWD, Child: child}), nil
}

// List is the session's shells in start order.
func List(sessionID string) []Info {
	mu.Lock()
	defer mu.Unlock()
	b := bucketOf(sessionID)
	out := make([]Info, 0, len(b.order))
	for _, id := range b.order {
		out = append(out, b.byID[id].snapshot())
	}
	return out
}

// RunningCount is loop's runningShellCount.
func RunningCount(sessionID string) int {
	n := 0
	for _, s := range List(sessionID) {
		if s.Status == "running" {
			n++
		}
	}
	return n
}

// ReadResult is loop's ShellReadResult.
type ReadResult struct {
	Info Info
	// Text is the output since the last read, newest kept when it is too long.
	Text string
	// SkippedBytes were dropped because the unread span was over MaxReadBytes.
	SkippedBytes int64
	// FilteredOut is the lines the filter hid, so an empty read is explainable.
	FilteredOut int
}

// Read returns output since the cursor and, unless noAdvance, moves the
// cursor past it — so polling a dev server does not re-read its startup
// banner every time.
func Read(sessionID, id, filter string, noAdvance bool) (ReadResult, bool) {
	mu.Lock()
	r := bucketOf(sessionID).byID[id]
	if r == nil {
		mu.Unlock()
		return ReadResult{}, false
	}
	end, start, logStart := r.Bytes, r.Cursor, r.logStart
	path := r.LogPath
	mu.Unlock()

	var res ReadResult
	if end-start > MaxReadBytes {
		res.SkippedBytes = end - start - MaxReadBytes
		start = end - MaxReadBytes
	}
	res.Text = readLogSpan(path, start-logStart, end-start)
	res.Text, res.FilteredOut = keepMatching(res.Text, filter)
	mu.Lock()
	if !noAdvance {
		r.Cursor = end
	}
	res.Info = r.snapshot()
	mu.Unlock()
	return res, true
}

func readLogSpan(path string, offset, length int64) string {
	if path == "" || length <= 0 {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, length)
	n, _ := f.ReadAt(buf, offset)
	return Sanitize(string(buf[:n]))
}

// keepMatching keeps the lines a regex matches. A filter that does not parse
// is ignored, not fatal.
func keepMatching(text, filter string) (kept string, filteredOut int) {
	if filter == "" || text == "" {
		return text, 0
	}
	re, err := regexp.Compile(filter)
	if err != nil {
		return text, 0
	}
	lines := strings.Split(text, "\n")
	var matching []string
	for _, line := range lines {
		if re.MatchString(line) {
			matching = append(matching, line)
		}
	}
	return strings.Join(matching, "\n"), len(lines) - len(matching)
}

// Kill stops one shell's whole process tree. An exited shell can still have
// left processes behind, so the tree goes either way while its pipe is open.
func Kill(sessionID, id string) (Info, bool) {
	mu.Lock()
	defer mu.Unlock()
	r := bucketOf(sessionID).byID[id]
	if r == nil {
		return Info{}, false
	}
	r.kill()
	return r.snapshot(), true
}

func (r *record) kill() {
	if r.outputOpen {
		r.child.KillTree()
	}
	if r.Status == "running" {
		r.killRequested = true
	}
}

// TakeNotices is the exits the model has not been told about, cleared as
// they are taken, so a finished shell announces itself instead of waiting to
// be polled.
func TakeNotices(sessionID string) []Info {
	mu.Lock()
	defer mu.Unlock()
	b := bucketOf(sessionID)
	var out []Info
	for _, id := range b.order {
		if r := b.byID[id]; r.noticePending {
			r.noticePending = false
			out = append(out, r.snapshot())
		}
	}
	return out
}

// KillSession kills every shell in one session, and what any of them left
// behind. It returns how many were running.
func KillSession(sessionID string) int {
	mu.Lock()
	defer mu.Unlock()
	b := bucketOf(sessionID)
	running := 0
	for _, id := range b.order {
		r := b.byID[id]
		if r.Status == "running" {
			running++
		}
		r.kill()
	}
	return running
}

// KillAll kills every shell in every session — the process is going away.
func KillAll() int {
	mu.Lock()
	keys := make([]string, 0, len(sessions))
	for k := range sessions {
		keys = append(keys, k)
	}
	mu.Unlock()
	n := 0
	for _, k := range keys {
		if k == shared {
			k = ""
		}
		n += KillSession(k)
	}
	return n
}

// KillAllBashChildren is everything bash started, on the way out: the
// background shells plus any foreground command still running. It returns
// the number of shells killed, which is what a surface prints.
//
// Shells live only as long as the process by design, so a surface that exits
// without calling this strands processes nobody can see, read or kill. Every
// surface that can run a turn owes this one call on its exit path.
func KillAllBashChildren() int {
	killed := KillAll()
	killTracked()
	return killed
}

// FormatDuration is loop's formatDuration: 8s, 2m14s, 1h03m.
func FormatDuration(d time.Duration) string {
	total := int((d + 500*time.Millisecond) / time.Second)
	if total < 0 {
		total = 0
	}
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	m, s := total/60, total%60
	if m < 60 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%dh%02dm", m/60, m%60)
}

// StatusText is the /shells line status.
func StatusText(s Info) string {
	switch s.Status {
	case "running":
		return "running " + FormatDuration(time.Since(time.UnixMilli(s.StartedAt)))
	case "killed":
		return "killed"
	case "failed":
		return "failed to start"
	}
	if s.ExitCode != nil && *s.ExitCode == 0 {
		return "exited 0"
	}
	if s.ExitCode == nil {
		return "exited ?"
	}
	return fmt.Sprintf("exited %d", *s.ExitCode)
}

// Whether a surface is showing running shells. Promotion on timeout is gated
// on it: the timeout exists to stop an invisible long run, so it may only be
// relaxed where a shells panel makes the run visible.
var panelPresent atomic.Bool

// SetPanelPresent records that a surface lists running shells.
func SetPanelPresent(present bool) { panelPresent.Store(present) }

// PanelPresent reports whether a surface lists running shells.
func PanelPresent() bool { return panelPresent.Load() }

// ResetForTests drops all state without killing.
func ResetForTests() {
	mu.Lock()
	sessions = map[string]*bucket{}
	listeners = map[int]func(Info){}
	pruneOnce = sync.Once{}
	mu.Unlock()
}

// FormatNotices is the reminder that tells the model which shells exited
// since its last step (loop's formatShellNotices). It is what makes "do not
// poll" honest: a model that is not told has no choice but to sleep and check.
func FormatNotices(exited []Info) string {
	if len(exited) == 0 {
		return ""
	}
	rows := make([]string, len(exited))
	for i, s := range exited {
		how := "exited with code ?"
		switch {
		case s.Status == "killed":
			how = "was killed"
		case s.Status == "failed":
			how = "failed to start"
		case s.ExitCode != nil:
			how = fmt.Sprintf("exited with code %d", *s.ExitCode)
		}
		tail := ""
		if unread := s.Bytes - s.Cursor; unread > 0 {
			tail = fmt.Sprintf("; %d bytes unread", unread)
		}
		rows[i] = fmt.Sprintf("%s (%s) %s%s.", s.ID, FirstLine(s.Command), how, tail)
	}
	return "<system-reminder>Background shell update: " + strings.Join(rows, " ") +
		" Read anything you still need with the shells tool. Ignore this if it no longer matters.</system-reminder>"
}

// commandPreviewChars is how much of a command a one-line listing shows.
const commandPreviewChars = 70

// FirstLine is a command's first line, cut short for a one-line listing.
func FirstLine(command string) string {
	line, _, _ := strings.Cut(command, "\n")
	if runes := []rune(line); len(runes) > commandPreviewChars {
		return string(runes[:commandPreviewChars])
	}
	return line
}
