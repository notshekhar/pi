// Package shells is loop's background-shell registry
// (tools/utils/shell-registry.ts): commands that outlive the tool call that
// started them. Shells belong to a session, live only in this process, log to
// ~/.loop/shells/<session>/<id>.log from byte zero, and are killed on exit.
package shells

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
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
	PID       int      `json:"pid,omitempty"`
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
	cmd           *exec.Cmd
	log           *os.File
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

const emitEvery = 400 * time.Millisecond

type sink struct{ r *record }

func (s sink) Write(p []byte) (int, error) {
	mu.Lock()
	r := s.r
	r.pushPreview(string(p))
	if r.log != nil {
		if _, err := r.log.Write(p); err != nil {
			_ = r.log.Close()
			r.log = nil
		}
	}
	r.Bytes += int64(len(p))
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

// StartOptions is loop's StartShellOptions, plus the command line to spawn.
type StartOptions struct {
	SessionID string
	Command   string
	CWD       string
	Seed      string
	Promoted  bool
}

// Start spawns command in its own process group and registers it.
func Start(o StartOptions) (Info, error) {
	shell, args := ShellConfig()
	cmd := exec.Command(shell, append(args, o.Command)...)
	cmd.Dir = o.CWD
	cmd.Env = ShellEnv()
	cmd.Stdin = nil
	setGroup(cmd)

	mu.Lock()
	b := bucketOf(o.SessionID)
	b.counter++
	id := fmt.Sprintf("bash_%d", b.counter)
	r := &record{Info: Info{ID: id, Command: o.Command, CWD: o.CWD, Status: "running",
		StartedAt: time.Now().UnixMilli(), Preview: []string{}, Promoted: o.Promoted}}
	dir := logDir(o.SessionID)
	r.LogPath = filepath.Join(dir, id+".log")
	if err := os.MkdirAll(dir, 0o755); err == nil {
		if f, err := os.Create(r.LogPath); err == nil {
			r.log = f
		} else {
			r.LogPath = ""
		}
	} else {
		r.LogPath = ""
	}
	b.byID[id] = r
	b.order = append(b.order, id)
	r.cmd = cmd
	mu.Unlock()

	out := sink{r}
	if o.Seed != "" {
		_, _ = out.Write([]byte(o.Seed))
	}
	cmd.Stdout = io.Writer(out)
	cmd.Stderr = io.Writer(out)
	if err := cmd.Start(); err != nil {
		mu.Lock()
		r.settle("failed", nil)
		fire := emitLocked(r)
		info := r.snapshot()
		mu.Unlock()
		fire()
		return info, err
	}
	mu.Lock()
	r.PID = cmd.Process.Pid
	fire := emitLocked(r)
	info := r.snapshot()
	mu.Unlock()
	fire()

	go func() {
		err := cmd.Wait()
		mu.Lock()
		code := cmd.ProcessState.ExitCode()
		var cp *int
		if code >= 0 {
			cp = &code
		}
		status := "exited"
		if r.killRequested && signaled(cmd) {
			status = "killed"
		} else if err != nil && cmd.ProcessState == nil {
			status = "failed"
		}
		r.settle(status, cp)
		fire := emitLocked(r)
		mu.Unlock()
		fire()
	}()
	return info, nil
}

func (r *record) settle(status string, code *int) {
	if r.Status != "running" {
		return
	}
	r.Status, r.ExitCode = status, code
	now := time.Now().UnixMilli()
	r.EndedAt = &now
	r.noticePending = true
	if r.log != nil {
		_ = r.log.Close()
		r.log = nil
	}
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
	Info         Info
	Text         string
	SkippedBytes int64
	FilteredOut  int
}

// Read returns output since the cursor (advancing it unless noAdvance).
func Read(sessionID, id, filter string, noAdvance bool) (ReadResult, bool) {
	mu.Lock()
	r := bucketOf(sessionID).byID[id]
	if r == nil {
		mu.Unlock()
		return ReadResult{}, false
	}
	end, start := r.Bytes, r.Cursor
	path := r.LogPath
	mu.Unlock()

	var res ReadResult
	if end-start > MaxReadBytes {
		res.SkippedBytes = end - start - MaxReadBytes
		start = end - MaxReadBytes
	}
	if path != "" && end > start {
		if f, err := os.Open(path); err == nil {
			buf := make([]byte, end-start)
			n, _ := f.ReadAt(buf, start)
			_ = f.Close()
			res.Text = Sanitize(string(buf[:n]))
		}
	}
	if filter != "" && res.Text != "" {
		if re, err := regexp.Compile(filter); err == nil {
			lines := strings.Split(res.Text, "\n")
			kept := lines[:0:0]
			for _, l := range lines {
				if re.MatchString(l) {
					kept = append(kept, l)
				}
			}
			res.FilteredOut = len(lines) - len(kept)
			res.Text = strings.Join(kept, "\n")
		}
	}
	mu.Lock()
	if !noAdvance {
		r.Cursor = end
	}
	res.Info = r.snapshot()
	mu.Unlock()
	return res, true
}

// Kill stops one shell's whole process tree.
func Kill(sessionID, id string) (Info, bool) {
	mu.Lock()
	r := bucketOf(sessionID).byID[id]
	if r == nil {
		mu.Unlock()
		return Info{}, false
	}
	if r.Status == "running" {
		r.killRequested = true
		if r.PID > 0 {
			killTree(r.PID)
		}
	}
	info := r.snapshot()
	mu.Unlock()
	return info, true
}

// TakeNotices is exits the model has not been told about, cleared as taken.
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

// KillSession kills every running shell in one session.
func KillSession(sessionID string) int {
	n := 0
	for _, s := range List(sessionID) {
		if s.Status == "running" {
			Kill(sessionID, s.ID)
			n++
		}
	}
	return n
}

// KillAll kills everything, everywhere — the process is going away.
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

// ResetForTests drops all state without killing.
func ResetForTests() {
	mu.Lock()
	sessions = map[string]*bucket{}
	listeners = map[int]func(Info){}
	mu.Unlock()
}
