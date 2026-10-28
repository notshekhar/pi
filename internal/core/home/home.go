// Package home is where pi keeps everything: loop's own ~/.loop.
//
// pi does not have a home of its own. It reads and writes loop's directory
// with loop's formats, so the two are interchangeable on the same machine — a
// session started in one resumes in the other, an agent written for one runs
// in both. Every path into ~/.loop is built here, so "where does X live" has
// one answer, and it is loop's.
package home

import (
	"os"
	"path/filepath"
	"strings"
)

// DirName is loop's config directory name.
const DirName = ".loop"

// Dir is ~/.loop. Like loop's getConfigDir it follows HOME and nothing else,
// so pointing HOME at a scratch directory isolates both programs the same way.
func Dir() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		h = "."
	}
	return filepath.Join(h, DirName)
}

// Path joins elements under ~/.loop.
func Path(elem ...string) string { return filepath.Join(append([]string{Dir()}, elem...)...) }

// AgentDir is ~/.loop/agent — skills, prompts, themes, transcript addresses.
func AgentDir(elem ...string) string { return Path(append([]string{"agent"}, elem...)...) }

// Ensure creates ~/.loop (or a directory under it) if it is missing.
func Ensure(elem ...string) (string, error) {
	dir := Path(elem...)
	return dir, os.MkdirAll(dir, 0o700)
}

// SlugCwd is loop's slugCwd: "/Users/a/b" → "--Users-a-b--".
func SlugCwd(cwd string) string {
	return "--" + strings.ReplaceAll(strings.Trim(cwd, "/"), "/", "-") + "--"
}

// TranscriptPath is a session's public address — loop keeps the historical
// JSONL shape as the session's NAME (hooks' transcript_path, picker values),
// though the entries themselves live in agent.db.
func TranscriptPath(cwd, id string) string {
	return AgentDir("sessions", SlugCwd(cwd), id+".jsonl")
}
