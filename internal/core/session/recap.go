package session

import (
	"path/filepath"
	"sort"
	"strings"
)

// Recap summarises a session from its own history.
//
// Derived, not generated: the conversation already records every tool call,
// so counting them is free, instant, and exactly true. Asking the model to
// summarise instead would cost a request and could be wrong about what it
// did, which is the one thing a recap must not be.

// Recap is what a session has done so far.
type Recap struct {
	Turns int
	// Tools is a count per tool name.
	Tools map[string]int
	// Read and Changed are repo-relative paths, sorted.
	Read    []string
	Changed []string
	// Commands are the shell commands run, most recent last.
	Commands []string
}

// Recap walks the history and totals it up.
func (s *Session) Recap(cwd string) Recap {
	r := Recap{Tools: map[string]int{}}
	read := map[string]bool{}
	changed := map[string]bool{}

	for _, msg := range s.Messages {
		for _, part := range Parts(msg) {
			switch p := part.(type) {
			case ReplayUser:
				r.Turns++
			case ReplayToolCall:
				r.Tools[p.Name]++
				switch p.Name {
				case "read", "ls":
					if path := argString(p.Input, "path", "file_path"); path != "" {
						read[rel(path, cwd)] = true
					}
				case "write", "edit":
					if path := argString(p.Input, "path", "file_path"); path != "" {
						changed[rel(path, cwd)] = true
					}
				case "bash":
					if cmd := argString(p.Input, "command"); cmd != "" {
						r.Commands = append(r.Commands, firstLine(cmd))
					}
				}
			}
		}
	}

	// A file that was changed is not also reported as merely read: the
	// stronger fact is the interesting one.
	for path := range changed {
		delete(read, path)
	}
	r.Read = sortedKeys(read)
	r.Changed = sortedKeys(changed)
	return r
}

func argString(args map[string]any, names ...string) string {
	for _, n := range names {
		if v, ok := args[n].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func rel(path, cwd string) string {
	if cwd == "" {
		return path
	}
	if r, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return path
}

func firstLine(s string) string {
	return strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
