// Package sound is loop's interface chimes (notifications/sound.ts): a real
// chime through afplay on macOS, the terminal bell everywhere else, and the
// bell always for attention so a multiplexer marks the pane. Every failure
// degrades to exactly one bell, never an error.
//
// The cues are loop's, derived from cuelume's MIT-licensed sound recipes by
// Daniel Belyi (see loop's THIRD_PARTY_NOTICES.md).
package sound

import (
	"bytes"
	"embed"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/core/config"
)

//go:embed cues/*
var cues embed.FS

// Cue names, as loop's.
const (
	Success = "success"
	Error   = "error"
	Bloom   = "bloom"
	Press   = "press"
	Click   = "click"
	Release = "release"
	Toggle  = "toggle"
)

var ext = map[string]string{Success: ".m4a", Error: ".m4a", Bloom: ".m4a", Press: ".m4a",
	Click: ".caf", Release: ".m4a", Toggle: ".m4a"}

const macPlayer = "/usr/bin/afplay"

// DefaultLevel is on only where a real player exists.
func DefaultLevel() string {
	if runtime.GOOS == "darwin" {
		return "on"
	}
	return "off"
}

func parseLevel(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "":
		return ""
	case "max":
		return "max"
	case "0", "false", "off":
		return "off"
	}
	return "on"
}

// Level is off | on | max: LOOP_SOUND, then the `sound` setting, then the
// platform default.
func Level() string {
	if v, ok := os.LookupEnv("LOOP_SOUND"); ok {
		if l := parseLevel(v); l != "" {
			return l
		}
	}
	var s string
	if config.SettingsStore().Get("sound", &s) && (s == "off" || s == "on" || s == "max") {
		return s
	}
	return DefaultLevel()
}

// SetLevel stores the `sound` setting.
func SetLevel(level string) error { return config.SettingsStore().Set("sound", level) }

// Bell is where the bell goes; the TUI may replace it. Straight to stderr by
// default, because the renderer owns stdout's cursor.
var Bell = func() { _, _ = os.Stderr.WriteString("\x07") }

func bell() {
	defer func() { _ = recover() }()
	Bell()
}

var (
	mu           sync.Mutex
	materialized = map[string]string{}
)

// Materialize writes a cue to $TMPDIR/loop-<cue><ext> (reused when already
// there byte for byte) and returns its path, or "" when it could not.
func Materialize(cue string) string {
	mu.Lock()
	defer mu.Unlock()
	if p, ok := materialized[cue]; ok {
		return p
	}
	result := ""
	defer func() { materialized[cue] = result }()
	data, err := cues.ReadFile("cues/" + cue + ext[cue])
	if err != nil {
		return ""
	}
	dir := os.Getenv("TMPDIR")
	if dir == "" {
		dir = os.TempDir()
	}
	path := filepath.Join(dir, "loop-"+cue+ext[cue])
	if st, err := os.Lstat(path); err == nil && st.Mode().IsRegular() && st.Size() == int64(len(data)) {
		if have, err := os.ReadFile(path); err == nil && bytes.Equal(have, data) {
			result = path
			return result
		}
	}
	staging := path + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	f, err := os.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ""
	}
	_, werr := f.Write(data)
	_ = f.Close()
	if werr != nil || os.Rename(staging, path) != nil {
		_ = os.Remove(staging)
		return ""
	}
	result = path
	return result
}

func chime(cue string, bellOnFailure bool) {
	if runtime.GOOS != "darwin" {
		if bellOnFailure {
			bell()
		}
		return
	}
	path := Materialize(cue)
	if path == "" {
		if bellOnFailure {
			bell()
		}
		return
	}
	cmd := exec.Command(macPlayer, path)
	if cmd.Start() != nil {
		if bellOnFailure {
			bell()
		}
		return
	}
	// Reap it; nothing waits on a 400 ms chime.
	go func() { _ = cmd.Wait() }()
}

// Cue plays a state-change cue. Silent at off.
func Cue(cue string) {
	if Level() == "off" {
		return
	}
	chime(cue, true)
}

// MaxCue plays an incidental cue, only at max.
func MaxCue(cue string) {
	if Level() != "max" {
		return
	}
	chime(cue, true)
}

// Attention is "the agent is waiting on you": always the bell, plus a chime.
func Attention() {
	if Level() == "off" {
		return
	}
	bell()
	chime(Success, false)
}
